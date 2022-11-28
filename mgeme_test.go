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
	_"time"
	"encoding/json"
)

func MakeWsURL(server *httptest.Server) string {
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func ReceiveOnce(ws *websocket.Conn, t *testing.T) []byte {
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

//Some Middlewares
//I think this one is probably redundant but makes the code look nicer :) Actually I'm not using this at all. I can replace with a good function once I'm cleaning up code
func SetKeys(keys map[string]any) gin.HandlerFunc {
	return func(c *gin.Context) {
		for k, v := range keys { //I'm not going to do a straight c.Keys = keys reassignment here in case I want to append instead of replace.
			c.Set(k, v)
		}
	}
}

func SetDefaultId(id string) gin.HandlerFunc {
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

func CreateServerHandler(onWsError errHandler, t *testing.T, middlewares ...gin.HandlerFunc) http.Handler {
	gin.SetMode("test")
	rout := gin.Default() //create a new router
	
	store := cookie.NewStore([]byte("SECRET"))
	rout.Use(sessions.Sessions("sessions", store))
	
	rout.Use(middlewares...)
	
	rout.GET("/", func (c *gin.Context) {
		err := WsServer(c)
		onWsError(err, t)
	})
	
	return rout.Handler()
}

func TestRejectUnlogged(t *testing.T) {
	sv := CreateServerHandler(func(err error, t *testing.T) {
			if err.Error() != "Rejecting websocket connection for unloggedin user" {
				t.Fatalf("Incorrect error when attempting to init ws connection while unloggedin %v", err)
			}
		},
		t,
		SetKeys(map[string]any{
			"Hub": newHub("user"),
		}),
		//if we included this we would be "logged in": SetDefaultId("FakePlayer123456789"),
		GetUser(), //Required to check if loggedin
	)
	
	server := httptest.NewServer(sv)
	defer server.Close()
	wsURL := MakeWsURL(server)
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

func TestConnect(t *testing.T) {
	sv := CreateServerHandler(defaultErrHandler,
		t,
		SetKeys(map[string]any{
			"Hub": newHub("user"),
		}),
		SetDefaultId("FakePlayer123456789"),
		GetUser(),
	)
	
	server := httptest.NewServer(sv)
	defer server.Close()
	wsURL := MakeWsURL(server)
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("could not open a ws connection on %s %v\n", wsURL, err)
	}
	defer ws.Close()
	
	//Initial connection
	m := ReceiveOnce(ws, t) //The queue is always sent after connecting. We want to receive this
	var ac AckQueue
	err = json.Unmarshal(m, &ac)
	if err != nil {
		t.Fatalf("Could not read queue ack msg: %v\n", err)
	}
	if (ac.IsInQueue || len(ac.Queue) > 0) {
		t.Fatalf("Queue is not empty. IIQ: %v | Queue: %v\n", ac.IsInQueue, ac.Queue)
	}
}

/* gotta work onthis functionality
func TestHandleReconnect(t *testing.T) {
	gin.SetMode("test")
	rout := gin.Default()
	store := cookie.NewStore([]byte("SECRET"))
	rout.Use(sessions.Sessions("sessions", store))
	rout.GET("/", func (c *gin.Context) {
		c.Keys = map[string]any{
			"steamid": "FakePlayer123456789",
			"Hub": newHub("user"),
			"User": User{
				id: "FakePlayer123456789",
				elo: 1000,
			},
		}
		err := WsServer(c)
		if err != nil {
			t.Fatalf("%v", err)
		}
	})
	
	server := httptest.NewServer(rout.Handler())
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") //+ "/"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("could not open a ws connection on %s %v", wsURL, err)
	}
	
	_ = ReceiveOnce(ws, t) //The queue is always sent after connecting. Garbage it
	
	gq := len(GameQueue)
	if gq != 0 {
		t.Fatalf("Queue isn't new")
	}
	err = ws.WriteJSON(WrapMessage("QueueUpdate", QueueJoinLeave{Joining: true}, t))
	if err != nil {
		t.Fatalf("could not send queue join msg: %v", err)
	}
	
	m := ReceiveOnce(ws, t)
	var ac AckQueue
	err = json.Unmarshal(m, &ac)
	if err != nil {
		ws.Close()
		t.Fatalf("could not read queue ack msg: %v", err)
	}
	log.Println("Received ack msg", ac.IsInQueue, ac.Queue)
	ws.Close()
} */