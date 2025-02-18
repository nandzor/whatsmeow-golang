package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gin-contrib/cors" // Import CORS middleware
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	_ "github.com/mattn/go-sqlite3" // For QR code rendering
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

var client *whatsmeow.Client
var receivedMessages []map[string]string
var messagesMutex sync.Mutex

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins (you can customize this for security)
	},
}

var wsClients = make(map[*websocket.Conn]bool) // Connected WebSocket clients
var wsMutex sync.Mutex
var db *sql.DB

func initDatabase() *sql.DB {
	db, err := sql.Open("sqlite3", "file:whatsapp.db?_foreign_keys=on")
	if err != nil {
		log.Fatalf("Error opening database: %v", err)
	}

	// Create the messages table if it doesn't exist
	_, err = db.Exec(`
        CREATE TABLE IF NOT EXISTS messages (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            sender TEXT NOT NULL,
            message TEXT NOT NULL,
            timestamp TEXT NOT NULL
        )
    `)
	if err != nil {
		log.Fatalf("Error creating messages table: %v", err)
	}

	return db
}

func handleIncomingMessage(evt *events.Message) {
	loc, _ := time.LoadLocation("Asia/Jakarta") // UTC+7 timezone
	timestampUTC7 := evt.Info.Timestamp.In(loc).String()

	// Extract message details
	sender := evt.Info.Sender.String()
	message := evt.Message.GetConversation()

	// Insert the message into the database
	_, err := db.Exec(`
        INSERT INTO messages (sender, message, timestamp)
        VALUES (?, ?, ?)
    `, sender, message, timestampUTC7)
	if err != nil {
		log.Printf("Error inserting message into database: %v", err)
		return
	}

	// Broadcast the message to WebSocket clients
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
	if client.Store.ID == nil {
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

		select {
		case qrCode := <-qrChan:
			if qrCode.Event == "code" {
				encodedQRCode := url.QueryEscape(qrCode.Code)
				html := `
                    <!DOCTYPE html>
                    <html>
                    <body>
                        <h1>Scan the QR Code with Your Phone</h1>
                        <img src="https://api.qrserver.com/v1/create-qr-code/?size=300x300&data=%s">
                    </body>
                    </html>
                `
				// Replace the placeholder with the URL-encoded QR code data
				c.Header("Content-Type", "text/html")
				c.String(http.StatusOK, fmt.Sprintf(html, encodedQRCode))
			} else if qrCode.Event == "timeout" {
				c.JSON(http.StatusRequestTimeout, gin.H{"error": "QR code timed out"})
			}
		}
	} else {
		c.JSON(http.StatusOK, gin.H{"message": "Already logged in"})
	}
}

func sendMessage(c *gin.Context) {
	var request struct {
		Recipient string `json:"recipient"`
		Message   string `json:"message"`
	}

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

	c.JSON(http.StatusOK, gin.H{"message": "Message sent successfully", "response": resp})
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
	// Query the database for all messages
	rows, err := db.Query("SELECT sender, message, timestamp FROM messages ORDER BY timestamp ASC")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch messages from database"})
		return
	}
	defer rows.Close()

	// Parse the results into a slice of maps
	var messages []map[string]string
	for rows.Next() {
		var sender, message, timestamp string
		err := rows.Scan(&sender, &message, &timestamp)
		if err != nil {
			log.Printf("Error scanning row: %v", err)
			continue
		}
		messages = append(messages, map[string]string{
			"sender":    sender,
			"message":   message,
			"timestamp": timestamp,
		})
	}

	// Check for errors during iteration
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error iterating over messages"})
		return
	}

	// Return the messages as JSON
	if len(messages) == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "No received messages"})
	} else {
		c.JSON(http.StatusOK, gin.H{"received_messages": messages})
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

func loadMessagesFromDB() {
	// Load previously received messages from the database or any other persistent storage
	// For simplicity, we'll assume messages are stored in memory (receivedMessages slice)
	// You can extend this to fetch messages from the database if needed.
	messagesMutex.Lock()
	defer messagesMutex.Unlock()

	if len(receivedMessages) == 0 {
		log.Println("No previous messages found in memory.")
	} else {
		log.Printf("Loaded %d previous messages from memory.", len(receivedMessages))
	}
}

func main() {
	initClient()
	loadMessagesFromDB()
	db = initDatabase()
	defer db.Close()

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
