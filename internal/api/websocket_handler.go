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
