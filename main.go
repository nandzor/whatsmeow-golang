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

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
	"github.com/mdp/qrterminal/v3" // For QR code rendering
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
}

func handleIncomingMessage(evt *events.Message) {
	messagesMutex.Lock()
	defer messagesMutex.Unlock()

	// Transform timestamp to UTC+7
	loc, _ := time.LoadLocation("Asia/Jakarta") // UTC+7 timezone
	timestampUTC7 := evt.Info.Timestamp.In(loc).String()

	// Extract message details
	message := map[string]string{
		"sender":    evt.Info.Sender.String(),
		"message":   evt.Message.GetConversation(),
		"timestamp": timestampUTC7,
	}
	receivedMessages = append(receivedMessages, message)
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

		qrCode := <-qrChan
		if qrCode.Event == "code" {
			// Validate QR code data
			if qrCode.Code == "" {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid QR code data"})
				return
			}

			// Render QR code in the terminal using qrterminal
			fmt.Println("Scan this QR code with your phone:")
			qrterminal.Generate(qrCode.Code, qrterminal.L, os.Stdout) // Use os.Stdout as the writer

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
		} else if qrCode.Event == "timeout" {
			c.JSON(http.StatusRequestTimeout, gin.H{"error": "QR code timed out"})
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
	messagesMutex.Lock()
	defer messagesMutex.Unlock()

	if len(receivedMessages) == 0 {
		c.JSON(http.StatusOK, gin.H{"message": "No received messages"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"received_messages": receivedMessages})
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

func main() {
	initClient()

	router := gin.Default()

	// Routes
	router.GET("/scan", func(c *gin.Context) {
		scanQR(c)
	})
	router.POST("/send-message", sendMessage)
	router.GET("/get-group", getGroup)
	router.GET("/receive-message", receiveMessage)

	// Start server
	router.Run(":8050")
}
