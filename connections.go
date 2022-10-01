//https://github.com/arschles/go-in-5-minutes/blob/master/episode4/connection.go
package main

import (
	"fmt"
	"sync"
	
	"github.com/gorilla/websocket"
)

type connection struct {
	id string //steamid for users or a To Be Decided for gameservers
	sendText chan []byte
	sendJSON chan interface{}
	h *Hub
}

func (c *connection) reader(wg *sync.WaitGroup, conn *websocket.Conn) {
	defer wg.Done()
	for {
		var jmsg Message
		err := conn.ReadJSON(&jmsg)
		if err != nil {
			fmt.Printf("Error reading json: %s\n", err.Error())
			if websocket.IsCloseError(err, 1001) {
				if c.h.hubType == "user" {QueueUpdate(false, c)}
				fmt.Printf("Disconnection (1001)\n")
				break
			} else if websocket.IsCloseError(err, 1006) {
				if c.h.hubType != "game" {
					fmt.Printf("Unexpected user disconnect (1006) didn't close connection properly")
				}
			}
			continue
		}
		herr := HandleMessage(jmsg, c.id, c)
		if herr != nil {
			c.sendText <- []byte(herr.Error()) //This echos errors back to the sender. Need to update this as its nonfunctional atm.
		}
		//c.h.broadcast <- message
	}
}

func (c *connection) writer(wg *sync.WaitGroup, conn *websocket.Conn) {
	defer wg.Done()
	for {
		select {
			case payload := <- c.sendJSON:
				err := conn.WriteJSON(payload)
				if err != nil {
					break
				}
			case text := <- c.sendText:
				conn.WriteMessage(websocket.TextMessage, text)
		}
	}
}