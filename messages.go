package main

import (
	"fmt"
	"encoding/json"
	"errors"
	"log"
)

type Message struct { //This is a struct-ambiguous way to receive json messages. The type is used to cast the payload into the right struct.
	Type string `json:"type"`
	Payload json.RawMessage `json:"payload"`
}


//MESSAGES WE WILL RECEIVE
type QueueJoinLeave struct { //Comes from users: Join or leave the queue
	//this is probably going to be crazy bad code to read but using bool to enforce binary options instead of remembering integer values seems like a good idea
	Joining bool `json:"joining"` //true for join, false for leave
}

type ServerHelloWorld struct { //Comes from gameserver to initialize connections.
	ApiKey string `json:"apiKey"`
	ServerNum string `json:"serverNum"`
}

type MatchResults struct {
	Winner string `json:"winner"`
	Loser string `json:"loser"`
	FinishMode string `json:"finishMode"`
}


//MESSAGES WE WILL SEND
type AckQueue struct {
	Type string `json:"type"`
	Queue PlayerEntries `json:"queue"`
	IsInQueue bool `json:"isInQueue"`
}

type HelloWorld struct {
	Hello string `json:"Hello"`
}

func HandleMessage(msg Message, steamid string, conn *connection) error { //get steamid from server (which gets it from browser session after auth), don't trust users to send it in json. pass the websocket connection so we can send stuff back if needed, or pass it to further functions
	fmt.Println(msg.Type)
	if msg.Type == "QueueUpdate" { // {type: "QueueUpdate", payload: {joining: true/false}} Comes from users
		var res QueueJoinLeave
		err := json.Unmarshal(msg.Payload, &res) //write to that empty instance
		if err != nil {
			fmt.Println("Error unmarshaling", err.Error(), "|", string(msg.Payload))
			if err.Error() == "unexpected end of JSON input" {
				return errors.New("Malformed JSON input") //not necessarily unexpected end. could be bad format, ie payload named msg or something
			} else {
				return err
			}
		}
		QueueUpdate(res.Joining, conn)	//Update the server's master queue
	} else if msg.Type == "TestMatch" {
		m := DummyMatch(conn.h)
		if m == nil { log.Fatalln("nil match") }
		SendMatchToServer(m)
	} else if msg.Type == "hworld" { //Comes from gameservers
		var res ServerHelloWorld
		err := json.Unmarshal(msg.Payload, &res)
		if err != nil {
			fmt.Println("Error unmarshaling", err.Error(), "|", string(msg.Payload))
		}
		fmt.Println("Game Server %s connected", res.ServerNum)
		conn.id = res.ServerNum
	} else if msg.Type == "MatchResults" {
		var res MatchResults
		err := json.Unmarshal(msg.Payload, &res)
		if err != nil {
			fmt.Println("Error unmarshaling MatchResults", err.Error(), "|", string(msg.Payload))
		}
		//do something
		winner := res.Winner
		loser := res.Loser
		fmt.Printf("Received MatchResults from Server %s : Winner %s , Loser %s ^ Finish Mode %s/n", conn.id, winner, loser, res.FinishMode)
	} else if msg.Type == "i dont know actually let me think about that" {
		//res := MessageType
		//JSON.Unmarshall(msg.Payload, &res)
	} else {
		return errors.New(fmt.Sprintf("Unknown message type: %s", msg.Type))
	}
	return nil
}
