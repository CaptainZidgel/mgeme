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
	playerReady chan bool
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
				c.h.removeConnection(c)
				fmt.Printf("User navigated away, Disconnection (1001)\n")
				break
			} else if websocket.IsCloseError(err, 1006) { //1006 is the code SM sends when the server shuts down uncleanly (ie via Ctrl+C)
				if c.h.hubType != "game" {
					fmt.Printf("Unexpected user disconnect (1006) didn't close connection properly")
				} else {
					fmt.Printf("Server %s didn't close properly (Websocket code 1006)", c.id)
				}
				c.h.removeConnection(c)
				break
			} else if websocket.IsCloseError(err, 1000) { //1000 is the code SM sends when the websocket closes properly in the code as the server quits or plugin is unloaded
				if c.h.hubType == "game" {
					fmt.Printf("Server %s shut down / plugin unloaded cleanly.", c.id)
				}
				c.h.removeConnection(c)
				break
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
