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
	"math/rand"
	"database/sql"
	"github.com/go-sql-driver/mysql"
	"github.com/leighmacdonald/steamid/v2/steamid"
	"io/ioutil"
	"encoding/json"
)

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
			elo, err := GetElo(user.id)
			if err != nil {
				panic(err)
			}
			user.elo = elo
		}
		if user.id != "" {
			c.Set("User", user)
			log.Print("Authing user")
		} else {
			log.Print("Not authing")
		}
	}
} //this is fairly superfluous at this point but if i build out the User type I will want to add stuff here probably

var SelectElo *sql.Stmt

func main() {
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

	hub := newHub()
	rout.GET("/websock", func(c *gin.Context) {
		c.Set("Hub", *hub) //all websocket connections should have the same hub (server)
		WsServer(c)
	})

	rout.GET("/queue", func(c *gin.Context) {
		c.HTML(http.StatusOK, "queue.html", gin.H{})
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

	AddPlayersTest(118, 14)
	SendQueueToClients(hub)
	rout.Run()
}

func loginSteam(c *gin.Context) {
	var w http.ResponseWriter = c.Writer
	var r *http.Request = c.Request
	opId := steam_go.NewOpenId(r) //creates an openid object used by the steam_go module but doesn't seem to actually authenticate anything yet (it takes r so it can read URL keyvalues, where openid does its comms)
	switch opId.Mode() {
		case "": //openid has not done anything yet, so redirect to steam login and begin the process
			http.Redirect(w, r, opId.AuthUrl(), 301)
			log.Print("OpenID 301 Redirecting")
		case "cancel": //Cancel authentication, treat user as unauthenticated
			w.Write([]byte("authorization cancelled"))
			log.Print("OpenID auth cancelled")
		default:
			steamId, err := opId.ValidateAndGetId() //redirects your user to steam to authenticate, returns their id or an error
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}

			session := sessions.Default(c)
			session.Set("steamid", steamId)
			session.Save()
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

func GetElo(steam64 string) (int, error) {
	s64 := steamid.ParseString(steam64)[0]	//ParseString returns an array. I like this over SID64FromString because no error testing.
	steam2 := steamid.SID64ToSID(s64) //7777777777777 -> Steam_0:1:1111111
	var rating int
	err := SelectElo.QueryRow(steam2).Scan(&rating)
	//if err is row doesn't exist for this steam2, ignore and return 1600 (default elo in mgemod)
	return rating, err
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

type PlayerEntries map[string]PlayerAdded //this is a type
var GameQueue = make(PlayerEntries) //this is an instance of the type

func WsServer(c *gin.Context) {
	h, _ := c.Get("Hub")
	hub := h.(Hub) //cast the context to Hub type

	usr, lgdin := c.Get("User")
	if lgdin {
		//steamid := usr.(User).id	//cast the usr context to a User type, then get the id
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

		c := &connection{
			sendText: make(chan []byte, 256), 
			sendJSON: make(chan interface{}, 1024),
			h: &hub, 
			user: usr.(User),
		} //create our ws connection object
		hub.addConnection(c) //Add our connection to the hub
		defer hub.removeConnection(c) //Remove
		var wg sync.WaitGroup
		wg.Add(2)
		go c.writer(&wg, conn)
		go c.reader(&wg, conn)
		wg.Wait()
		conn.Close()
	}
}

func QueueUpdate(joining bool, conn *connection) { //The individual act of joining/leaving the queue. Should be followed by SendQueueToClients
	steamid := conn.user.id
	if joining { //add to queue
			GameQueue[steamid] = PlayerAdded{Connection: conn, Steamid: steamid, Elo: conn.user.elo, WaitingSince: time.Now()}//steamid//lol
	} else { //remove from queue
			delete(GameQueue, steamid) //remove steamid from gamequeue
	}

	SendQueueToClients(conn.h)
}

func AddPlayersTest(seed int64, maxplayers int) {
	rand.Seed(seed)
	i := rand.Intn(maxplayers+1) + 3
	n := 0
	for n <= i {
		n = n + 1
		GameQueue[fmt.Sprintf("%d", n)] = PlayerAdded{
			Connection: nil, 
			Steamid: fmt.Sprintf("%d", n), 
			Elo: rand.Intn(2000) + 1000, 
			WaitingSince: time.Now().Add(time.Second * -time.Duration(rand.Intn(120) + 1)),	//subtracts a random amount of seconds from the current time
		}
	}
}

func SendQueueToClients(hub *Hub) {
	for c := range hub.connections {
		_, ok := GameQueue[c.user.id] //check if key exists in map
		ack := &AckQueue{Type:"AckQueue", Queue: GameQueue, IsInQueue: ok}	//send a personalized ack out to each client, including confirmation that they're still inqueue
		c.sendJSON <- ack
	}
}

