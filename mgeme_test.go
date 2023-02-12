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
	"github.com/stretchr/testify/require"
	"sync"
	"os"
)

func makeWsURL(server *httptest.Server, endpoint string) string {
	return "ws" + strings.TrimPrefix(server.URL, "http") + "/" + endpoint
}

func readWaitFor(ws *websocket.Conn, ty string, t *testing.T, logDiscards bool) []byte {
	for {
		_, m, rerr := ws.ReadMessage()
		if rerr != nil {
			t.Fatalf("Error reading json in test: %v\n", rerr)
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
	require.NoErrorf(t, err, "Err wrapping msg in test: %v", err)
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
	require.NoErrorf(t, err, "Got error from WsServer route: %v", err)
}

func createServerHandler(mgeme *webServer, onWsError errHandler, t *testing.T, middlewares ...gin.HandlerFunc) http.Handler {
	gin.SetMode("test")
	rout := gin.Default() //create a new router
	
	store := cookie.NewStore([]byte("SECRET"))
	rout.Use(sessions.Sessions("sessions", store))
	
	rout.Use(middlewares...)
	
	//default user entrypoint. use /user?id=Mario to set contextual user object.
	rout.GET("/user", func (c *gin.Context) {
		id, ok := c.GetQuery("id")
		if ok {
			t.Logf("Setting user for query %s", id)
			c.Set("User", User{id: id})
		}
		err := mgeme.WsServer(c, "user")
		onWsError(err, t)
	})
	
	rout.GET("/server", func (c *gin.Context) {
		err := mgeme.WsServer(c, "game")
		onWsError(err, t)
	})
	
	return rout.Handler()
}

func createOneUser(t *testing.T, server *httptest.Server, withId string) *websocket.Conn {
	var endpoint string
	if withId != "" {
		endpoint = "user?id=" + withId
	} else {
		endpoint = "user"
	}
	wsURL := makeWsURL(server, endpoint)
	userConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoErrorf(t, err, "Error dialing user endpoint %v", err)
	return userConn
}

func createServer(handler http.Handler) *httptest.Server {
	server := httptest.NewServer(handler)
	return server
}

func createGameConn(t *testing.T, server *httptest.Server, writeHello bool) *websocket.Conn {
	wsURL := makeWsURL(server, "")
	
	gameConn, _, err := websocket.DefaultDialer.Dial(wsURL + "server", nil)
	require.NoErrorf(t, err, "Error dialing server endpoint %v", err)
	if writeHello {
		err = gameConn.WriteJSON(WrapMessage("ServerHello", ServerHelloWorld{Secret: os.Getenv("MGEME_SV_SECRET"), ServerNum: "1", ServerHost: "FakeHost"}, t))
		require.NoErrorf(t, err, "Error writing server-hello %v", err)
		
		_ = readWaitFor(gameConn, "ServerAck", t, false)
		t.Log("Gameconn acknowledged")
	}
	return gameConn
}

func createServerAndGameConns(t *testing.T, handler http.Handler, writeHello bool) (*websocket.Conn, *httptest.Server) {
	server := createServer(handler)
	gameConn := createGameConn(t, server, writeHello)
	
	return gameConn, server
}

//A connection with no steamid
func TestRejectUnlogged(t *testing.T) {
	mgeme := newWebServer()
	sv := createServerHandler(
		mgeme,
		func(err error, t *testing.T) {
			require.EqualErrorf(t, err, "Rejecting websocket connection for unloggedin user", "Incorrect error when attempting to init ws connection while unloggedin %v", err)
			/*if err.Error() != "Rejecting websocket connection for unloggedin user" {
				t.Fatalf("Incorrect error when attempting to init ws connection while unloggedin %v", err)
			}*/
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
	updateBanMethod = updateBanMock
	selectBanMethod = selectBanMock

	mgeme := newWebServer()
	sv := createServerHandler(
		mgeme,
		defaultErrHandler,
		t,
		setDefaultId("76561198292350104"),
		GetUser(),
	)
	
	server := httptest.NewServer(sv)
	defer server.Close()
	wsURL := makeWsURL(server, "user")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoErrorf(t, err, "Could not open a ws connection on %s: %v", wsURL, err)
	defer ws.Close()
	
	//Initial connection
	m := receiveOnce(ws, t) //The queue is always sent after connecting. We want to receive this
	var ac AckQueue
	err = json.Unmarshal(m, &ac)
	require.NoErrorf(t, err, "Could not read queue ack msg: %v", err)

	require.Equal(t, 0, len(ac.Queue), "Queue should be empty")
	require.Equal(t, false, ac.IsInQueue, "Should not be marked in queue")
}

//Most of these tests could be run in parallel, technically, but at the moment that wouldn't really speed anything up and would just make the logs more confusing. To make a test parallel, add t.Parallel() to the start

func TestReadyUp(t *testing.T) {
	type rupper map[string]int //key: id, int: seconds after signal to rup
	type exq map[string]bool

	var tests = []struct{
		name string
		responders rupper
		matchOrQueue string
		expectedQueue exq
	}{
		{name: "A", responders: make(rupper), matchOrQueue: "q", expectedQueue: make(exq)},
		{name: "B", responders: rupper{"Mario": 1}, matchOrQueue: "q", expectedQueue: exq{"Mario": true}},
		{name: "C", responders: rupper{"Mario": 1, "Luigi": 1}, matchOrQueue: "m", expectedQueue: make(exq)}, //after put in a match, q should be empty
		{name: "D", responders: rupper{"Mario": 1, "Luigi": 6}, matchOrQueue: "q", expectedQueue: exq{"Mario": true}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			mgeme := newWebServer()
			sv := createServerHandler(mgeme, defaultErrHandler, t)
				
			gameConn, server := createServerAndGameConns(t, sv, true)

			wsConnA := createOneUser(t, server, "Luigi")
			wsConnB := createOneUser(t, server, "Mario")
			
			//Add two players to queue
			players := make(map[string]*websocket.Conn)
			var mario *connection
			var luigi *connection
			
			player_cond := func() bool {
				mario, _ = mgeme.playerHub.findConnection("Mario")
				luigi, _ = mgeme.playerHub.findConnection("Luigi")
				return mario != nil && luigi != nil
			}
			
			require.Eventuallyf(t, player_cond, 1 * time.Second, 10 * time.Millisecond, "Player conns shouldn't be nil in test %s", test.name)
			
			mgeme.queueUpdate(true, mario)
			mgeme.queueUpdate(true, luigi)
			players["Luigi"] = wsConnA
			players["Mario"] = wsConnB
			
			match, err := mgeme.dummyMatch()
			require.NoErrorf(t, err, "Error forming dummy match: %v", err)
			var wgSendPrompt sync.WaitGroup
			wgSendPrompt.Add(1)
			go mgeme.sendReadyUpPrompt(match, &wgSendPrompt)
			
			expected_rs := NewRupSignalMsg(true, false, mgeme.rupTime)
			msg := readWaitFor(wsConnA, "RupSignal", t, false)
			var rs RupSignal
			json.Unmarshal(msg, &rs)
			require.EqualValues(t, expected_rs, rs, "Rup signal value not what was expected")

			var wgSendingRups sync.WaitGroup
			for id, conn := range players {
				wgSendingRups.Add(1)
				go func(id string, conn *websocket.Conn) {
					defer wgSendingRups.Done()
					seconds, exists := test.responders[id]
					if exists {
						<-time.After(time.Duration(seconds) * time.Second)
						t.Log("Test-Sending rup signal....")
						conn.WriteJSON(Message{Type: "Ready"})
					}
				}(id, conn)
			}
			wgSendingRups.Wait()
			queue_cond := func() bool {
				mgeme.queueMutex.RLock()
				_, mq := mgeme.gameQueue["Mario"]
				_, lq := mgeme.gameQueue["Luigi"]
				mgeme.queueMutex.RUnlock()
				return (test.expectedQueue["Mario"] == mq && test.expectedQueue["Luigi"] == lq)
			}
			_ = readWaitFor(wsConnA, "RupSignal", t, false)
			require.Eventuallyf(t, queue_cond, 500 * time.Millisecond, 10*time.Millisecond, "Queues should be equal in test %s", test.name)
			if test.matchOrQueue == "m" {
				_, obj := mgeme.gameServerHub.findConnection("1")
				<-time.After(500 * time.Millisecond)
				require.True(t, obj.(*gameServer).findMatchByPlayer("Mario") != -1)
			}
			wgSendPrompt.Wait()
			
			gameConn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			gameConn.Close()
			
			wsConnA.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			wsConnB.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			
			wsConnA.Close()
			wsConnB.Close()
			server.CloseClientConnections()
			server.Close()
			
			<-time.After(time.Second)
		})
	}
}


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
	//The penalize package should call `now` when it wants time.Now(), here it will return whatever value we set testNow to so we can time travel (if time travel is not required, we can use regular now anyway).
	var testNow = time.Now()
	now = func() time.Time {
		return testNow
	}

	b := createBan("myId", true)
	require.Equal(t, 0, b.banLevel, "ban levels should start at 0")
	require.Equal(t, now().Add(expireLadder[0]), b.expires, "expire should be equal to first ban level (idx 0)")

	testNow = testNow.Add(1 * dayDur)
	b.newPenalty()
	require.Equal(t, 1, b.banLevel, "ban level should have increased by 1")
	require.Equal(t, now().Add(expireLadder[1]), b.expires, "expire should be equal to second ban level (idx 1)")

	testNow = now().Add(9 * dayDur)
	b.newPenalty()	//First, set level according to time passed since last offence. Then increase by 1. (We should go to 0 then back to 1)
	require.Equal(t, 0, b.banLevel, "ban level should have gone to 0")
	require.Equal(t, now().Add(expireLadder[0]), b.expires, "expire should be equal to first ban level (idx 0)")
	
	b.newPenalty()
	b.newPenalty()
	b.newPenalty()
	require.Equal(t, 3, b.banLevel, "ban level should be 3")
	require.Equal(t, now().Add(expireLadder[3]), b.expires, "expire should be equal to fourth ban level (idx 3)")
	
	testNow = now().Add(2 * weekDur)
	b.newPenalty()
	require.Equal(t, 1, b.banLevel, "ban level should be at 3 - 2 = 1")
	require.Equal(t, now().Add(expireLadder[1]), b.expires, "expire should be equal to second ban level (idx 2)")
	
	testNow = now().Add(1 * weekDur)
	require.Equal(t, false, b.isActive(), "ban should be expired")
}

func TestDelinquency(t *testing.T) {
	updateBanMethod = updateBanMock
	selectBanMethod = selectBanMock
	
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

	gameConn, server := createServerAndGameConns(t, sv, true)
	defer gameConn.Close()
	defer server.Close()

	gc, obj := mgeme.gameServerHub.findConnection("1")
	require.NotNil(t, gc, "Game server should be found")
	require.NotNil(t, obj, "Game object should not be nil")
	gsv := obj.(*gameServer)

	A, _ := mgeme.playerHub.findConnection("A")
	B, _ := mgeme.playerHub.findConnection("B")

	match := createMatchObject([]PlayerAdded{
		PlayerAdded{Connection: A, Steamid: "A"}, PlayerAdded{Connection: B, Steamid: "B"},
	}, "1")
	go mgeme.initializeMatch(match)

	_ = readWaitFor(gameConn, "MatchDetails", t, false)
	require.Equal(t, 1, len(gsv.Matches), "Should only have 1 match")

	msg := WrapMessage("MatchCancel", MatchCancel{
		Delinquents: []string{"A"},
		Arrived: "2",
		Arena: 1,
	}, t)
	
	mgeme.HandleMessage(msg, gc.id, gc)
	
	tmr := time.NewTimer(1 * time.Second)
	<-tmr.C
	require.Equal(t, 0, len(gsv.Matches), "Should have deleted all matches")
	
	b := getBan("A")
	require.NotNil(t, b, "Should get ban")
	require.Equal(t, now().Add(expireLadder[0]).Truncate(time.Second), b.expires.Truncate(time.Second), "ID A should be banned until now+30 minutes")
	//I'm using truncate here even though in other tests I may prefer to compare time.Time to time.Time, because actually waiting on messages being sent produces slight differences in times.
}

func TestCacheBans(t *testing.T) {
	selectBanMethod = selectBanMock
	updateBanMethod = updateBanMock
	
	banCache = newCache()
	testBanSlice = make([]*ban, 0)

	punishDelinquents([]string{"76561198292350104"})
	jb := checkBanCache("76561198011940487")
	require.NotNil(t, jb, "Should get league ban (uncached)")
	gb := checkBanCache("76561198292350104")
	require.NotNil(t, gb, "Should get penalty ban (uncached)")
	require.True(t, gb.isActive(), "Should get active penalty ban (uncached)")
	zb := checkBanCache("76561198098770013")
	require.Nil(t, zb, "Shouldn't get ban (uncached")
	///////////////////////////////////
	jb = checkBanCache("76561198011940487")
	require.NotNil(t, jb, "Should get league ban (cached)")
	require.Equal(t, -1, jb.banLevel, "Shouldn't be leveled")
	require.True(t, jb.isActive(), "Should be active")
	
	gb = checkBanCache("76561198292350104")
	require.NotNil(t, gb, "Should get penalty ban (cached)")
	zb = checkBanCache("76561198098770013")
	require.Nil(t, zb, "Shouldn't get ban (cached")
	
	lo := now().Add(-200 * time.Hour)
	testBanSlice = append(testBanSlice, &ban{
		steamid: "123",
		expires: now().Add(-100 * time.Hour),
		banLevel: 3,
		lastOffence: &lo,
	})
	xtra := checkBanCache("123")
	require.NotNil(t, xtra, "Should get expired and uncached penalty ban")
	require.False(t, xtra.isActive(), "Should be expired")
}

func TestSummaryCache(t *testing.T) {
	var tests = []struct{
		player string
		expected_final_name string
	}{
		{"76561198098770013", "Captain Zidgel"},
		{"76561198292350104", "Gibby from iCarly"},
	}
	
	for _, tt := range tests {
		_, err := getRGLSummary(tt.player)
		require.NoErrorf(t, err, "Should get RGL Summary instead of err %v", err)
		_, err = getSteamSummary(tt.player)
		require.NoErrorf(t, err, "Should get Steam Summary instead of err %v", err)
		
		final := getTotalSummary(tt.player)
		require.Equal(t, tt.expected_final_name, final.PersonaName, "Should have correct name")
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

func TestFailServerHello(t *testing.T) {
	mgeme := newWebServer()
	sv := createServerHandler(
		mgeme,
		defaultErrHandler,
		t,
	)

	gameConn, server := createServerAndGameConns(t, sv, false)
	defer gameConn.Close()
	defer server.Close()
	
	err := gameConn.WriteJSON(WrapMessage("MatchResults", MatchResults{Winner: "I won!", Loser: "You lose!", Finished: true}, t))
	require.NoErrorf(t, err, "JSON Writing error %v", err)
	
	m := readWaitFor(gameConn, "Error", t, true) //The queue is always sent after connecting. We want to receive this
	var e ErrorJson
	err = json.Unmarshal(m, &e)
	require.NoErrorf(t, err, "Could not read msg: %v", err)
	require.Equal(t, NewJsonError("Must receive ServerHello"), e, "Should receive 'must receive serverhello'")
	
	sh := ServerHelloWorld{
		Secret: "badSecret",
		ServerNum: "1",
		ServerHost: "1.2.3.4",
		ServerPort: "12345",
		StvPort: "12345",
	}
	
	err = gameConn.WriteJSON(WrapMessage("ServerHello", sh, t))
	require.NoErrorf(t, err, "JSON Writing error %v", err)
	
	m = readWaitFor(gameConn, "Error", t, true) //The queue is always sent after connecting. We want to receive this
	err = json.Unmarshal(m, &e)
	require.NoErrorf(t, err, "Could not read msg: %v", err)
	require.Equal(t, NewJsonError("Authorization failed"), e, "Should receive rejected thing idk")
	//For some reason, sends after the connection is closed don't error? So the actual client needs to be aware of what they're sending and receiving (no duh)
	//Reinit the gameConn to test correctly authing
	
	newconn := createGameConn(t, server, false)
	defer newconn.Close()
	sh.Secret = os.Getenv("MGEME_SV_SECRET")
	err = newconn.WriteJSON(WrapMessage("ServerHello", sh, t))
	require.NoErrorf(t, err, "JSON writing error %v", err)
	
	cond := func() bool {
		_, o := mgeme.gameServerHub.findConnection("1")
		if o != nil && o != struct{}{} {
			_, ok := o.(*gameServer)
			if ok {
				return true
			}
		}
		return false
	}
	require.Eventuallyf(t, cond, time.Second, 10*time.Millisecond, "Game server should be found validated")
}