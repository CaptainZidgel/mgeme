//https://github.com/arschles/go-in-5-minutes/blob/master/episode4/hub.go
//one of the most goated files ive ever ripped off
package main

import (
	"log"
	"sync"
	"time"
)

type Hub struct {
	connectionsMx sync.RWMutex //so my understanding of a mutex is that it simply protects resources from being used by two different threads. If one thread tries to access a locked mutex, it is automatically blocked until the thread holding the mutex unlocks it. Sounds pretty cool and not as complicated as the name makes it seem.
	connections map[*connection]interface{} //registered connections. the guide does not say why they chose to store the conns as keys to empty values but I'm going to use my own structs as values.
	
	broadcast chan []byte //An asynchronous go channel for messages from the connections
	
	hubType string //"game" or "user"
}

func newHub(hubType string) *Hub {
	h := &Hub {
		connectionsMx: sync.RWMutex{},
		broadcast: make(chan []byte),
		connections: make(map[*connection]interface{}),
		hubType: hubType,
	}
	
	go func() {
		for {	//this loop serves back any messages one client sends up down to other clients
			msg := <- h.broadcast //when a message is put into the broadcast channel, it will be spat out here (<-) and assigned to msg (:=)
			h.connectionsMx.RLock() ///Lock for reading. This goroutine is the only thing that can read from it now.
			for c := range h.connections {
				select {	//the select statement doesn't switch on values, instead it simply waits until one of the cases becomes valid.
					case c.sendText <- msg:
						//no additional code to run. the colon is only here for syntax.
					case <-time.After(1 * time.Second):	//time.After is a channel. Once the duration of 1 * time.Second has elapsed, the time is sent to the channel. Now that there's something in the channel to receive from (<-) we know 1 second has passed and we time out the conn.
						log.Printf("Shutting down connection %v", *c)
						h.removeConnection(c)
				}
			}
			h.connectionsMx.RUnlock() //Undoes the lock
		}
	}()
	return h
}

func (h *Hub) findConnection(id string) (*connection, interface{}) {
	for conn, object := range h.connections {
		if conn.id == id {
			return conn, object
		}
	}
	return nil, nil
}

func (h *Hub) addConnection(conn *connection) {
	h.connectionsMx.Lock()	//Locks for writing. If the mutex is already locked, block until its available to lock again.
	defer h.connectionsMx.Unlock()
	h.connections[conn] = struct{}{}
	log.Printf("Added connection to %s hub", h.hubType)
	if (h.hubType == "game") {
		conn.sendJSON <- &HelloWorld{Hello: "World!"}
	}
}

func (h *Hub) removeConnection(conn *connection) {
	h.connectionsMx.Lock()
	defer h.connectionsMx.Unlock()
	if _, ok := h.connections[conn]; ok {
		delete(h.connections, conn)
		close(conn.sendText)
		close(conn.sendJSON)
		log.Printf("Removed connection from %s hub", h.hubType)
	}
}