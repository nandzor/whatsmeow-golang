package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	_ "github.com/lib/pq" // PostgreSQL driver
	_ "github.com/mattn/go-sqlite3"
	"github.com/mdp/qrterminal"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var client *whatsmeow.Client
var db *gorm.DB
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins (you can customize this for security)
	},
}
var wsClients = make(map[*websocket.Conn]bool)
var wsMutex sync.Mutex

type Request struct {
	Recipient string `json:"recipient"`
	Message   string `json:"message"`
}

type Message struct {
	ID        uint   `gorm:"primaryKey"`
	Sender    string `gorm:"not null"`
	Message   string `gorm:"not null"`
	Timestamp string `gorm:"not null;uniqueIndex:idx_sender_message_timestamp"`
}

func initDatabase() *gorm.DB {
	dsn := "host=100.81.120.54 port=5432 user=root password=kambin dbname=wa_lib sslmode=disable"
	var err error
	db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatalf("Error opening database: %v", err)
	}

	err = db.AutoMigrate(&Message{})
	if err != nil {
		log.Fatalf("Error migrating database: %v", err)
	}

	return db
}

func sendMessage(c *gin.Context) {
	var request Request

	// Bind JSON payload to the request struct
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	// Parse recipient JID
	recipientJID, ok := parseJID(request.Recipient)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid recipient JID"})
		return
	}

	// Send message
	msg := &waProto.Message{
		Conversation: proto.String(request.Message),
	}
	resp, err := client.SendMessage(context.Background(), recipientJID, msg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to send message: %v", err)})
		return
	}

	// Handle incoming message (store in DB and broadcast)
	handleIncomingMessage(request)

	// Respond with success
	c.JSON(http.StatusOK, gin.H{"message": "Message sent successfully", "response": resp})
}

func handleIncomingMessage(input interface{}) {
	loc, _ := time.LoadLocation("Asia/Jakarta")
	var sender, message, timestampUTC7 string

	switch v := input.(type) {
	case *events.Message:
		timestampUTC7 = v.Info.Timestamp.In(loc).String()
		sender = v.Info.Sender.String()
		message = v.Message.GetConversation()

	case Request:
		timestampUTC7 = time.Now().In(loc).String()
		sender = "6285123945816@s.whatsapp.net"
		message = v.Message
		log.Printf("Sender: %s, Message: %s", sender, message)

	default:
		log.Printf("Unsupported input type: %T", input)
		return
	}

	msg := Message{
		Sender:    sender,
		Message:   message,
		Timestamp: timestampUTC7,
	}

	result := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "sender"}, {Name: "message"}, {Name: "timestamp"}},
		UpdateAll: true,
	}).Create(&msg)

	if result.Error != nil {
		log.Printf("Error inserting or updating message in database: %v", result.Error)
		return
	}

	broadcastMessage(map[string]string{
		"sender":    sender,
		"message":   message,
		"timestamp": timestampUTC7,
	})
}

func broadcastMessage(message map[string]string) {
	wsMutex.Lock()
	defer wsMutex.Unlock()

	for client := range wsClients {
		err := client.WriteJSON(message)
		if err != nil {
			log.Printf("Error broadcasting message: %v", err)
			client.Close()
			delete(wsClients, client)
		}
	}
}

func handleWebSocket(c *gin.Context) {
	// Upgrade the HTTP connection to a WebSocket connection
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// Register the new WebSocket client
	wsMutex.Lock()
	wsClients[conn] = true
	wsMutex.Unlock()

	// Listen for messages from the client (not used in this case)
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			log.Printf("WebSocket read error: %v", err)
			break
		}
	}

	// Remove the client when the connection is closed
	wsMutex.Lock()
	delete(wsClients, conn)
	wsMutex.Unlock()
}

func scanQR(c *gin.Context) {
	// Check if the user is already logged in
	if client.Store.ID != nil {
		c.JSON(http.StatusOK, gin.H{"message": "Already logged in"})
		return
	}

	// If not logged in, start the QR code process
	qrChan, err := client.GetQRChannel(context.Background())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get QR channel"})
		return
	}

	err = client.Connect()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to connect to WhatsApp"})
		return
	}

	qrCode := <-qrChan
	switch qrCode.Event {
	case "code":
		// Validate QR code data
		if qrCode.Code == "" {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid QR code data"})
			return
		}

		// Render QR code in the terminal using qrterminal
		fmt.Println("Scan this QR code with your phone:")
		qrterminal.Generate(qrCode.Code, qrterminal.L, os.Stdout)

		// URL-encode the QR code data to ensure compatibility with the QR server
		encodedQRCode := url.QueryEscape(qrCode.Code)

		// Serve an HTML page with the QR code
		html := `
            <!DOCTYPE html>
            <html lang="en">
            <head>
                <meta charset="UTF-8">
                <meta name="viewport" content="width=device-width, initial-scale=1.0">
                <title>Scan QR Code</title>
                <style>
                    body {
                        font-family: Arial, sans-serif;
                        text-align: center;
                        margin-top: 50px;
                    }
                    img {
                        max-width: 300px;
                        height: auto;
                    }
                </style>
            </head>
            <body>
                <h1>Scan the QR Code with Your Phone</h1>
                <img src="https://api.qrserver.com/v1/create-qr-code/?size=300x300&data=%s" alt="QR Code">
            </body>
            </html>
        `

		// Replace the placeholder with the URL-encoded QR code data
		c.Header("Content-Type", "text/html")
		c.String(http.StatusOK, fmt.Sprintf(html, encodedQRCode))

	case "timeout":
		c.JSON(http.StatusRequestTimeout, gin.H{"error": "QR code timed out"})

	case "login":
		// Log the entire qrCode object to inspect its structure
		fmt.Printf("QR Code Event: %+v\n", qrCode)

		// Assuming the library automatically manages the session
		if client.Store != nil && client.Store.ID != nil {
			c.JSON(http.StatusOK, gin.H{"message": "Login successful"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve session data"})
		}
	}
}

func getGroup(c *gin.Context) {
	// Check if the client is initialized (logged in)
	if client == nil || client.Store.ID == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Device must be scanned first"})
		return
	}

	// Fetch joined groups
	groups, err := client.GetJoinedGroups()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get groups"})
		return
	}

	// Check if there are no groups
	if len(groups) == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "No groups found"})
		return
	}

	// Prepare the list of groups
	var groupList []map[string]string
	for _, group := range groups {
		groupInfo := map[string]string{
			"id":   group.JID.String(),
			"name": group.GroupName.Name,
		}
		groupList = append(groupList, groupInfo)
	}

	// Return the list of groups as JSON
	c.JSON(http.StatusOK, gin.H{"groups": groupList})
}

func receiveMessage(c *gin.Context) {
	var messages []Message

	result := db.Order("timestamp ASC").Find(&messages)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch messages from database"})
		return
	}

	var messageList []map[string]string
	for _, msg := range messages {
		messageList = append(messageList, map[string]string{
			"sender":    msg.Sender,
			"message":   msg.Message,
			"timestamp": msg.Timestamp,
		})
	}

	if len(messageList) == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "No received messages"})
	} else {
		c.JSON(http.StatusOK, gin.H{"received_messages": messageList})
	}
}

func parseJID(raw string) (types.JID, bool) {
	// Append "@s.whatsapp.net" to the raw number
	raw = raw + "@s.whatsapp.net"

	// Parse the JID using the types.ParseJID function
	jid, err := types.ParseJID(raw)
	if err != nil || jid.User == "" || jid.Server == "" {
		return jid, false
	}
	return jid, true
}

func initClient() {
	// Initialize database for storing WhatsApp sessions
	container, err := sqlstore.New("sqlite3", "file:whatsapp.db?_foreign_keys=on", nil)
	if err != nil {
		log.Fatalf("Error initializing database: %v", err)
	}
	deviceStore, err := container.GetFirstDevice()
	if err != nil {
		log.Fatalf("Error getting device store: %v", err)
	}
	client = whatsmeow.NewClient(deviceStore, nil)

	// Add event handler to capture incoming messages
	client.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			handleIncomingMessage(v)
		}
	})

	// Check if the client has an existing session
	if client.Store.ID == nil {
		log.Println("No existing session found. Please scan the QR code to log in.")
	} else {
		log.Println("Existing session found. Attempting to reconnect...")
		err := client.Connect()
		if err != nil {
			log.Fatalf("Failed to reconnect: %v", err)
		}
		log.Println("Reconnected successfully!")
	}
}

func main() {
	initClient()
	db = initDatabase()
	router := gin.Default()

	// Configure CORS middleware
	config := cors.DefaultConfig()
	config.AllowOrigins = []string{"*"} // Replace with your frontend origin
	config.AllowMethods = []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}
	config.AllowHeaders = []string{"Origin", "Content-Type", "Authorization"}
	config.AllowCredentials = true

	router.Use(cors.New(config)) // Apply CORS middleware globally

	// Routes
	router.GET("/scan", func(c *gin.Context) {
		scanQR(c)
	})
	router.POST("/send-message", sendMessage)
	router.GET("/get-group", getGroup)
	router.GET("/receive-message", receiveMessage)

	router.GET("/ws", func(c *gin.Context) {
		handleWebSocket(c)
	})

	// Start server
	router.Run(":8050")
}
