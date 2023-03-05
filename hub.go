//https://github.com/arschles/go-in-5-minutes/blob/master/episode4/hub.go
//one of the most goated files ive ever ripped off
package main

import (
	"log"
	"sync"
	"strconv"
)

type Hub struct {
	connectionsMx sync.RWMutex                //so my understanding of a mutex is that it simply protects resources from being used by two different threads. If one thread tries to access a locked mutex, it is automatically blocked until the thread holding the mutex unlocks it. Sounds pretty cool and not as complicated as the name makes it seem.
	connections   map[string]*connection //registered connections. the guide does not say why they chose to store the conns as keys to empty values but I'm going to use my own structs as values.

	broadcast chan []byte //An asynchronous go channel for messages from the connections

	hubType string //"game" or "user"
}

func newHub(hubType string) *Hub {
	h := &Hub{
		connectionsMx: sync.RWMutex{},
		broadcast:     make(chan []byte),
		connections:   make(map[string]*connection),
		hubType:       hubType,
	}
	return h
}

func (h *Hub) getConn(id string) (*connection, bool) {
	h.connectionsMx.Lock()
	defer h.connectionsMx.Unlock()
	conn, exists := h.connections[id]
	return conn, exists
}

/*func (h *Hub) findConnection(id string) (*connection, interface{}) {
	for conn, object := range h.connections {
		if conn.id == id {
			return conn, object
		}
	}
	return nil, nil
}*/

func (h *Hub) addConnection(conn *connection) {
	h.connectionsMx.Lock() //Locks for writing. If the mutex is already locked, block until its available to lock again.
	defer h.connectionsMx.Unlock()
	if conn.id == "" { //Servers connect with no inherent ID
		nid := strconv.Itoa(len(h.connections)+1)
		conn.id = nid
		log.Println("Adding new gameserver with id", conn.id)
	}
	h.connections[conn.id] = conn
	log.Printf("Added connection to %s hub", h.hubType)
	if h.hubType == "game" {
		conn.sendJSON <- &HelloWorld{Hello: "World!"}
	}
}

func (h *Hub) removeConnection(conn *connection) {
	h.connectionsMx.Lock()
	defer h.connectionsMx.Unlock()
	if _, ok := h.connections[conn.id]; ok {
		delete(h.connections, conn.id)
		for len(conn.sendJSON) > 0 {
			<-conn.sendJSON //drain
		}
		close(conn.sendText)
		close(conn.sendJSON)
		for len(conn.playerReady) > 0 {
			<-conn.playerReady
		}
		close(conn.playerReady)
		log.Printf("Removed connection %s from %s hub", conn.id, h.hubType)
	}
}
