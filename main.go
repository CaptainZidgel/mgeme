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
)

type User struct {
	id string
}

func GetUser() gin.HandlerFunc { //middleware to set contextual variable from session
	return func(c *gin.Context) {
		var user User
		session := sessions.Default(c)
		if id := session.Get("steamid"); id != nil {
			user.id = id.(string)
		}
		if user.id != "" {
			c.Set("User", user)
			log.Print("Authing user")
		} else {
			log.Print("Not authing")
		}
	}
} //this is fairly superfluous at this point but if i build out the User type I will want to add stuff here probably

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

	rout.GET("/websock", WsServer)

	rout.GET("/queue", func(c *gin.Context) {
		c.HTML(http.StatusOK, "queue.html", gin.H{})
	})

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

func GetElo(steamid string) int {
	return 1000
}

type PlayerAdded struct {
	Connection *websocket.Conn
	Steamid string
	Elo int
	SecsWaiting int
	//this type seems bare and the map seems unnecessary,
	//but if i build this out we will need more than 1 value so a key/value map doesnt make sense
	//example of further properties: maps desired, server location, classes desired
}

type PlayerEntries map[string]PlayerAdded //this is a type
var GameQueue = make(PlayerEntries) //this is an instance of the type

func WsServer(c *gin.Context) {
	usr, lgdin := c.Get("User")
	if lgdin {
		steamid := usr.(User).id
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

		QueueUpdate(false, steamid, conn)

		for {
	/*
		messageType, msg, err := conn.ReadMessage() //msg is byte slice, messageType is either websocket.BinaryMessage or websocket.TextMessage
		if err != nil {
			log.Println(err)
			return
		}
		if messageType == websocket.BinaryMessage {
			log.Println(string(msg))
		} else {
			log.Println(msg)
		}
	*/
			var qmsg Message
			err2 := conn.ReadJSON(&qmsg)
			if err2 != nil {
				fmt.Printf("Error reading json: %s\n", err2.Error())
				if websocket.IsCloseError(err2, 1001) {
					fmt.Printf("Disconnection (1001)\n")
					break
				}
				continue
			}
			herr := HandleMessage(qmsg, steamid, conn)
			if herr != nil {
				conn.WriteMessage(websocket.TextMessage, []byte(herr.Error()))
			}
		}
	}
}

func QueueUpdate(joining bool, steamid string, conn *websocket.Conn) {
	if joining { //add to queue
			GameQueue[steamid] = PlayerAdded{Connection: conn, Steamid: steamid, Elo: GetElo(steamid), SecsWaiting: 0}//steamid//lol
	} else { //remove from queue
			delete(GameQueue, steamid) //remove steamid from gamequeue
	}
	//simpler ack: ack := &AckQueue{Type:"AckQueue", NewQueueStatus: !joining}
	ack := &AckQueue{Type:"AckQueue", NewQueueStatus: !joining, Queue: GameQueue}
	//todo: pass a slice of steamids instead of the server's whole info of the gamequeue
	//https://stackoverflow.com/questions/21362950/getting-a-slice-of-keys-from-a-map
	err := conn.WriteJSON(ack)
	if err != nil {
		panic(err)
	}
}


