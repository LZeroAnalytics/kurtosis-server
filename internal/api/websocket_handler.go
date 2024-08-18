package api

import (
	"fmt"
	"github.com/gorilla/websocket"
	"kurtosis-server/internal/api/util"
	"log"
	"net/http"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

var logUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func StreamOutput(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionID")

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade to WebSocket: %v", err)
		return
	}

	log.Printf("Re-attaching WebSocket connection for session ID: %s", sessionID)
	redisSession, err := util.GetSession(sessionID)
	if err != nil {
		log.Printf("Error retrieving session from Redis: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Invalid session ID"))
		conn.Close()
		return
	}

	log.Printf("Attempting to send the response lines: %v", redisSession.ResponseLines)
	for _, message := range redisSession.ResponseLines {
		err := conn.WriteMessage(websocket.TextMessage, []byte(message))
		if err != nil {
			log.Printf("Error sending stored message: %v", err)
		}
	}

	conn.SetPingHandler(func(appData string) error {
		log.Printf("Received ping: %s", appData)
		return conn.WriteMessage(websocket.PongMessage, []byte(appData))
	})

	subscribeToUpdates(sessionID, conn)
	return
}

func subscribeToUpdates(sessionID string, conn *websocket.Conn) {

	pubsub := util.GetRedisClient().Subscribe(util.GetContext(), sessionID)
	defer pubsub.Close()

	for {
		msg, err := pubsub.ReceiveMessage(util.GetContext())
		if err != nil {
			log.Printf("Error receiving message: %v", err)
			errMsg := fmt.Sprintf("Error: %v", err)
			writeErr := conn.WriteMessage(websocket.TextMessage, []byte(errMsg))
			if writeErr != nil {
				log.Printf("Error sending error message to WebSocket: %v", writeErr)
				continue
			}
			continue
		}

		log.Printf("Received message from the pubsub: %v", msg)
		err = conn.WriteMessage(websocket.TextMessage, []byte(msg.Payload))
		if err != nil {
			log.Printf("Error sending message: %v", err)
			errMsg := fmt.Sprintf("Error: %v", err)
			writeErr := conn.WriteMessage(websocket.TextMessage, []byte(errMsg))
			if writeErr != nil {
				log.Printf("Error sending error message to WebSocket: %v", writeErr)
			}
		}
	}
}

func StreamServiceLogs(w http.ResponseWriter, r *http.Request) {
	enclaveIdentifier := r.URL.Query().Get("enclaveIdentifier")
	serviceName := r.URL.Query().Get("serviceName")

	if enclaveIdentifier == "" || serviceName == "" {
		http.Error(w, "Missing enclaveIdentifier or serviceName query parameter", http.StatusBadRequest)
		return
	}

	conn, err := logUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade to WebSocket: %v", err)
		return
	}
	defer conn.Close()

	// Function to handle the log entries received from Redis and send them to the WebSocket client
	handleLog := func(logEntry string) {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(logEntry)); err != nil {
			log.Printf("Error sending log line: %v", err)
			conn.Close()
			return
		}
	}

	// Subscribe to the Redis logs channel
	err = util.SubscribeToLogs(enclaveIdentifier, serviceName, handleLog)
	if err != nil {
		log.Printf("Failed to subscribe to logs: %v", err)
		http.Error(w, "Failed to subscribe to logs", http.StatusInternalServerError)
		return
	}

	// Wait for client disconnect
	disconnect := make(chan struct{})
	go func() {
		defer close(disconnect)
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				// Client has disconnected or an error occurred
				log.Printf("Client disconnected: %v", err)
				return
			}
		}
	}()

	// Block until the client disconnects
	<-disconnect
	log.Printf("Closing WebSocket connection for service: %s", serviceName)
}
