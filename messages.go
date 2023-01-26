package main

import (
	"fmt"
	"encoding/json"
	"errors"
	"log"
	"time"
)

//helper function
func clearEmpties(s []string) []string {
	n := make([]string, 0)
	for x := range(s) {
		if s[x] != "" {
			n = append(n, s[x])
		}
	}
	return n
}

type Message struct { //This is a struct-ambiguous way to receive json messages. The type is used to cast the payload into the right struct.
	Type string `json:"type"`
	Payload json.RawMessage `json:"payload"`
}


//MESSAGES WE WILL RECEIVE
//User messages are structured (type, payload are siblings) and decoded from type Message

///
// User Messages
///
type QueueJoinLeave struct { //Comes from users: Join or leave the queue
	//this is probably going to be crazy bad code to read but using bool to enforce binary options instead of remembering integer values seems like a good idea
	Joining bool `json:"joining"` //true for join, false for leave
}

type TestMatch struct { //Can't do everything with unit testing! Send a pseudomatch to the server
	X string `json:"X"` //"1v1" picks 2 real players, "1v_" picks 1 real player
}

//User can also send a very simple {type: "Ready"} "ready" signal that doesn't need a payload

///
// Game Server Messages
///
type ServerHelloWorld struct { //Comes from gameserver to initialize connections.
	ApiKey string `json:"apiKey"`
	ServerNum string `json:"serverNum"`
	ServerHost string `json:"serverHost"`
	ServerPort string `json:"serverPort"`
	StvPort string `json:"stvPort"`
}

type MatchResults struct {
	Winner string `json:"winner"`
	Loser string `json:"loser"`
	Finished bool `json:"finished"` //true = normal finish, false = forfeit
}

type MatchCancel struct { //called when match is cancelled **before beginning**
	Delinquents []string `json:"delinquents"`
	Arrived string `json:"arrived"`
	Arena int `json:"arena"`
}

type MatchBegan struct {
	P1 string `json:"p1"`
	P2 string `json:"p2"`
}



//MESSAGES WE WILL SEND
//Our message types are flat (type is a part of payload)
type AckQueue struct {
	Type string `json:"type"`
	Queue PlayerEntries `json:"queue"`
	IsInQueue bool `json:"isInQueue"`
}

func NewAckQueueMsg (queue PlayerEntries, id string) AckQueue {
	_, ok := queue[id]
	return AckQueue{Type: "AckQueue", Queue: queue, IsInQueue: ok}
}

//Sent to server on connect
type ServerAck struct {
	Type string `json:"type"`
	Accept bool `json:"accepted"`
	RejectReason string `json:"rejectReason"`
}

func newServerAck(rejectReason string) ServerAck {
	s := ServerAck{}
	s.Type = "ServerAck"
	if rejectReason == "" {
		s.Accept = true
	} else {
		s.Accept = false
		s.RejectReason = rejectReason
	}
	return s
}

type HelloWorld struct {
	Hello string `json:"Hello"`
}

type RupSignal struct {
	Type string `json:"type"`
	ShowPrompt bool `json:"showPrompt"`
	SelfRupped bool `json:"selfRupped"`
	ExpireAt int64 `json:"expireAt"`
}

func NewRupSignalMsg(show bool, selfrupped bool, deadline int) RupSignal { //I should move to this format for all messages sent from Go server so I don't need to rewrite type
	return RupSignal{Type: "RupSignal", ShowPrompt: show, SelfRupped: selfrupped, ExpireAt: now().Add(time.Second * time.Duration(deadline)).Unix()}
}

type ServerIssue struct { //Used to communicate with users that something is interrupting the service (ie, a gameserver is down, the webserver is erroring, idk something like that
	Type string `json:"type"`
	Code int `json:"code"`
	Message string `json:"msg"`
}
//Codes: (First digit should relate to severity, 1 being a warning and 2 being a total interruption of service
//200 - Game servers not available

func alertPlayers(code int, message string, h *Hub) {
	msg := ServerIssue{Type: "ServerIssue", Code: code, Message: message}
	for c := range h.connections {
		c.sendJSON <- msg
	}
}

func (w *webServer) HandleMessage(msg Message, steamid string, conn *connection) error { //get steamid from server (which gets it from browser session after auth), don't trust users to send it in json. pass the websocket connection so we can send stuff back if needed, or pass it to further functions
	connType := conn.h.hubType
	if connType == "user" {
		if msg.Type == "QueueUpdate" { // {type: "QueueUpdate", payload: {joining: true/false}} Comes from users
			var res QueueJoinLeave
			err := json.Unmarshal(msg.Payload, &res) //write to that empty instance
			if err != nil {
				if err.Error() == "unexpected end of JSON input" {
					return errors.New("Malformed JSON input") //not necessarily unexpected end. could be bad format, ie payload named msg or something
				} else {
					return fmt.Errorf("Error unmarshaling QueueUpdate %v: %v", msg.Payload, err)
				}
			}
			w.queueUpdate(res.Joining, conn)	//Update the server's master queue
		} else if msg.Type == "Ready" {
			w.erMutex.Lock()
			if w.expectingRup[conn.id] {
				w.erMutex.Unlock()
				conn.playerReady <- true
			} else {
				w.erMutex.Unlock()
				log.Println("Warning: out of period rup signal")
			}
		} else if msg.Type == "TestMatch" {
			var res TestMatch
			json.Unmarshal(msg.Payload, &res)
			if res.X == "1v1" {
				m, err := w.dummyMatch()
				if err != nil { log.Fatalf("%v", err) }
				go w.sendReadyUpPrompt(m, nil)
			} else if res.X == "1v_" {
				m := &Match{
					Arena: 1,
					P1id: steamid,
					P2id: "FakePlayer",
					ServerId: "1",
					players: []PlayerAdded{PlayerAdded{Steamid: steamid, Connection: conn}, PlayerAdded{Steamid: "FakePlayer", Connection: NewFakeConnection()}},
				}
				w.initializeMatch(m)
			}
		} else {
			return fmt.Errorf("Unknown message type: %s", msg.Type)
		}
	} else if connType == "game" { //Ensure user connections can't send server messages
		if msg.Type == "ServerHello" {
			var res ServerHelloWorld
			err := json.Unmarshal(msg.Payload, &res)
			if err != nil {
				return fmt.Errorf("Error unmarshaling ServerHello %v: %v", msg.Payload, err)
			}
			fmt.Printf("Game Server %s connected\n", res.ServerNum)
			conn.id = res.ServerNum
			gs := &gameServer{
				Info: matchServerInfo{
					Id: res.ServerNum,
					Host: res.ServerHost,
					Port: res.ServerPort,
					Stv: res.StvPort,
				},
				Matches: make(map[int]*Match),
				Full: false,
			}
			conn.h.connections[conn] = gs
			conn.sendJSON <- newServerAck("") //Let the server know we accepted the connection
			//TODO: Implement API keying to guarantee game servers are permitted agents. Reject with conn.sendJSON <- newServerAck("Bad API Key")
		} else if msg.Type == "MatchResults" {
			var res MatchResults
			err := json.Unmarshal(msg.Payload, &res)
			if err != nil {
				return fmt.Errorf("Error unmarshaling MatchResults %v: %v", msg.Payload, err)
			}
			//do something
			if !res.Finished { //forfeit (punish "res.Loser")
				punishDelinquents([]string{res.Loser})
			}
			sv := conn.h.connections[conn].(*gameServer)
			matchIndex := sv.findMatchByPlayer(res.Winner)
			for _, player := range sv.Matches[matchIndex].players {
				player.Connection.sendJSON <- msg
			}
			sv.deleteMatch(matchIndex)
			fmt.Printf("Received MatchResults from Server %v, finish Mode %t", res, res.Finished)
		} else if msg.Type == "MatchCancel" {
			var res MatchCancel
			err := json.Unmarshal(msg.Payload, &res)
			if err != nil {
				return fmt.Errorf("Error unmarshaling MatchCancel %v: %v", msg.Payload, err)
			}
			fmt.Printf("Received matchcancel %v\n", res)
			sv := conn.h.connections[conn].(*gameServer)
			delinquents := clearEmpties(res.Delinquents)
			matchIndex := sv.findMatchByPlayer(delinquents[0])
			if matchIndex == -1 {
				fmt.Println("Couldn't find match with player in it", delinquents[0])
				return nil
			} else {
				fmt.Println("Abandonment in match index", matchIndex)
			}
			punishDelinquents(delinquents)
			for _, player := range w.getPlayerConns(sv.Matches[matchIndex]) {
				player.sendJSON <- msg
			}
			sv.deleteMatch(matchIndex)
		} else {
			return fmt.Errorf("Unknown message type: %s", msg.Type)
		}
	}
	return nil
}
