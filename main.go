package main

import (
	"net/http"
	"github.com/gin-gonic/gin"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-contrib/multitemplate"
	"log"
	"os"
	"fmt"
	"path/filepath"
	"github.com/solovev/steam_go"
	"github.com/gorilla/websocket"
	"sync"
	"time"
	"math/rand"
	"database/sql"
	"github.com/go-sql-driver/mysql"
	"github.com/leighmacdonald/steamid/v2/steamid"
	"io/ioutil"
	"encoding/json"
	"net"
	"strings"
	"errors"
)

var rupTime int

type User struct {
	id string
	elo int
}

//a Json object for loading your sql configuration. The fields are just named user, pass, addr, dbName
type sqlConfig struct {
	User string	//fields need to be exported to be JSON compatible
	Pass string
	Addr string
	DbName string
}

func GetUser() gin.HandlerFunc { //middleware to set contextual variable from session
	return func(c *gin.Context) {
		var user User
		session := sessions.Default(c)
		if id := session.Get("steamid"); id != nil {
			user.id = id.(string)
			user.elo = GetElo(user.id)
		}
		if user.id != "" {
			c.Set("User", user)
			log.Println("Authing user")
		} else {
			log.Println("Not authing")
		}
	}
} //this is fairly superfluous at this point but if i build out the User type I will want to add stuff here probably

var SelectElo *sql.Stmt
var gameHub *Hub

func main() {
	rupTime = 5

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

	userHub := newHub("user")
	rout.GET("/websock", func(c *gin.Context) {	//The endpoint for user connections (ie users adding up to play, but not for servers connecting to transmit messages)
		c.Set("Hub", userHub) //all websocket connections should have the same hub (server)
		WsServer(c)
	})
	
	gameHub = newHub("game")
	rout.GET("/tf2serverep", func(c *gin.Context) {
		c.Set("Hub", gameHub)
		WsServer(c)
	})
	
	var wsHost string
	if len(os.Args) > 1 {
		wsHost = os.Args[1]
	} else {
		wsHost = getOutboundIp()
	}

	rout.GET("/queue", func(c *gin.Context) {
		_, loggedin := c.Get("User")
		c.HTML(http.StatusOK, "queue.html", gin.H{"wsHost": wsHost, "wsPort": 8080, "loggedIn": loggedin})
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

	AddPlayersTest(118, 14, userHub)
	SendQueueToClients(userHub)
	rout.Run(":8080") //run main router on 0.0.0.0:8080
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
	if strings.Contains(steam64, "FakePlayer") {
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
	Steamid string
	Elo int
	WaitingSince time.Time
	//this type seems bare and the map seems unnecessary,
	//but if i build this out we will need more than 1 value so a key/value map doesnt make sense
	//example of further properties: maps desired, server location, classes desired
}

type PlayerEntries map[string]PlayerAdded //this is a maptype of PlayerAdded structs. It maps steamids to player data.
var GameQueue = make(PlayerEntries) //this is an instance of the maptype of PlayerAdded structs
var Prematches []*prematch //Matches that are waiting for players to ready up (we need to keep these in memory so we can get player info back later)

func WsServer(c *gin.Context) {
	h, _ := c.Get("Hub")
	hub := h.(*Hub) //cast the context to Hub type. This hub may either be a user hub (groups the connections of users to the webserver) or a gameserver hub (groups the connections of game servers to the webserver)

	usr, lgdin := c.Get("User") //lgdin (loggedin) represents if the key User exists in context
	hubtype := hub.hubType
	if lgdin || hubtype == "game" { //We don't bother upgrading the connection for an unlogged in user (but we will for game servers!)
		fmt.Printf("Accepting websocket connection type: %s\n", hubtype)

		w := c.Writer
		r := c.Request
		//"Upgrade" the HTTP connection to a WebSocket connection, and use default buffer sizes
		var upgrader = websocket.Upgrader{
			ReadBufferSize: 0,
			WriteBufferSize: 0,
			CheckOrigin: func(r *http.Request) bool {return true},
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println(err)
			return
		}
		
		var user User
		if lgdin {
			user = usr.(User) //cast the context var to a User type
		}

		c := &connection{
			sendText: make(chan []byte, 256), 
			sendJSON: make(chan interface{}, 1024),
			playerReady: make(chan bool, 8),
			h: hub, 
			id: user.id,
		} //create our ws connection object
		hub.addConnection(c) //Add our connection to the hub
		defer hub.removeConnection(c) //Remove
		var wg sync.WaitGroup
		wg.Add(2)
		go c.writer(&wg, conn)
		go c.reader(&wg, conn)
		wg.Wait()
		conn.Close()
	} else {
		fmt.Printf("Rejecting websocket connection: %s, %s\n", lgdin, hubtype)
	}
}

func QueueUpdate(joining bool, conn *connection) { //The individual act of joining/leaving the queue. Should be followed by SendQueueToClients
	steamid := conn.id
	if joining { //add to queue
			GameQueue[steamid] = PlayerAdded{Connection: conn, Steamid: steamid, Elo: GetElo(conn.id), WaitingSince: time.Now()}//steamid//lol
	} else { //remove from queue
			delete(GameQueue, steamid) //remove steamid from gamequeue
	}

	SendQueueToClients(conn.h)
}

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
}

func SendQueueToClients(hub *Hub) {
	for c := range hub.connections {
		_, ok := GameQueue[c.id] //check if key exists in map
		c.sendJSON <- NewAckQueueMsg(GameQueue, ok) //send a personalized ack out to each client, including confirmation that they're still inqueue
	}
}

//A sample match object made by the Go server, including full-fat user PlayerAdded objects that can be used to repopulate the queue if the match is cancelled before it begins.
type prematch struct {
	//These elements are copied directly into Match
	Server string
	Arena int
	Configuration map[string]string
	//This is unpacked into just the IDs, the only relevant part to the game server.
	Players []PlayerAdded
	timer *time.Timer //Used for Ready Up timers.
	hub *Hub //I still need to find a good way to get hub to all funcs, look I'll fix it later OK i'm working on the fun stuff right now (<- he is lying to you)
}

//Finalized object to send to game servers (includes type param for JSON)
type Match struct {
	Type string `json:"type"` //This sucks. I have to have type here so I don't dupe my code into the messages section but I don't like having my object here be the same as my json message object.
	Server string `json:"serverId"`
	Arena int `json:"arenaId"`
	P1id string `json:"p1Id"` //players1 and 2 ids
	P2id string `json:"p2Id"`
	Configuration map[string]string `json:"matchCfg"` //reserved: configuration may be something like "scout vs scout" or "demo vs demo" perhaps modeled as "cfg": "svs" or p1class : p2class
}

func SendMatchToServer(match Match) {
	serverid := match.Server
	c := gameHub.findConnection(serverid)
	if c == nil {
		log.Fatalf("No server to send match to")
	}
	c.sendJSON <- match
}

func fillPlayerSlice(num int, fallback bool) ([]PlayerAdded, error) {
	fill := make([]PlayerAdded, num) //Create empty slice of PlayerAdded elements, length num. instead of dynamically resizing with fill = append(fill, x) we're just going to assign x to fill[i]
	i := 0
	for key := range GameQueue {
		if GameQueue[key].Connection.id == "FakePlayer" {continue} else {log.Println("Adding to fill: ", key)} //only use real players
		fill[i] = GameQueue[key]
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

func findRealPlayerInQueue(hub *Hub) PlayerAdded { //Finds a real player in the queue, if one exists. This is NOT error safe if one doesn't exist. Doesn't know if it's already returned you that player in another call. Use fillPlayerSlice
	for _, player := range GameQueue {
		if !strings.Contains(player.Connection.id, "FakePlayer") {
			return player
		}
	}
	return PlayerAdded{}
}

/*The functional process for starting a match should be:
	Make a match via algorithm (in tesitng, DummyMatch)		(hub -> Match)
	Send the ready up signal to the players, temporarily remove them from queue (Match, hub -> void)
		Both players ready up: Initialize the match to the game server
		Player(s) fail to ready up: Restore any player who readied to the queue (using same PlayerAdded object as before, preserving WaitingSince)
									Leave unready players out of queue
*/

func DummyMatch(hub *Hub) (*prematch, error) { //change string to SteamID2 type? hub should always be userHub
	players, err := fillPlayerSlice(2, true)
	if err != nil {
		return nil, err
	}
	log.Println("Received fill slice: ", players)
	
	//remove players from queue, update queue for all players
	RemovePlayersFromQueue(players, hub)
	return CreatePreMatchObject(players, hub), nil
}

func CreatePreMatchObject(players []PlayerAdded, hub *Hub) *prematch {
	log.Println("Matching together", players[0].Steamid, players[1].Steamid)
	server := "1" //TODO: SelectServer() function if I scale out to multiple servers. I'm keeping server as a string for now in case I want to identify servers in another way.
	arena := 1 // Random. TODO: Selection.
	return &prematch{Server: server, Arena: arena, Configuration: make(map[string]string), Players: players, timer: nil, hub: hub}
}

func (m *prematch) SendReadyUpPrompt() {
	for _, player := range m.Players {
		c := player.Connection
		if c == nil {
			if player.Steamid == "FakePlayer" {
				continue
			} else {
				log.Println("Couldn't send rup signal to player (no connection object)", player)
			}
		} else {
			c.sendJSON <- NewRupSignalMsg(true, false)
		}
	}
	m.timer = time.NewTimer(time.Second * time.Duration(rupTime))
	Prematches = append(Prematches, m)
	
	p1 := m.Players[0].Connection
	p2 := m.Players[1].Connection
	if (p2 == nil) { //This block is for testing with fake players
		if (m.Players[1].Steamid == "FakePlayer") {
			p2 = NewFakeConnection()
			go func() {
				<-time.After(7 * time.Second) //Go rules
				p2.playerReady <- true
			}()
		} else {
			log.Println("Couldn't send rup signal to player 2", m.Players[1])
			return
		}
	}
	go func() {
		p1ready := false
		p2ready := false
		for !(p1ready && p2ready) { //while not both players readied
			select {
				case <- m.timer.C: //Block until timer reaches maturity
					m.ExpireRup([]bool{p1ready, p2ready})
					return //NO MORE FUNCTION!!! >:(
				case <- p1.playerReady: //r1 receives a true only when the client sends a message.
					p1ready = true
					log.Println("player 1 has readied")
				case <- p2.playerReady:
					p2ready = true
					log.Println("player 2 has readied")
			}
		}
		log.Println("both players have readied")
		m.ExpireRup([]bool{false, false}) //We're "kicking" both from the queue but in this case we're going to follow it up with making a match :-) Hehehe
	}()
}

//ExpireRup is used to end the rup timer and manage the queue
//Pass a slice of 2 bools that match to ready signals for players in the slice m.Players
func (m *prematch) ExpireRup(readies []bool) {
	log.Println("Expiring Rup, player 1 and 2 back to queue?:", readies)
	m.timer.Stop() //Stop the timer, if it hasn't executed yet
	m.timer = nil
	log.Println("bruh")
	for i, player := range m.Players {
		if player.Steamid != "FakePlayer" {
			if readies[i] == true { //player was ready when timer ended, add them back to queue
				QueueUpdate(true, player.Connection)
			} else {	//player didn't ready, remove them from idling in queue
				QueueUpdate(false, player.Connection)
			}
			if player.Connection != nil {
				player.Connection.sendJSON <- NewRupSignalMsg(false, false)
			}
		}
	}
}

func RemovePlayersFromQueue(players []PlayerAdded, hub *Hub) {
	for _, p := range players {
		delete(GameQueue, p.Steamid)
	}
	if hub != nil {
		SendQueueToClients(hub)
	}
}

func NewFakeConnection() *connection {
	return &connection{playerReady: make(chan bool, 8)}
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
