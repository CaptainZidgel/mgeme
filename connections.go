//https://github.com/arschles/go-in-5-minutes/blob/master/episode4/connection.go
package main

import (
	"fmt"
	"sync"
	
	"github.com/gorilla/websocket"
)

type connection struct {
	user User
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
				QueueUpdate(false, c)
				fmt.Printf("Disconnection (1001)\n")
				break
			}
			continue
		}
		herr := HandleMessage(jmsg, c.user.id, c)
		if herr != nil {
			c.sendText <- []byte(herr.Error())
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