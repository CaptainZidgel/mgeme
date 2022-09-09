package main

import (
	"fmt"
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
)

type Message struct {
	Type string `json:"type"`
	Payload json.RawMessage `json:"payload"`
}


//MESSAGES WE WILL RECEIVE
type QueueJoinLeave struct {
	//this is probably going to be crazy bad code to read but using bool to enforce binary options instead of remembering integer values seems like a good idea
	Joining bool `json:"joining"` //true for join, false for leave
}


//MESSAGES WE WILL SEND
type AckQueue struct {
	Type string `json:"type"`
	Queue PlayerEntries `json:"queue"`
	NewQueueStatus bool `json:"newstatus"`
}

func HandleMessage(msg Message, steamid string, ws *websocket.Conn) error { //get steamid from server (which gets it from browser session after auth), don't trust users to send it in json. pass the websocket connection so we can send stuff back if needed, or pass it to further functions
	fmt.Println(msg.Type)
	if msg.Type == "QueueUpdate" { // {type: "QueueUpdate", payload: {joining: true/false}}
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
		QueueUpdate(res.Joining, steamid, ws)
	} else if msg.Type == "i dont know actually let me think about that" {
		//res := MessageType
		//JSON.Unmarshall(msg.Payload, &res)
	} else {
		return errors.New(fmt.Sprintf("Unknown message type: %s", msg.Type))
	}
	return nil
}
