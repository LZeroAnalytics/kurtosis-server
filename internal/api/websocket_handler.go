package api

import (
	"context"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/lib/services"
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/lib/kurtosis_context"
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

	// Initialize Kurtosis context
	kurtosisCtx, err := kurtosis_context.NewKurtosisContextFromLocalEngine()
	if err != nil {
		log.Printf("Failed to create Kurtosis context: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to create Kurtosis context"))
		return
	}

	// Get the EnclaveContext
	enclaveCtx, err := kurtosisCtx.GetEnclaveContext(context.Background(), enclaveIdentifier)
	if err != nil {
		log.Printf("Failed to get EnclaveContext: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to get EnclaveContext"))
		return
	}

	// Get the service UUID for the given service name
	serviceCtx, err := enclaveCtx.GetServiceContext(serviceName)
	if err != nil {
		log.Printf("Failed to get Service UUID for service name '%s': %v", serviceName, err)
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to get Service for service name"))
		return
	}
	serviceUUID := serviceCtx.GetServiceUUID()

	// Create a log line filter (empty to get all logs)
	logLineFilter := kurtosis_context.NewDoesContainTextLogLineFilter("")

	logStream, cleanupFunc, err := kurtosisCtx.GetServiceLogs(context.Background(), enclaveIdentifier, map[services.ServiceUUID]bool{serviceUUID: true}, true, true, 100, logLineFilter)
	if err != nil {
		log.Printf("Failed to get service logs: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to get service logs"))
		return
	}
	defer cleanupFunc()

	// Stream logs to WebSocket
	for logContent := range logStream {
		for _, logLine := range logContent.GetServiceLogsByServiceUuids()[serviceUUID] {
			logLineContent := logLine.GetContent()
			if err := conn.WriteMessage(websocket.TextMessage, []byte(logLineContent)); err != nil {
				log.Printf("Error sending log line: %v", err)

				conn.Close()
				return
			}
		}

		// Check if the service was not found
		if _, notFound := logContent.GetNotFoundServiceUuids()[serviceUUID]; notFound {
			conn.WriteMessage(websocket.TextMessage, []byte("Service UUID not found or no logs available"))
			return
		}
	}
	conn.Close()
}
