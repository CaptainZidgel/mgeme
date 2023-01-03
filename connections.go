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
	object interface{}
}

func (c *connection) reader(wg *sync.WaitGroup, conn *websocket.Conn, webserver *webServer) {
	defer wg.Done()
	for {
		var jmsg Message
		err := conn.ReadJSON(&jmsg)
		if err != nil {
			fmt.Printf("Error reading json for %s ID %s: ", c.h.hubType, c.id)
			if websocket.IsCloseError(err, 1001) {
				if c.h.hubType == "user" {
					webserver.queueUpdate(false, c)
					fmt.Printf("User navigated away, Disconnection (1001)\n")
				} else {
					fmt.Printf("Game Server code 10001\n")
				}
				c.h.removeConnection(c)
				break
			} else if websocket.IsCloseError(err, 1006) { //1006 is the code SM sends when the server shuts down uncleanly (ie via Ctrl+C). It's also the code gorilla's websocket.Close() will call if you don't send a close message before hand.
				if c.h.hubType != "game" {
					fmt.Print("Unexpected user disconnect (1006) didn't close connection properly\n")
				} else {
					fmt.Print("Game Server didn't close properly (1006)\n")
				}
				c.h.removeConnection(c)
				break
			} else if websocket.IsCloseError(err, 1000) { //1000 is the code SM sends when the websocket closes properly in the code as the server quits or plugin is unloaded
				if c.h.hubType == "game" {
					fmt.Printf("Game Server shut down / plugin unloaded cleanly.\n")
				} else {
					fmt.Printf("User disconnect code 1000\n")
				}
				c.h.removeConnection(c)
				break
			} else if websocket.IsUnexpectedCloseError(err, 1000, 1001, 1006) { //If error isn't one of these codes
				fmt.Printf("unexpected close error: %v\n", err)
				c.h.removeConnection(c)
				break
			} else { //err is not a websocket close error
				fmt.Printf("Breaking due to %v\n", err)
				c.h.removeConnection(c)
				break
			}
			continue
		}
		herr := webserver.HandleMessage(jmsg, c.id, c)
		if herr != nil {
			fmt.Printf("Received err handling message %v: %v", jmsg, herr)
			c.sendText <- []byte(herr.Error()) //This echos errors back to the sender. Need to update this as its nonfunctional atm.
		}
	}
}

func (c *connection) writer(wg *sync.WaitGroup, conn *websocket.Conn) {
	defer wg.Done()
	for {
		select {
			case payload := <- c.sendJSON:
				err := conn.WriteJSON(payload)
				if err != nil {
					break //This only breaks out of the select, not the for.
				}
			case text := <- c.sendText:
				conn.WriteMessage(websocket.TextMessage, text)
		}
	}
}
