package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"github.com/gorilla/websocket"
	"github.com/gin-gonic/gin"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"strings"
	"time"
	"encoding/json"
	"github.com/stretchr/testify/assert"
)

func makeWsURL(server *httptest.Server, endpoint string) string {
	return "ws" + strings.TrimPrefix(server.URL, "http") + "/" + endpoint
}

func readWaitFor(ws *websocket.Conn, ty string, t *testing.T, logDiscards bool) []byte {
	for {
		_, m, rerr := ws.ReadMessage()
		if rerr != nil {
			t.Fatalf("Error reading json in test: %v", rerr)
		}
		if strings.Contains(string(m[:]), ty) {
			return m
		} else if logDiscards {
			t.Logf("Discarding msg %v\n", string(m[:]))
		}
	}
}

func receiveOnce(ws *websocket.Conn, t *testing.T) []byte {
	_, m, rerr := ws.ReadMessage()
	if rerr != nil {
		t.Fatalf("Error reading json in test: %v", rerr)
	}
	return m
}

func WrapMessage(typ string, m interface{}, t *testing.T) Message {
	s, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Err wrapping msg in test: %v", err)
	}
	return Message{Type: typ, Payload: s}
}

func setDefaultId(id string) gin.HandlerFunc {
	return func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set("steamid", id)
		session.Save()
	}
}

type errHandler func(error, *testing.T)

func defaultErrHandler(err error, t *testing.T) {
	if err != nil {
		t.Fatalf("Got error from WsServer route: %v", err)
	}
}

func createServerHandler(mgeme *webServer, onWsError errHandler, t *testing.T, middlewares ...gin.HandlerFunc) http.Handler {
	gin.SetMode("test")
	rout := gin.Default() //create a new router
	
	store := cookie.NewStore([]byte("SECRET"))
	rout.Use(sessions.Sessions("sessions", store))
	
	rout.Use(middlewares...)
	
	rout.GET("/user", func (c *gin.Context) {
		err := mgeme.WsServer(c, "user")
		onWsError(err, t)
	})
	
	rout.GET("/server", func (c *gin.Context) {
		err := mgeme.WsServer(c, "game")
		onWsError(err, t)
	})
	
	return rout.Handler()
}

func createUserAndServerConns(handler http.Handler, t *testing.T) (*websocket.Conn, *websocket.Conn, *httptest.Server) {
	server := httptest.NewServer(handler)
	wsURL := makeWsURL(server, "")
	
	gameConn, _, err := websocket.DefaultDialer.Dial(wsURL + "server", nil)
	if err != nil {
		t.Fatalf("Error dialing server endpoint %v", err)
	}
	
	userConn, _, err := websocket.DefaultDialer.Dial(wsURL + "user", nil)
	if err != nil {
		t.Fatalf("Error dialing user endpoint %v", err)
	}
	
	err = gameConn.WriteJSON(WrapMessage("ServerHello", ServerHelloWorld{ServerNum: "1", ServerHost: "FakeHost"}, t))
	if err != nil {
		t.Fatalf("Error writing server-hello %v", err)
	}
	_ = readWaitFor(gameConn, "ServerAck", t, false)
	t.Log("Gameconn acknowledged")
	return userConn, gameConn, server
}

//A connection with no steamid
func TestRejectUnlogged(t *testing.T) {
	mgeme := newWebServer()
	sv := createServerHandler(
		mgeme,
		func(err error, t *testing.T) {
			if err.Error() != "Rejecting websocket connection for unloggedin user" {
				t.Fatalf("Incorrect error when attempting to init ws connection while unloggedin %v", err)
			}
		},
		t,
		//if we included this we would be "logged in": SetDefaultId("FakePlayer123456789"),
		GetUser(), //Required to check if loggedin
	)
	
	server := httptest.NewServer(sv)
	defer server.Close()
	wsURL := makeWsURL(server, "user")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != websocket.ErrBadHandshake {
		if err == nil {
			t.Fatalf("Unexpected success opening ws connection")
			ws.Close() //Don't defer for this test because if there's an error, calling ws.Close() creates a new error :-)
		} else {
			t.Fatalf("Unexpecting error attempting opening ws connection on %s %v", wsURL, err)
		}
	}
}

//A connection with a steamid should be able to open a connection.
func TestConnect(t *testing.T) {
	mgeme := newWebServer()
	sv := createServerHandler(
		mgeme,
		defaultErrHandler,
		t,
		setDefaultId("FakePlayer123456789"),
		GetUser(),
	)
	
	server := httptest.NewServer(sv)
	defer server.Close()
	wsURL := makeWsURL(server, "user")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("could not open a ws connection on %s %v\n", wsURL, err)
	}
	defer ws.Close()
	
	//Initial connection
	m := receiveOnce(ws, t) //The queue is always sent after connecting. We want to receive this
	var ac AckQueue
	err = json.Unmarshal(m, &ac)
	if err != nil {
		t.Fatalf("Could not read queue ack msg: %v\n", err)
	}
	if (ac.IsInQueue || len(ac.Queue) > 0) {
		t.Fatalf("Queue is not empty. IIQ: %v | Queue: %v\n", ac.IsInQueue, ac.Queue)
	}
}

//Most of these tests could be run in parallel, technically, but at the moment that wouldn't really speed anything up and would just make the logs more confusing. To make a test parallel, add t.Parallel() to the start

//Neither users respond to the ready up signal
func TestReadyUpExpire(t *testing.T) {
	mgeme := newWebServer()
	mgeme.playerHub.addConnection(&connection{
			sendText: make(chan []byte, 256), 
			sendJSON: make(chan interface{}, 1024),
			playerReady: make(chan bool, 8),
			id: "Mario",
		})
	sv := createServerHandler(
		mgeme,
		defaultErrHandler,
		t,
		setDefaultId("Luigi"),
		GetUser(), //Required to check if loggedin
	)
	
	userConn, gameConn, server := createUserAndServerConns(sv, t)
	defer userConn.Close()
	defer gameConn.Close()
	defer server.Close()
	
	mario, _ := mgeme.playerHub.findConnection("Mario")
	luigi, _ := mgeme.playerHub.findConnection("Luigi")
	mgeme.queueUpdate(true, mario)
	mgeme.queueUpdate(true, luigi)
	
	match, err := mgeme.dummyMatch()
	if err != nil {
		t.Fatalf("Error forming dummy match %v", err)
	}
	if match.players[0].Connection == nil || match.players[1].Connection == nil {
		t.Fatalf("One of the player connections is nil. %v", match.players)
	}
	go mgeme.sendReadyUpPrompt(match)

	msg := readWaitFor(userConn, "RupSignal", t, false)
	var rs RupSignal
	json.Unmarshal(msg, &rs)
	t.Logf("Rup signal: %v", rs)
	if (!rs.ShowPrompt || rs.SelfRupped) {
		t.Fatalf("[RUP signal begin] Incorrect values for showprompt or selfrupped: (want true & false, got: %t & %t)\n", rs.ShowPrompt, rs.SelfRupped)
	}
	
	msg = readWaitFor(userConn, "RupSignal", t, false)
	json.Unmarshal(msg, &rs)
	t.Logf("Rup signal: %v", rs)
	if (rs.ShowPrompt || rs.SelfRupped) {
		t.Fatalf("[RUP signal expire] Incorrect value for showprompt or selfrupped: (want false & false, got: %t & %t) \n", rs.ShowPrompt, rs.SelfRupped)
	}
	
	_, waiterInQueue := mgeme.gameQueue["Waiter"]
	_, baiterInQueue := mgeme.gameQueue["Baiter"]
	if (baiterInQueue || waiterInQueue) {
		t.Fatalf("Waiter or Baiter values not correct. (Want false & false, got %t & %t)\n", waiterInQueue, baiterInQueue)
	}
}

//One user readies, the other doesn't
func TestReadyUpMismatch(t *testing.T) {
	mgeme := newWebServer()
	mgeme.playerHub.addConnection(&connection{
			sendText: make(chan []byte, 256), 
			sendJSON: make(chan interface{}, 1024),
			playerReady: make(chan bool, 8),
			id: "Baiter", //this user doesn't ready
		})
		
	sv := createServerHandler(
		mgeme,
		defaultErrHandler,
		t,
		setDefaultId("Waiter"), //this user readies
		GetUser(), //Required to check if loggedin
	)
	
	userConn, gameConn, server := createUserAndServerConns(sv, t)
	defer userConn.Close()
	defer gameConn.Close()
	defer server.Close()
	
	waiter, _ := mgeme.playerHub.findConnection("Waiter") //the client connection that wraps around the websocket connection userConn
	baiter, _ := mgeme.playerHub.findConnection("Baiter")
	mgeme.queueUpdate(true, waiter)
	mgeme.queueUpdate(true, baiter)
	
	match, err := mgeme.dummyMatch()
	if err != nil {
		t.Fatalf("Error forming dummy match %v", err)
	}
	if match.players[0].Connection == nil || match.players[1].Connection == nil {
		t.Fatalf("One of the player connections is nil. %v", match.players)
	}
	go mgeme.sendReadyUpPrompt(match)

	//Initial signal to ready up
	msg := readWaitFor(userConn, "RupSignal", t, false)
	var rs RupSignal
	json.Unmarshal(msg, &rs)
	t.Logf("Rup signal: %v", rs)
	if (!rs.ShowPrompt || rs.SelfRupped) {
		t.Fatalf("[RUP signal begin] Incorrect values for showprompt or selfrupped: (want true & false, got: %t & %t)\n", rs.ShowPrompt, rs.SelfRupped)
	}
	rupIn2Seconds := time.NewTimer(2 * time.Second)
	<-rupIn2Seconds.C
	waiter.playerReady <- true
	
	msg = readWaitFor(userConn, "RupSignal", t, false)
	json.Unmarshal(msg, &rs)
	t.Logf("Rup signal: %v", rs)
	if (rs.ShowPrompt || rs.SelfRupped) {
		t.Fatalf("[RUP signal expire] Incorrect value for showprompt or selfrupped: (want false & true, got: %t & %t) \n", rs.ShowPrompt, rs.SelfRupped)
	}
	
	_, waiterInQueue := mgeme.gameQueue["Waiter"]
	_, baiterInQueue := mgeme.gameQueue["Baiter"]
	if (baiterInQueue || !waiterInQueue) {
		t.Fatalf("Waiter or Baiter values not correct. (Want true & false, got %t & %t)\n", waiterInQueue, baiterInQueue)
	}
}

//Todo:
//func TestReadyUpBothGood(t *testing.T) {

/*func TestWaitingForPlayersOneGood(t *testing.T) {
	mgeme := newWebServer()
	mgeme.wfpSeconds = 3
	mgeme.playerHub.addConnection(&connection{
			sendText: make(chan []byte, 256), 
			sendJSON: make(chan interface{}, 1024),
			playerReady: make(chan bool, 8),
			id: "Baiter", //this user doesn't ready
		})
		
	sv := createServerHandler(
		mgeme,
		defaultErrHandler,
		t,
		setDefaultId("Waiter"), //this user readies
		GetUser(), //Required to check if loggedin
	)
	
	userConn, gameConn, server := createUserAndServerConns(sv, t)
	defer userConn.Close()
	defer gameConn.Close()
	defer server.Close()
	
	waiter, _ := mgeme.playerHub.findConnection("Waiter") //the client connection that wraps around the websocket connection userConn
	baiter, _ := mgeme.playerHub.findConnection("Baiter")
	
	match := mgeme.createMatchObject([]PlayerAdded{
		PlayerAdded{Connection: Waiter, Steamid: "Waiter"}, PlayerAdded{Connection: Baiter, Steamid: "Baiter"}
	})
	go mgeme.initializeMatch(match)
	
	go func() {
		//Mocking the game server. We want to send back info that one 
		_ := readWaitFor(gameConn, "MatchDetails", t, false)
		var md Match
		json.Unmarshal(msg, &md)
	}()
}*/

func TestUpdateBanLevel(t *testing.T) {
	updateBanMethod = updateBanMock
	//The penalize package should call `now` when it wants time.Now(), here it will return whatever value we set testNow to.
	var testNow = time.Now()
	now = func() time.Time {
		return testNow
	}

	b := createBan("myId", true)
	assert.Equal(t, 0, b.banLevel, "ban levels should start at 0")
	assert.Equal(t, now().Add(expireLadder[0]), b.expires, "expire should be equal to first ban level (idx 0)")

	testNow = testNow.Add(1 * dayDur)
	b.newPenalty()
	assert.Equal(t, 1, b.banLevel, "ban level should have increased by 1")
	assert.Equal(t, now().Add(expireLadder[1]), b.expires, "expire should be equal to second ban level (idx 1)")

	testNow = now().Add(9 * dayDur)
	b.newPenalty()	//First, set level according to time passed since last offence. Then increase by 1. (We should go to 0 then back to 1)
	assert.Equal(t, 0, b.banLevel, "ban level should have gone to 0")
	assert.Equal(t, now().Add(expireLadder[0]), b.expires, "expire should be equal to first ban level (idx 0)")
	
	b.newPenalty()
	b.newPenalty()
	b.newPenalty()
	assert.Equal(t, 3, b.banLevel, "ban level should be 3")
	assert.Equal(t, now().Add(expireLadder[3]), b.expires, "expire should be equal to fourth ban level (idx 3)")
	
	testNow = now().Add(2 * weekDur)
	b.newPenalty()
	assert.Equal(t, 1, b.banLevel, "ban level should be at 3 - 2 = 1")
	assert.Equal(t, now().Add(expireLadder[1]), b.expires, "expire should be equal to second ban level (idx 2)")
	
	testNow = now().Add(1 * weekDur)
	assert.Equal(t, b.isActive(), false, "ban should be expired")
}

func TestDelinquency(t *testing.T) {
	mgeme := newWebServer()
	mgeme.playerHub.addConnection(&connection{
		id: "B",
		sendJSON: make(chan interface{}, 1024),
	})
	sv := createServerHandler(
		mgeme,
		defaultErrHandler,
		t,
		setDefaultId("A"),
		GetUser(),
	)

	userConn, gameConn, server := createUserAndServerConns(sv, t)
	defer userConn.Close()
	defer gameConn.Close()
	defer server.Close()

	gc, obj := mgeme.gameServerHub.findConnection("1")
	if gc == nil {
		t.Fatal("No game server connection found")
	}
	gsv := obj.(*gameServer)

	A, _ := mgeme.playerHub.findConnection("A")
	B, _ := mgeme.playerHub.findConnection("B")

	match := createMatchObject([]PlayerAdded{
		PlayerAdded{Connection: A, Steamid: "A"}, PlayerAdded{Connection: B, Steamid: "B"},
	}, "1")
	go mgeme.initializeMatch(match)

	_ = readWaitFor(gameConn, "MatchDetails", t, false)
	if len(gsv.Matches) != 1 {
		t.Fatalf("Have %d matches instead of 1", len(gsv.Matches))
	}

	msg := WrapMessage("MatchCancel", MatchCancel{
		Delinquents: []string{"A"},
		Arrived: "2",
		Arena: 1,
	}, t)
	mgeme.HandleMessage(msg, gc.id, gc)
	
	tmr := time.NewTimer(1 * time.Second)
	<-tmr.C
	if len(gsv.Matches) > 0 {
		t.Fatalf("Didn't delete match")
	}
}

//TODO
/*
func TestAssignArena(t *testing.T) {
	gs := &gameServer{
		matchServerInfo{
			Id: "1",
		},
	}
}
*/