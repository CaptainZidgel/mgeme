package main

import (
	"net/http"
	"github.com/gin-gonic/gin"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-contrib/multitemplate"
	"log"
	"fmt"
	"path/filepath"
	"github.com/solovev/steam_go"
	"github.com/gorilla/websocket"
	"sync"
	"time"
	"database/sql"
	"github.com/go-sql-driver/mysql"
	"github.com/leighmacdonald/steamid/v2/steamid"
	"github.com/leighmacdonald/steamweb"
	"io/ioutil"
	"encoding/json"
	"net"
	"strings"
	"errors"
	"flag"
)

type User struct {
	id string
	elo int
	summary steamweb.PlayerSummary
	Nickname string
	Avatar struct {
		Small string
		Medium string
		Full string
	}
}

//a Json object for loading your sql configuration. The fields are just named user, pass, addr, dbName
type sqlConfig struct {
	User string	//fields need to be exported to be JSON compatible
	Pass string
	Addr string
	DbName string
}

func singleSummary(id string) (steamweb.PlayerSummary, error) {
	sid, err := steamid.StringToSID64(id)
	if err != nil {
		return steamweb.PlayerSummary{}, err
	}
	sums, err := steamweb.PlayerSummaries([]steamid.SID64{sid})
	if err != nil {
		return steamweb.PlayerSummary{}, err
	}
	return sums[0], err
}

func GetUser() gin.HandlerFunc { //middleware to set contextual variable from session
	return func(c *gin.Context) {
		var user User
		session := sessions.Default(c)
		if id := session.Get("steamid"); id != nil {
			user.id = id.(string)
			user.elo = GetElo(user.id)
			log.Println("Authorizing user with steamid", user.id)
			summary, err := singleSummary(user.id)
			if err != nil {
				log.Printf("Error getting user summary for id %d : %v\n", user.id, err)
			} else {
				//log.Printf("Got summary %v\n", summary)
				user.summary = summary
				user.Nickname = summary.PersonaName
				user.Avatar.Small = summary.Avatar
				user.Avatar.Medium = summary.AvatarMedium
				user.Avatar.Full = summary.AvatarFull
			}
			c.Set("User", user)
		} else {
			log.Println("session steamid was nil, not authorizing")
		}
	}
} //this is fairly superfluous at this point but if i build out the User type I will want to add stuff here probably

var SelectElo *sql.Stmt

type webServer struct{
	gameServerHub *Hub
	playerHub *Hub
	
	gameQueue PlayerEntries
	rupTime int
}

func newWebServer() *webServer {
	web := webServer{}
	web.gameServerHub = newHub("game")
	web.playerHub = newHub("user")
	web.gameQueue = make(PlayerEntries)
	web.rupTime = 5
		
	return &web
}

func main() {
	wsHostPtr := flag.String("addr", getOutboundIp(), "Address to listen on (Relayed to clients to know where to send messages to, ie 'localhost' on windows)")
	portPtr := flag.String("port", "8080", "Port to listen on")
	flag.Parse()

	mgeme := newWebServer()
	
	rout := gin.Default()
	rout.HTMLRender = loadTemplates("./views")

	store := cookie.NewStore([]byte("SECRET"))
	rout.Use(sessions.Sessions("sessions", store))
	rout.Use(GetUser())
	
	rout.GET("/", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	rout.GET("/login", func(c *gin.Context) {
		loginSteam(c)
		steamId := sessions.Default(c).Get("steamid")
		if steamId == "" {
			log.Fatal("UHHHH")
		}
		c.String(200, steamId.(string))
	})

	rout.GET("/logout", func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set("steamid", nil)
		session.Save()

		c.Redirect(302, "/")
	})

	rout.GET("/checkme", func(c *gin.Context) {
		usr, lgdin := c.Get("User") //returns interface{}, and if-key-exists
		if lgdin {
			var user User = usr.(User) //explicitly cast interface as User
			c.String(200, fmt.Sprintf("Logged in as %s", user.id))
		} else {
			c.String(401, "Not logged in")
		}
	})

	rout.GET("/websock", func(c *gin.Context) {	//The endpoint for user connections (ie users adding up to play, but not for servers connecting to transmit messages)
		mgeme.WsServer(c, "user")
	})
	
	rout.GET("/tf2serverep", func(c *gin.Context) { //endpoint for game servers
		mgeme.WsServer(c, "game")
	})

	rout.GET("/queue", func(c *gin.Context) {
		usr, loggedin := c.Get("User")
		var id string
		if loggedin {
			id = usr.(User).id
		}
		c.HTML(http.StatusOK, "queue.html", gin.H{"wsHost": *wsHostPtr, "wsPort": *portPtr, "loggedIn": loggedin, "steamid": id, "user": usr}) //clean this up later?
	})
	
	content, err := ioutil.ReadFile("./DbCfg.json")
	if err != nil { log.Fatal("Error opening database config: ", err) }
	var conf sqlConfig
	err = json.Unmarshal(content, &conf)
	if err != nil { log.Fatal("Error unmarshalling: ", err) }
	
	dbCfg := mysql.NewConfig()	//create a new config object with default values
	dbCfg.User = conf.User		//insert my values into the config object... (username/password for sql user, etc)
	dbCfg.Passwd = conf.Pass
	dbCfg.Net = "tcp"
	dbCfg.Addr = conf.Addr
	dbCfg.DBName = conf.DbName
	db, err := sql.Open("mysql", dbCfg.FormatDSN())	//opens a sql connection, the FormatDSN() function turns out config object into a driver string
	if err != nil { log.Fatal("Error connecting to sql: ", err) }
	defer db.Close()
	
	SelectElo, err = db.Prepare("SELECT rating FROM mgemod_stats WHERE steamid = ?")
	if err != nil { log.Fatal(err) }
	defer SelectElo.Close()

	//AddPlayersTest(118, 14, userHub)
	mgeme.sendQueueToClients()
	rout.Run(*wsHostPtr + ":" + *portPtr)
}

//https://stackoverflow.com/questions/23558425/how-do-i-get-the-local-ip-address-in-go
func getOutboundIp() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	
	localAddr := conn.LocalAddr().(*net.UDPAddr).IP.String()
	return localAddr
}

func loginSteam(c *gin.Context) {
	var w http.ResponseWriter = c.Writer
	var r *http.Request = c.Request
	opId := steam_go.NewOpenId(r) //creates an openid object used by the steam_go module but doesn't seem to actually authenticate anything yet (it takes r so it can read URL keyvalues, where openid does its comms)
	switch opId.Mode() {
		case "": //openid has not done anything yet, so redirect to steam login and begin the process
			http.Redirect(w, r, opId.AuthUrl(), 301)
			log.Println("OpenID 301 Redirecting")
		case "cancel": //Cancel authentication, treat user as unauthenticated
			w.Write([]byte("authorization cancelled"))
			log.Println("OpenID auth cancelled")
		default:
			steamId, err := opId.ValidateAndGetId() //redirects your user to steam to authenticate, returns their id or an error
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}

			session := sessions.Default(c)
			session.Set("steamid", steamId)
			session.Save()

			//parse original request (r) to see if there was a specific redirect param
			redir := r.FormValue("redirect")
			if redir == "queue" {
				http.Redirect(w, r, "/queue", 301)
			}
	}
}

func loadTemplates(dir string) multitemplate.Renderer {
	r := multitemplate.NewRenderer()

	layouts, err := filepath.Glob(dir + "/layouts/*")
	if err != nil {
		log.Fatal(err)
	}

	includes, err := filepath.Glob(dir + "/templates/*")
	if err != nil {
		log.Fatal(err)
	}

	for _, include := range includes {
		layoutCopy := make([]string, len(layouts))
		copy(layoutCopy, layouts)
		files := append(layoutCopy, include)
		r.AddFromFiles(filepath.Base(include), files...)
	}

	return r
}

func GetElo(steam64 string) (int) {
	if strings.Contains(steam64, "FakePlayer") || !strings.HasPrefix(steam64, "7") {
		return 1600
	}
	s64 := steamid.ParseString(steam64)[0]	//ParseString returns an array. I like this over SID64FromString because no error testing.
	steam2 := steamid.SID64ToSID(s64) //7777777777777 -> Steam_0:1:1111111
	var rating int
	err := SelectElo.QueryRow(steam2).Scan(&rating)
	//if err is row doesn't exist for this steam2, ignore and return 1600 (default elo in mgemod)
	if err != nil { //To do: Double check this error is just "sql: no rows in result set"
		rating = 1600
	}
	return rating
}

type PlayerAdded struct {
	Connection *connection
	User User
	Steamid string
	Elo int
	WaitingSince time.Time
	//this type seems bare and the map seems unnecessary,
	//but if i build this out we will need more than 1 value so a key/value map doesnt make sense
	//example of further properties: maps desired, server location, classes desired
}

type PlayerEntries map[string]PlayerAdded //this is a maptype of PlayerAdded structs. It maps steamids to player data.

func (w *webServer) WsServer(c *gin.Context, hubtype string) (error) {
	if !(hubtype == "user" || hubtype == "game") {
		return fmt.Errorf("Incorrect hubtype of %v, use 'user' or 'game'", hubtype)
	}
	var hub *Hub
	if hubtype == "user" {
		hub = w.playerHub
	} else {
		hub = w.gameServerHub
	}

	usr, lgdin := c.Get("User") //lgdin (loggedin) represents if the key User exists in context
	if lgdin || hubtype == "game" { //We don't bother upgrading the connection for an unlogged in user (but we will for game servers!)
		fmt.Printf("Accepting websocket connection type: %s\n", hubtype)

		write := c.Writer
		r := c.Request
		//"Upgrade" the HTTP connection to a WebSocket connection, and use default buffer sizes
		var upgrader = websocket.Upgrader{
			ReadBufferSize: 0,
			WriteBufferSize: 0,
			CheckOrigin: func(r *http.Request) bool {return true},
		}
		wsConn, err := upgrader.Upgrade(write, r, nil)
		if err != nil {
			log.Println("Error at websocket initialization:", err)
			return err
		}
		
		var id string
		if lgdin {
			id = usr.(User).id //cast the context var to a User type
		}

		clientConn := &connection{
			sendText: make(chan []byte, 256), 
			sendJSON: make(chan interface{}, 1024),
			playerReady: make(chan bool, 8),
			h: hub, 
			id: id,
		} //create our ws connection object
		hub.addConnection(clientConn) //Add our connection to the hub
		if hubtype == "user" {
			clientConn.sendJSON <- NewAckQueueMsg(w.gameQueue, id)
			hub.connections[clientConn] = usr.(User)
			w.resolveConnectingUser(clientConn)
		}
		defer hub.removeConnection(clientConn)
		var wg sync.WaitGroup
		wg.Add(2)
		go clientConn.writer(&wg, wsConn)
		go clientConn.reader(&wg, wsConn, w)
		wg.Wait()
		wsConn.Close()
	} else { //Neither a logged in user, not a game server.
		return fmt.Errorf("Rejecting websocket connection for unloggedin user")
	}
	return nil
}

//If the user is in a match, update this information after they open the queue page
//When a user closes or refreshes a tab, that closes the websocket (code 1001, navigated away). I know yet how much info I want to preserve across sessions
func (w *webServer) resolveConnectingUser(conn *connection) {
	log.Println("Resolving reconnected user")
	for _, server := range w.gameServerHub.connections {
		server, ok := server.(*gameServer)
		if !ok {
			log.Printf("Error casting serverhub connection to server type %v\n", server)
		} else {
			matchIndex := server.findMatchByPlayer(conn.id)
			if matchIndex > -1 {
				log.Printf("Found user in match on server %d, sending to user\n", matchIndex)
				conn.sendJSON <- server.Matches[matchIndex]
				break
			} else {
				log.Println("Could not find user in match")
			}
		}
	}
}

func (w *webServer) queueUpdate(joining bool, conn *connection) { //The individual act of joining/leaving the queue. Should be followed by SendQueueToClients
	steamid := conn.id
	if joining { //add to queue
			user := w.playerHub.connections[conn].(User)
			w.gameQueue[steamid] = PlayerAdded{Connection: conn, User: user, Steamid: steamid, Elo: GetElo(conn.id), WaitingSince: time.Now()}//steamid//lol
			log.Println("Adding to queue")
	} else { //remove from queue
			delete(w.gameQueue, steamid) //remove steamid from gamequeue
			log.Println("Removing from queue")
	}

	w.sendQueueToClients()
}

/*
func AddPlayersTest(seed int64, maxplayers int, hub *Hub) {
	rand.Seed(seed)
	i := rand.Intn(maxplayers+1) + 3
	for n := 0; n < i; n++ {
		GameQueue[fmt.Sprintf("%d", n)] = PlayerAdded{
			Connection: &connection{id: "FakePlayer", sendText: nil, sendJSON: nil, h: hub}, 
			Steamid: fmt.Sprintf("%d", n), 
			Elo: rand.Intn(2000) + 1000, 
			WaitingSince: time.Now().Add(time.Second * -time.Duration(rand.Intn(120) + 1)),	//subtracts a random amount of seconds from the current time
		}
	}
}*/

func (w *webServer) sendQueueToClients() {
	for c := range w.playerHub.connections {
		c.sendJSON <- NewAckQueueMsg(w.gameQueue, c.id) //send a personalized ack out to each client, including confirmation that they're still inqueue
	}
}

type gameServer struct { //The stuff the webserver will want to know, doesn't necessarily have info like the IP as that isn't necessary.
	Matches []*Match
	Info matchServerInfo
}

//Find what match contains a certain player (since players can only be in one match at a time, it should be sufficient to only pass one player)
//Return the index and not the match object since I'll probably be more interested in the match's position in the slice.
func (s *gameServer) findMatchByPlayer(id string) (int) {
	for i, m := range s.Matches {
		if m.P1id == id || m.P2id == id {
			return i
		}
	}
	return -1
}

func (s *gameServer) deleteMatch(ind int) error {
	if ind >= len(s.Matches) {
		return fmt.Errorf("Index out of bounds")
	}
	if len(s.Matches) < 1 {
		return fmt.Errorf("Can't delete from empty match slice")
	} else if len(s.Matches) > 1 {
		if ind < len(s.Matches)-1 {
			s.Matches = append(s.Matches[:ind], s.Matches[ind+1:]...)
		} else if ind == len(s.Matches)-1 { //ind = len(s.Matches) Deleting last item off the slice
			s.Matches = s.Matches[:ind]
		} else {
			return fmt.Errorf("Huh? Logic should be exhaustive here. Bad bounds logic.")
		}
	} else if len(s.Matches) == 1 {
		s.Matches = make([]*Match, 0)
	}
	//There are many ways in Go to remove something from a slice, but here I use the re-slicing method to avoid leaving nil spots in my slice
	//This is relatively expensive but I think it should be better than having to resize my slice to 1,000 objects, mostly nil, if I have 1,000 matches over 15 days of time.
	log.Printf("Deleting match idx %d from server %s\n", ind, s.Info.Id)
	return nil
}

type matchServerInfo struct { //Just the stuff users need to know
	Id	string	`json:"id"`
	Host string `json:"ip"`
	Port string	`json:"port"`
	Stv string	`json:"stv"`
}

const (
	matchInit = 0
	matchRupSignal = 1
	matchWaitingForPlayers = 2
	matchPlaying = 3
	matchOver = 4
)

//A match object should encapsulate an entire match from inception on the webserver to being sent to the game servers and the clients.
//The webserver will hold a slice of these and each server will hold their own copies as well
type Match struct {
	Type string `json:"type"`
	Arena int `json:"arenaId"`
	P1id string `json:"p1Id"` //players1 and 2 ids for serialization
	P2id string `json:"p2Id"`
	Configuration map[string]string `json:"matchCfg"` //reserved: configuration may be something like "scout vs scout" or "demo vs demo" perhaps modeled as "cfg": "svs" or p1class : p2class
	ServerId string `json:"gameServer"`
	Status int
	ConnectDeadline int64 `json:"deadline"` //Not set until match initialization. Though this deadline is not used by the server, it will be useful to the client.
	
	timer *time.Timer //no json tag!! Don't serialize it!!
	players []PlayerAdded //unserialized helper thing :)
}

//Both players ready, send match to server and players.
func (w *webServer) initializeMatch(m *Match) {
	c, obj := w.gameServerHub.findConnection(m.ServerId)	//find connection for this id
	if c == nil {
		alertPlayers(200, "Can't connect to game servers...", w.playerHub)
		log.Println("No server to send match to. Cancelling match")
		//Cancel matchmaker until servers come back online? Probably using a channel, mmSentinel <- 1
		w.clearAllMatches()
		return
	}
	m.Status = matchWaitingForPlayers
	m.Type = "MatchDetails"
	m.ConnectDeadline = time.Now().Add(time.Second * 180).Unix()
	
	sv := obj.(*gameServer) //get server object
	sv.Matches = append(sv.Matches, m) //Adds the match to the webserver's records for this server
	
	c.sendJSON <- m //Sends match details to the gameserver (sourcemod)
	for _, player := range m.players {
			player.Connection.sendJSON <- m
	}
}

func (w *webServer) fillPlayerSlice(num int, fallback bool) ([]PlayerAdded, error) {
	fill := make([]PlayerAdded, num) //Create empty slice of PlayerAdded elements, length num. instead of dynamically resizing with fill = append(fill, x) we're just going to assign x to fill[i]
	i := 0
	for _, val := range w.gameQueue {
		fill[i] = val
		i = i + 1
		if i == num {
			return fill, nil
		}
	}
	if fallback { //still extra space? allowed to use fake players? then do so
		diff := num - i
		for diff > 0 {
			log.Println("Adding a fake player to fill. diff before subtraction = ", diff)
			fill[i] = PlayerAdded{Steamid: "FakePlayer"}
			diff = diff - 1
			i = i + 1 //continue iterating so we can fill our slice properly
		}
	} else {
		return nil, errors.New("Couldn't find enough players")
	}
	return fill, nil
}

/*func findRealPlayerInQueue(hub *Hub) PlayerAdded { //Finds a real player in the queue, if one exists. This is NOT error safe if one doesn't exist. Doesn't know if it's already returned you that player in another call. Use fillPlayerSlice
	for _, player := range GameQueue {
		if !strings.Contains(player.Connection.id, "FakePlayer") {
			return player
		}
	}
	return PlayerAdded{}
}*/

/*The functional process for starting a match should be:
	Make a match via algorithm (in tesitng, DummyMatch)		(hub -> Match)
	Send the ready up signal to the players, temporarily remove them from queue (Match, hub -> void)
		Both players ready up: Initialize the match to the game server
		Player(s) fail to ready up: Restore any player who readied to the queue (using same PlayerAdded object as before, preserving WaitingSince)
									Leave unready players out of queue
*/

func (w *webServer) dummyMatch() (*Match, error) { //change string to SteamID2 type?
	players, err := w.fillPlayerSlice(2, false)
	if err != nil {
		return nil, err
	}
	log.Println("Received fill slice: ", players)
	
	//remove players from queue, update queue for all players
	w.removePlayersFromQueue(players)
	return createMatchObject(players), nil
}

func createMatchObject(players []PlayerAdded) *Match {
	log.Println("Matching together", players[0].Steamid, players[1].Steamid)
	server := "1" //TODO: SelectServer() function if I scale out to multiple servers. I'm keeping server as a string for now in case I want to identify servers in another way.
	arena := 1 // Random. TODO: Selection.
	return &Match{
		ServerId: server, 
		Arena: arena, 
		Configuration: make(map[string]string), 
		P1id: players[0].Steamid,
		P2id: players[1].Steamid,
		timer: nil,
		Status: matchInit,
		players: players,
	}
}

func (w *webServer) sendReadyUpPrompt(m *Match) {
	for _, player := range m.players {
		player := player.Connection
		if player == nil { //players is a slice of pointers, which could have been nilled out by this point
			log.Println("Couldn't send rup signal to player (no connection object)", player)
		} else {
			player.sendJSON <- NewRupSignalMsg(true, false, w.rupTime)
		}
	}
	m.timer = time.NewTimer(time.Second * time.Duration(w.rupTime))
	m.Status = matchRupSignal
	
	p1 := m.players[0].Connection
	p2 := m.players[1].Connection
	p1ready := false
	p2ready := false
	for !(p1ready && p2ready) { //while not both players readied
		select {
			case <- m.timer.C: //Block until timer reaches maturity
				log.Println("Timer expired in rup")
				w.expireRup(m, p1ready, p2ready)
				return //NO MORE FUNCTION!!! >:(
			case _, ok := <- p1.playerReady: //r1 receives a true only when the client sends a message.
				if !ok {
					log.Println("Closed ready channel 1")
				}
				p1ready = true
				log.Println("player 1 has readied")
			case _, ok := <- p2.playerReady:
				if !ok {
					log.Println("Closed ready channel 2")
				}
				p2ready = true
				log.Println("player 2 has readied")
		}
	}
	log.Println("both players have readied")
	w.expireRup(m, false, false) //We're "kicking" both from the queue but in this case we're going to follow it up with making a match :-) Hehehe
	w.initializeMatch(m)
}

//ExpireRup is used to end the rup timer and manage the queue
//Pass a slice of 2 bools that match to ready signals for players in the slice m.players
func (w *webServer) expireRup(m *Match, readies ...bool) {
	log.Println("Expiring Rup, player 1 and 2 back to queue?:", readies)
	m.timer.Stop() //Stop the timer, if it hasn't executed yet
	m.timer = nil
	for i, player := range m.players {
		player := player.Connection
		if readies[i] == true { //player was ready when timer ended, add them back to queue
			w.queueUpdate(true, player)
		} else {	//player didn't ready, remove them from idling in queue
			w.queueUpdate(false, player)
		}
		player.sendJSON <- NewRupSignalMsg(false, false, w.rupTime)
	}
}

func (w *webServer) clearAllMatches() {
	for _, server := range w.gameServerHub.connections {
		server := server.(*gameServer)
		for _, match := range server.Matches {
			//Add players back to queue: Not calling expireRup as a shortcut for this because that would send a rupsignal and start a client timer.
			for _, p := range match.players {
				w.gameQueue[p.Connection.id] = p //Not using QueueUpdate, as to avoid sending a queue message for every player. Waiting until after.
			}
		}
		server.Matches = make([]*Match, 0)
	}
	w.sendQueueToClients()
}

func (w *webServer) removePlayersFromQueue(players []PlayerAdded) {
	for _, p := range players {
		delete(w.gameQueue, p.Steamid)
	}
	w.sendQueueToClients()
}

func NewFakeConnection() *connection {
	return &connection{
		playerReady: make(chan bool, 9999),
		sendJSON: make(chan interface{}, 9999),
	}
}

/*
func DummyMatchAll(hub *Hub) { //Just put 2 players together with no rhyme or reason.
	keys := make([]string, 0)
	for k, _ := range GameQueue {
		keys = append(keys, k)
	}
	if len(keys) % 2 != 0 { //We only match an even amount of players
		keys = keys[0:len(keys)-1]
	}
	for i := 1; i < len(keys) - 1; i += 2{
		DummyMatch(keys[i], keys[i+1], hub)
	}
}
*/
