package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/captainzidgel/rgl"
	"github.com/dgraph-io/ristretto"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
	"github.com/gorilla/websocket"
	"github.com/leighmacdonald/steamid/v2/steamid"
	"github.com/leighmacdonald/steamweb"
	"github.com/solovev/steam_go"
	"golang.org/x/exp/maps"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

//This variable can be overwritten in a test to replace time.Now() with whatever time we want
var now = time.Now

type User struct {
	id       string
	elo      int
	summary  steamweb.PlayerSummary
	Nickname string
	Avatar   struct {
		Small  string
		Medium string
		Full   string
	}
	Ban *ban
}

func isAdmin(user User) bool {
	return user.id == "76561198098770013"
}

type sqlConfig struct {
	User   string `json:"user"`
	Pass   string `json:"pass"`
	Addr   string `json:"addr"`
	DbName string `json:"dbName"`
}

type MMCfg struct {
	Database sqlConfig `json:"database"`
	Ws       struct {
		Addr string `json:"addr"`
		Port string `json:"port"`
	} `json:"websocket_expose"`
	WhitelistEnabled bool                 `json:"whitelistEnabled"`
	WhitelistRules   *map[string][]string `json:"whitelist"`
	SteamToken       string               `json:"STEAM_TOKEN"`
	ServerSecret     string               `json:"MGEME_SV_SECRET"`
	SessionSecret    string               `json:"SESSION_SECRET"`
}

var steamCache *ristretto.Cache
var rglCache *ristretto.Cache
var banCache *ristretto.Cache

func GetUser() gin.HandlerFunc { //middleware to set contextual variable from session
	return func(c *gin.Context) {
		var user User
		session := sessions.Default(c)
		if id := session.Get("steamid"); id != nil {
			user.id = id.(string)
			user.elo = GetElo(user.id)
			log.Println("Authorizing user with steamid", user.id)
			summary := getTotalSummary(user.id)

			//user.summary = summary
			user.Nickname = summary.PersonaName
			user.Avatar.Small = summary.Avatar
			user.Avatar.Medium = summary.AvatarMedium
			user.Avatar.Full = summary.AvatarFull

			ban := checkBanCache(user.id)
			user.Ban = ban

			c.Set("User", user)
		} else {
			log.Println("session steamid was nil, not authorizing")
		}
	}
} //this is fairly superfluous at this point but if i build out the User type I will want to add stuff here probably

type webServer struct {
	gameServerHub *Hub
	playerHub     *Hub

	gameQueue PlayerEntries
	rupTime   int

	queueMutex sync.RWMutex

	expectingRup map[string]bool //I feel like this is too ostentatious for such a small feature but i didnt really feel like redesigning the match storage system.
	erMutex      sync.Mutex      //maps aren't thread safe

	svSecret string

	matchmakerStop chan int
}

func newWebServer() *webServer {
	web := webServer{}
	web.gameServerHub = newHub("game")
	web.playerHub = newHub("user")
	web.gameQueue = make(PlayerEntries)
	web.rupTime = 35

	web.queueMutex = sync.RWMutex{}

	web.expectingRup = make(map[string]bool)
	web.erMutex = sync.Mutex{}

	web.matchmakerStop = make(chan int, 0)

	return &web
}

func shouldMatch(a, b PlayerAdded) bool {
	if a.WaitTime(now())+b.WaitTime(now()) >= a.Distance(b) {
		return true
	}
	if 12 < 10 { //if LOW_PLAYER_MODE ?
		if a.WaitTime(now()) > 30 || b.WaitTime(now()) > 30 {
			return true
		}
	}
	return false
}

func (w *webServer) Matchmaker() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
total:
	for {
	out:
		select {
		case <-ticker.C:
			w.queueMutex.RLock()
			requests := maps.Values(w.gameQueue)
			w.queueMutex.RUnlock()
			for i, a := range requests {
				for ii, b := range requests {
					if i == ii {
						continue
					}
					if shouldMatch(a, b) {
						m := []PlayerAdded{a, b}
						match := w.newMatchFromMatchmaker(m)
						go w.sendReadyUpPrompt(&match, nil)
						break out
					}
				}
			}
		case <-w.matchmakerStop:
			break total
		}
	}
}

var db *sql.DB
var SelectElo *sql.Stmt
var r = rgl.DefaultRateLimit()

func main() {
	//unmarshal configs
	content, err := os.ReadFile("./config/webconfig.json")
	if err != nil {
		log.Fatal("Error opening config: ", err)
	}
	var conf MMCfg
	err = json.Unmarshal(content, &conf)
	if err != nil {
		log.Fatal("Error unmarshalling: ", err)
	}

	if err := steamweb.SetKey(conf.SteamToken); err != nil {
		log.Print("Error setting steam token: ", err)
	}

	//wsHostPtr := flag.String("addr", getOutboundIp(), "Address to listen on (Relayed to clients to know where to send messages to, ie 'localhost' on windows)")
	//portPtr := flag.String("port", "8080", "Port to listen on")
	//flag.Parse()
	if conf.WhitelistEnabled {
		whitelist = loadWhitelist(*conf.WhitelistRules)
	}

	steamCache = newCache()
	rglCache = newCache()
	banCache = newCache()

	mgeme := newWebServer()
	mgeme.svSecret = conf.ServerSecret

	rout := gin.Default()
	rout.Delims("%%", "%%")
	rout.LoadHTMLGlob("./views/templates/*")

	store := cookie.NewStore([]byte(conf.SessionSecret))
	store.Options(sessions.Options{
		Domain:   conf.Ws.Addr,
		SameSite: http.SameSiteLaxMode,
	})
	rout.Use(sessions.Sessions("sessions", store))
	rout.Use(GetUser())

	rout.GET("/", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	rout.GET("/login", func(c *gin.Context) {
		loginSteam(c)
		steamId := sessions.Default(c).Get("steamid")
		if steamId == nil {
			c.String(400, "Could not log in")
			return
		}
		c.String(200, steamId.(string))
	})

	rout.GET("/logout", func(c *gin.Context) {
		session := sessions.Default(c)
		sid := session.Get("steamid") //get for error checking only
		session.Set("steamid", nil)
		err := session.Save()
		if err != nil {
			log.Fatalf("Err saving session for user %s: %v", sid, err)
		}

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

	rout.GET("/websock", func(c *gin.Context) { //The endpoint for user connections (ie users adding up to play, but not for servers connecting to transmit messages)
		mgeme.WsServer(c, "user")
	})

	rout.GET("/tf2serverep", func(c *gin.Context) { //endpoint for game servers
		mgeme.WsServer(c, "game")
	})

	rout.GET("/queue", func(c *gin.Context) {
		usr, loggedin := c.Get("User")
		var id string
		var ban *ban
		var user User
		if loggedin {
			user = usr.(User)
			id = user.id
			ban = user.Ban
			log.Println("User name:", user.Nickname)
		}
		if !loggedin || ((ban == nil || !ban.isActive()) && (!conf.WhitelistEnabled || (isWhitelisted(id)))) {
			c.HTML(http.StatusOK, "queue.html", gin.H{"wsHost": conf.Ws.Addr, "wsPort": conf.Ws.Port, "loggedIn": loggedin, "steamid": id, "user": usr, "isAdmin": isAdmin(user)}) //clean this up later?
		} else {
			var reason string
			expires := time.Now().Add(10000 * time.Hour)
			if ban != nil && ban.isActive() {
				expires = ban.expires
				if ban.banLevel != -1 {
					reason = "Baiting/Quitting matches"
				} else {
					reason = "League ban"
				}
			} else {
				reason = "Whitelist mode is enabled and you are not permitted. If you think this is incorrect, please contact the site manager"
			}
			c.HTML(http.StatusOK, "banned.html", gin.H{"Expires": expires, "Reason": reason})
		}
	})

	dbCfg := mysql.NewConfig()      //create a new config object with default values
	dbCfg.User = conf.Database.User //insert my values into the config object... (username/password for sql user, etc)
	dbCfg.Passwd = conf.Database.Pass
	dbCfg.Net = "tcp"
	dbCfg.Addr = conf.Database.Addr
	dbCfg.DBName = conf.Database.DbName
	db, err := sql.Open("mysql", dbCfg.FormatDSN()) //opens a sql connection, the FormatDSN() function turns out config object into a driver string
	if err != nil {
		log.Fatal("Error connecting to sql: ", err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE IF NOT EXISTS bans(steam64 VARCHAR(20) PRIMARY KEY NOT NULL, expires BIGINT NOT NULL, level INT, lastOffence BIGINT)")
	if err != nil {
		log.Println(err)
	} //likely "no create permissions"

	SelectElo, err = db.Prepare("SELECT rating FROM mgemod_stats WHERE steamid = ?")
	if err != nil {
		log.Fatal(err)
	}
	defer SelectElo.Close()
	SelectBan, err = db.Prepare("SELECT expires, level, lastOffence FROM bans WHERE steam64 = ?")
	if err != nil {
		log.Fatal(err)
	}
	defer SelectBan.Close()

	UpdateBan, err = db.Prepare("INSERT INTO bans (steam64, expires, level, lastOffence) VALUES(?, ?, ?, ?) ON DUPLICATE KEY UPDATE expires=VALUES(expires), level=VALUES(level), lastOffence=VALUES(lastOffence)")
	if err != nil {
		log.Fatal(err)
	} //likely "no table bans"
	defer UpdateBan.Close()
	updateBanMethod = updateBanSql
	selectBanMethod = selectBanSql

	mgeme.sendQueueToClients()
	go mgeme.Matchmaker()
	rout.Run("0.0.0.0:8080")
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
			return
		}

		session := sessions.Default(c)
		session.Set("steamid", steamId)
		err = session.Save()
		if err != nil {
			log.Fatalf("Error setting steamid session for user %s: %v", steamId, err)
		}

		//parse original request (r) to see if there was a specific redirect param
		redir := r.FormValue("redirect")
		if redir == "queue" {
			c.Redirect(302, "/queue")
		}
	}
}

func GetElo(steam64 string) int {
	if flag.Lookup("test.v") != nil || strings.Contains(steam64, "FakePlayer") || !strings.HasPrefix(steam64, "7") {
		return 1600
	}
	s64 := steamid.ParseString(steam64)[0] //ParseString returns an array. I like this over SID64FromString because no error testing.
	steam2 := steamid.SID64ToSID(s64)      //7777777777777 -> Steam_0:1:1111111
	var rating int
	err := SelectElo.QueryRow(steam2).Scan(&rating)
	//if err is row doesn't exist for this steam2, ignore and return 1600 (default elo in mgemod)
	if err != nil { //To do: Double check this error is just "sql: no rows in result set"
		rating = 1600
	}
	return rating
}

type PlayerAdded struct {
	Connection   *connection
	User         User
	Steamid      string
	Elo          int
	WaitingSince time.Time
	//this type seems bare and the map seems unnecessary,
	//but if i build this out we will need more than 1 value so a key/value map doesnt make sense
	//example of further properties: maps desired, server location, classes desired
}

func (pa PlayerAdded) WaitTime(now time.Time) int {
	d := now.Sub(pa.WaitingSince).Seconds()
	return int(d)
}

func (pa PlayerAdded) Location() int {
	return pa.Elo
}

func abs(a int) int {
	if a < 0 {
		return a * -1
	}
	return a
}

func (pa PlayerAdded) Distance(to PlayerAdded) int {
	return abs(pa.Location() - to.Location())
}

type PlayerEntries map[string]PlayerAdded //this is a maptype of PlayerAdded structs. It maps steamids to player data.

func (w *webServer) WsServer(c *gin.Context, hubtype string) error {
	if !(hubtype == "user" || hubtype == "game") {
		return fmt.Errorf("Incorrect hubtype of %v, use 'user' or 'game'", hubtype)
	}
	var hub *Hub
	if hubtype == "user" {
		hub = w.playerHub
	} else {
		hub = w.gameServerHub
	}

	usr, lgdin := c.Get("User")     //lgdin (loggedin) represents if the key User exists in context
	if lgdin || hubtype == "game" { //We don't bother upgrading the connection for an unlogged in user (but we will for game servers!)
		write := c.Writer
		r := c.Request
		//"Upgrade" the HTTP connection to a WebSocket connection, and use default buffer sizes
		var upgrader = websocket.Upgrader{
			ReadBufferSize:  0,
			WriteBufferSize: 0,
			CheckOrigin:     func(r *http.Request) bool { return true },
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
			sendText:    make(chan []byte, 256),
			sendJSON:    make(chan interface{}, 1024),
			playerReady: make(chan bool, 0),
			h:           hub,
			id:          id,
		} //create our ws connection object
		hub.addConnection(clientConn) //Add our connection to the hub
        if hubtype == "user" {
			clientConn.sendJSON <- NewAckQueueMsg(w.gameQueue, id)
            hub.connectionsMx.Lock()
			hub.connections[clientConn] = usr.(User)
            hub.connectionsMx.Unlock()
			w.resolveConnectingUser(clientConn)
		}
		defer hub.removeConnection(clientConn)
		var wg sync.WaitGroup
		wg.Add(2)
        hub.connectionsMx.Lock()
		go clientConn.writer(&wg, wsConn)
		go clientConn.reader(&wg, wsConn, w)
		hub.connectionsMx.Unlock()
		wg.Wait()
		if hubtype == "user" {
			w.erMutex.Lock()
			delete(w.expectingRup, id)
			w.erMutex.Unlock()
		}
		log.Println("Closing conn")
		wsConn.WriteControl(websocket.CloseMessage, []byte{}, time.Now().Add(time.Second))
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
	w.gameServerHub.connectionsMx.Lock()
	defer w.gameServerHub.connectionsMx.Unlock()
	for _, server := range w.gameServerHub.connections {
		server, ok := server.(*gameServer)
		if !ok {
			log.Printf("Couldn't cast serverhub connection to server type %v\n", server)
		} else {
			matchIndex := server.findMatchByPlayer(conn.id)
			if matchIndex > -1 {
				log.Printf("Found user in match on server %d, sending to user\n", matchIndex)
				conn.sendJSON <- server.Matches[matchIndex]
				break
			}
		}
	}
}

func (w *webServer) queueUpdate(joining bool, conn *connection) { //The individual act of joining/leaving the queue. Should be followed by SendQueueToClients
	steamid := conn.id
	w.queueMutex.Lock()
	w.playerHub.connectionsMx.Lock()
	if joining { //add to queue
		user, ok := w.playerHub.connections[conn].(User)
		if !ok {
			log.Printf("Error casting user to User at queueUpdate.")
		}
		if b := checkBanCache(steamid); b != nil && b.isActive() {
			log.Printf("Attempt to queue by banned user %s\n", steamid)
			return
		}
		w.gameQueue[steamid] = PlayerAdded{Connection: conn, User: user, Steamid: steamid, Elo: GetElo(conn.id), WaitingSince: time.Now()} //steamid//lol
		log.Printf("Adding %s to queue\n", conn.id)
	} else { //remove from queue
		delete(w.gameQueue, steamid) //remove steamid from gamequeue
		log.Printf("Removing %s from queue\n", conn.id)
	}
	w.queueMutex.Unlock()
	w.playerHub.connectionsMx.Unlock() //we can't defer these because sendQueueToClients has a lock in it
	w.sendQueueToClients()
}

func (w *webServer) sendQueueToClients() {
	w.queueMutex.Lock()
	w.playerHub.connectionsMx.Lock()
	defer w.queueMutex.Unlock()
	defer w.playerHub.connectionsMx.Unlock()
	for c := range w.playerHub.connections {
		c.sendJSON <- NewAckQueueMsg(w.gameQueue, c.id) //send a personalized ack out to each client, including confirmation that they're still inqueue
	}
}

type gameServer struct { //The stuff the webserver will want to know, doesn't necessarily have info like the IP as that isn't necessary.
	Matches map[int]*Match //Arena Index to Match. I used a map instead of a slide because not all arenas are used and I found using a slice in this manner too confusing. I kept confusing the slice index and the arena index.
	Info    matchServerInfo
	Full    bool //Can we fit more players into these arenas?
}

//Find what match contains a certain player (since players can only be in one match at a time, it should be sufficient to only pass one player)
//Return the index and not the match object since I'll probably be more interested in the match's position in the slice.
func (s *gameServer) findMatchByPlayer(id string) int {
	for i, m := range s.Matches {
		if m.P1id == id || m.P2id == id {
			return i
		}
	}
	return -1
}

func (w *webServer) findMatchByPlayer(id string) *Match {
	w.gameServerHub.connectionsMx.Lock()
	defer w.gameServerHub.connectionsMx.Unlock()
	for _, server := range w.gameServerHub.connections {
		log.Println("Checking A")
		server := server.(*gameServer)
		idx := server.findMatchByPlayer(id)
		if idx > -1 {
			log.Println("Found")
			return server.Matches[idx]
		}
	}
	return nil
}

func (s *gameServer) deleteMatch(ind int) {
	delete(s.Matches, ind)
}

//gamemaps
var mgeTrainingV8Arenas = []int{1, 2, 3, 4, 5, 6, 8, 10}

func (s *gameServer) assignArena(m *Match, gamemap []int) error {
	if s.Full {
		return fmt.Errorf("No room in this server")
	}
	rand_index := rand.Perm(len(gamemap)) //create a slice of n ints from 0 to n-1 so we can get a random arena and check them all for matches eventually
	for i, idx := range rand_index {
		arena_index := gamemap[idx]
		x, ok := s.Matches[arena_index] //x: a pointer to a match or nil, and if that value is defined.
		if ok && x != nil {             //"ok" will be true if x is defined as nil, since it could be a pointer
			if i == len(gamemap)-1 { //if we've iterated through all arenas still, we're full
				s.Full = true
				return fmt.Errorf("No room in this server") //Hopefully I won't be stupid enough to call this function on a full server but if I am, I covered my bases.
			} else { //there's a match in this arena but there's more to check. onwards with the loop
				continue
			}
		} //if undefined or defined as nil, the arena is free
		m.Arena = arena_index
		s.Matches[arena_index] = m
		return nil
	}
	return nil
}

type matchServerInfo struct { //Just the stuff users need to know
	Id   string `json:"id"`
	Host string `json:"ip"`
	Port string `json:"port"`
	Stv  string `json:"stv"`
}

const (
	matchInit              = 0
	matchRupSignal         = 1
	matchWaitingForPlayers = 2
	matchPlaying           = 3
	matchOver              = 4
)

//A match object should encapsulate an entire match from inception on the webserver to being sent to the game servers and the clients.
//The webserver will hold a slice of these and each server will hold their own copies as well
type Match struct {
	Type            string            `json:"type"`
	Arena           int               `json:"arenaId"`
	P1id            string            `json:"p1Id"` //players1 and 2 ids for serialization
	P2id            string            `json:"p2Id"`
	Configuration   map[string]string `json:"matchCfg"` //reserved: configuration may be something like "scout vs scout" or "demo vs demo" perhaps modeled as "cfg": "svs" or p1class : p2class
	ServerDetails   matchServerInfo   `json:"gameServer"`
	Status          int
	ConnectDeadline int64 `json:"deadline"` //Not set until match initialization. Though this deadline is not used by the server, it will be useful to the client.

	timer   *time.Timer   //no json tag!! Don't serialize it!!
	players []PlayerAdded //we embed the entire PlayerAdded object so we can remove them from queue but add them back with data unchanged when necessary
}

//The match object holds a table of PlayerAdded (queue items) so if the match is cancelled the queue can be restored. However these connections could have died at any point.
func (w *webServer) getPlayerConns(m *Match) []*connection {
	w.playerHub.connectionsMx.Lock()
	defer w.playerHub.connectionsMx.Unlock()
	s := make([]*connection, 0)
	for _, playerAdded := range m.players {
		if _, ok := w.playerHub.connections[playerAdded.Connection]; ok { //if connection still saved in hub
			s = append(s, playerAdded.Connection)
		}
	}
	return s
}

//Both players ready, send match to server and players.
func (w *webServer) initializeMatch(m *Match) {
	if m.ServerDetails.Id == "" {
		log.Printf("Warning: Initializing match with empty server details, defaulting to 1")
		_, obj := w.gameServerHub.findConnection("1")
		m.ServerDetails = obj.(*gameServer).Info
	}
	c, obj := w.gameServerHub.findConnection(m.ServerDetails.Id) //find connection for this id
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
	err := sv.assignArena(m, mgeTrainingV8Arenas)
	if err != nil {
		log.Fatalf("this server is full. How could you do this.")
	}

	c.sendJSON <- m //Sends match details to the gameserver (sourcemod)
	for _, player := range w.getPlayerConns(m) {
		player.sendJSON <- m
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

//Going to have to table this functionality until closer to production
func (w *webServer) getFreeServer() string {
	//Our servers are stored in a map by their connections as keys. Sort this out so we fill servers in order.
	//I am drowning in technical debt and my children will inherit it
	return "1"
	/*
		i := 0
		for i < len(w.gameServerHub) {
			id := strconv.Itoa(i)
			conn, sv := w.findConnection(id)
			if !sv.Full {
				return id
			}
			i = i + 1
		}
	*/
}

func (w *webServer) newMatchFromMatchmaker(players []PlayerAdded) Match {
	id := w.getFreeServer()
	if len(players) > 2 {
		log.Fatal("Tried to establish match with more than 2 players")
	}
	w.removePlayersFromQueue(players)
	return w.createMatchObject(players, id)
}

func (w *webServer) dummyMatch() (Match, error) { //change string to SteamID2 type?
	players, err := w.fillPlayerSlice(2, false)
	if err != nil {
		return Match{}, err
	}

	id := w.getFreeServer()
	//remove players from queue, update queue for all players
	w.removePlayersFromQueue(players)
	return w.createMatchObject(players, id), nil
}

func (w *webServer) createMatchObject(players []PlayerAdded, server string) Match { //gonna leave server as a param here so I can assign earlier and not return an error here
	log.Println("Matching together", players[0].Steamid, players[1].Steamid)
	_, sv := w.gameServerHub.findConnection(server)
	return Match{
		ServerDetails: sv.(*gameServer).Info,
		Configuration: make(map[string]string),
		P1id:          players[0].Steamid,
		P2id:          players[1].Steamid,
		timer:         nil,
		Status:        matchInit,
		players:       players,
	}
}

func (w *webServer) sendReadyUpPrompt(m *Match, wg *sync.WaitGroup) {
	if wg != nil {
		defer wg.Done()
	} else {
		log.Println("Warning: no waitgroup for sendReadyUpPrompt")
	}
	for _, player := range w.getPlayerConns(m) {
		if player == nil { //players is a slice of pointers, which could have been nilled out by this point
			log.Println("Couldn't send rup signal to player (no connection object)", player)
		} else {
			player.sendJSON <- NewRupSignalMsg(true, false, w.rupTime)
			w.erMutex.Lock()
			w.expectingRup[player.id] = true
			w.erMutex.Unlock()
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
		case <-m.timer.C: //Block until timer reaches maturity
			log.Println("Rup timer expired.")
			w.expireRup(m, p1ready, p2ready)
			w.erMutex.Lock()
			delete(w.expectingRup, p1.id)
			delete(w.expectingRup, p2.id)
			w.erMutex.Unlock()
			return
		case x := <-p1.playerReady: //r1 receives a true only when the client sends a message. could receive a false (zero-value) when the channel is closed.
			if x {
				p1ready = true
				log.Printf("%s has readied\n", p1.id)
				w.erMutex.Lock()
				delete(w.expectingRup, p1.id)
				w.erMutex.Unlock()
			} else {
				log.Printf("Warning: reading from closed playerReady channel. user %s\n", p1.id) //You don't want to see this buddy! Thankfully I only see it in my messed up tests
			}
		case x := <-p2.playerReady:
			if x {
				p2ready = true
				log.Printf("%s has readied\n", p1.id)
				w.erMutex.Lock()
				delete(w.expectingRup, p2.id)
				w.erMutex.Unlock()
			} else {
				log.Printf("Warning: reading from closed playerReady channel. user %s\n", p2.id)
			}
		}
	}
	log.Println("both players have readied")
	w.expireRup(m, false, false) //We're "kicking" both from the queue but in this case we're going to follow it up with making a match :-) Hehehe
	w.initializeMatch(m)
}

//ExpireRup is used to end the rup timer and manage the queue
//Pass a slice of 2 bools that match to ready signals for players in the slice m.players
func (w *webServer) expireRup(m *Match, readies ...bool) {
	log.Println("Killing rup timer, player 1 and 2 back to queue?:", readies)
	m.timer.Stop() //Stop the timer, if it hasn't executed yet
	m.timer = nil
	//this will infinite lock because queueUpdate contains a connectionsMx lock.
	for i, player := range m.players {
		player := player.Connection
		if readies[i] == true { //player was ready when timer ended, add them back to queue
			w.queueUpdate(true, player) //this doesn't actually restore players, it creates new player added objects. implement actual later.
		} else { //player didn't ready, remove them from idling in queue
			w.queueUpdate(false, player)
		}
		log.Println("Sending rupsignal expire to", player.id)
		w.playerHub.connectionsMx.Lock()
		if _, ok := w.playerHub.connections[player]; ok {
			player.sendJSON <- NewRupSignalMsg(false, false, w.rupTime)
		}
		w.playerHub.connectionsMx.Unlock()
	}
}

func (w *webServer) clearAllMatches() {
	w.queueMutex.Lock()
	w.gameServerHub.connectionsMx.Lock()
	defer w.queueMutex.Unlock()
	defer w.gameServerHub.connectionsMx.Unlock()
	for _, server := range w.gameServerHub.connections {
		server := server.(*gameServer)
		for _, match := range server.Matches {
			//Add players back to queue: Not calling expireRup as a shortcut for this because that would send a rupsignal and start a client timer.
			for _, p := range match.players {
				w.gameQueue[p.Connection.id] = p //Not using QueueUpdate, as to avoid sending a queue message for every player. Waiting until after.
			}
		}
		server.Matches = make(map[int]*Match)
	}
	w.sendQueueToClients()
}

func (w *webServer) removePlayersFromQueue(players []PlayerAdded) {
	w.queueMutex.Lock()
	for _, p := range players {
		delete(w.gameQueue, p.Steamid)
	}
	w.queueMutex.Unlock()
	w.sendQueueToClients()
}

func NewFakeConnection() *connection {
	return &connection{
		playerReady: make(chan bool, 9999),
		sendJSON:    make(chan interface{}, 9999),
	}
}

func clamp(n, min, max int) int {
	if n > max {
		return max
	} else if n < min {
		return min
	} else {
		return n
	}
}
