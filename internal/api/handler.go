package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/kurtosis_core_rpc_api_bindings"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/lib/starlark_run_config"
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/lib/kurtosis_context"
	"log"
	"net/http"
	"sync"
	"time"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type RunPackageMessage struct {
	PackageURL string                 `json:"package_url"`
	Params     map[string]interface{} `json:"params"`
}

type ExecCommandRequest struct {
	EnclaveIdentifier string   `json:"enclaveIdentifier"`
	ServiceName       string   `json:"serviceName"`
	Command           []string `json:"command"`
}

type RedisSession struct {
	ResponseLines []string
}

type Session struct {
	Conn       *websocket.Conn
	CancelFunc context.CancelFunc
}

var (
	sessionsMu sync.Mutex
	sessions   = make(map[string]*Session)
)

var ctx = context.Background()
var redisClient *redis.Client

func init() {
	redisClient = redis.NewClient(&redis.Options{
		Addr:         "lzero-api-redis-g0loij.serverless.euw1.cache.amazonaws.com:6379", // Replace with your Redis endpoint
		Password:     "",                                                                // No password set
		DB:           0,                                                                 // Use default DB
		DialTimeout:  10 * time.Second,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		TLSConfig: &tls.Config{
			InsecureSkipVerify: true, // Skip certificate verification if needed
		},
	})
}

func storeSession(sessionID string, session *RedisSession) error {
	sessionData, err := json.Marshal(session)
	if err != nil {
		return err
	}
	err = redisClient.Set(ctx, sessionID, sessionData, 0).Err()
	return err
}

func getSession(sessionID string) (*RedisSession, error) {
	sessionData, err := redisClient.Get(ctx, sessionID).Result()
	if err != nil {
		return nil, err
	}
	var session RedisSession
	err = json.Unmarshal([]byte(sessionData), &session)
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func HandleRoot(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "Welcome to the Kurtosis API Server")
}

func RunPackage(w http.ResponseWriter, r *http.Request) {
	enclaveName := r.URL.Query().Get("enclaveName")
	sessionID := r.URL.Query().Get("sessionID")

	log.Printf("Run Package called: %s", sessionID)
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade to WebSocket: %v", err)
		return
	}
	defer conn.Close()

	sessionsMu.Lock()
	session, exists := sessions[sessionID]
	if exists {
		session.Conn = conn
		log.Printf("Re-attaching WebSocket connection for session ID: %s", sessionID)

		redisSession, err := getSession(sessionID)
		if err != nil {
			log.Printf("Error retrieving session from Redis: %v", err)
			sessionsMu.Unlock()
			return
		}

		log.Printf("Attempting to send the response lines: %v", redisSession.ResponseLines)
		for _, message := range redisSession.ResponseLines {
			err := conn.WriteMessage(websocket.TextMessage, []byte(message))
			if err != nil {
				log.Printf("Error sending stored message: %v", err)
			}
		}
		sessionsMu.Unlock()
		subscribeToUpdates(sessionID, conn)
		return
	}
	sessionsMu.Unlock()

	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	_, message, err := conn.ReadMessage()
	if err != nil {
		log.Printf("Error reading message: %v", err)
		return
	}

	var runPackageMessage RunPackageMessage
	err = json.Unmarshal(message, &runPackageMessage)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Invalid message format: "+err.Error()))
		return
	}

	paramsJSON, err := json.Marshal(runPackageMessage.Params)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to serialize parameters: "+err.Error()))
		return
	}

	kurtosisCtx, err := kurtosis_context.NewKurtosisContextFromLocalEngine()
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to create Kurtosis context: "+err.Error()))
		return
	}

	enclaveCtx, err := kurtosisCtx.CreateEnclave(context.Background(), enclaveName)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to create enclave: "+err.Error()))
		return
	}

	starlarkRunOptions := starlark_run_config.WithSerializedParams(string(paramsJSON))
	starlarkRunConfig := starlark_run_config.NewRunStarlarkConfig(starlarkRunOptions)

	responseLines, cancelFunc, err := enclaveCtx.RunStarlarkRemotePackage(r.Context(), runPackageMessage.PackageURL, starlarkRunConfig)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to run Starlark package: "+err.Error()))
		return
	}
	defer cancelFunc()

	sessionsMu.Lock()
	sessions[sessionID] = &Session{
		Conn:       conn,
		CancelFunc: cancelFunc,
	}
	sessionsMu.Unlock()

	newRedisSession := &RedisSession{
		ResponseLines: []string{},
	}
	err = storeSession(sessionID, newRedisSession)
	if err != nil {
		log.Printf("Error storing session: %v", err)
	}

	for line := range responseLines {
		if line == nil {
			continue
		}

		var outputJSON []byte
		switch detail := line.RunResponseLine.(type) {
		case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_ProgressInfo:
			progress := detail.ProgressInfo
			if len(progress.CurrentStepInfo) > 0 {
				output := map[string]interface{}{
					"info":         progress.CurrentStepInfo[0],
					"current_step": progress.CurrentStepNumber,
					"total_steps":  progress.TotalSteps,
				}
				outputJSON, err = json.Marshal(output)
			}
		case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_InstructionResult:
			result := detail.InstructionResult
			output := map[string]interface{}{
				"info": result.SerializedInstructionResult,
			}
			outputJSON, err = json.Marshal(output)
		case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_Error:
			log.Printf("Error during Starlark execution: %v", detail.Error)
			outputJSON = []byte("Error: " + detail.Error.String())
		case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_Warning:
			log.Printf("Warning: %s", detail.Warning.WarningMessage)
			continue
		case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_RunFinishedEvent:
			if detail.RunFinishedEvent.IsRunSuccessful {
				output := map[string]interface{}{
					"info": "Network run successfully",
				}
				outputJSON, err = json.Marshal(output)
			} else {
				log.Println("Starlark script failed to complete.")
			}
		case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_Info:
			continue
		case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_Instruction:
			continue
		default:
			log.Printf("Received unexpected type in response line: %T", detail)
			continue
		}

		if err != nil {
			log.Printf("Error marshaling output: %v", err)
			continue
		}

		conn.WriteMessage(websocket.TextMessage, outputJSON)
		newRedisSession.ResponseLines = append(newRedisSession.ResponseLines, string(outputJSON))

		err = storeSession(sessionID, newRedisSession)
		if err != nil {
			log.Printf("Error storing session: %v", err)
		}

		// Publish the new response line to the Redis channel
		redisClient.Publish(ctx, sessionID, string(outputJSON))
	}
}

func subscribeToUpdates(sessionID string, conn *websocket.Conn) {
	pubsub := redisClient.Subscribe(ctx, sessionID)
	defer pubsub.Close()

	for {
		msg, err := pubsub.ReceiveMessage(ctx)
		if err != nil {
			log.Printf("Error receiving message: %v", err)
			if err == redis.Nil {
				continue
			}
			return
		}

		log.Printf("Received message from the pubsub: %v", msg)
		err = conn.WriteMessage(websocket.TextMessage, []byte(msg.Payload))
		if err != nil {
			log.Printf("Error sending message: %v", err)
			return
		}
	}
}

func StopPackage(w http.ResponseWriter, r *http.Request) {
	// Extract enclaveIdentifier from query parameters
	enclaveIdentifier := r.URL.Query().Get("enclaveIdentifier")
	if enclaveIdentifier == "" {
		http.Error(w, "Missing enclaveIdentifier query parameter", http.StatusBadRequest)
		return
	}

	// Initialize the Kurtosis context
	kurtosisCtx, err := kurtosis_context.NewKurtosisContextFromLocalEngine()
	if err != nil {
		http.Error(w, "Failed to create Kurtosis context: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Destroy the enclave
	err = kurtosisCtx.DestroyEnclave(context.Background(), enclaveIdentifier)
	if err != nil {
		http.Error(w, "Failed to destroy enclave: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write([]byte("Enclave destroyed successfully"))
}

func GetServicesInfo(w http.ResponseWriter, r *http.Request) {
	// Extract enclaveIdentifier from query parameters
	enclaveIdentifier := r.URL.Query().Get("enclaveIdentifier")

	if enclaveIdentifier == "" {
		http.Error(w, "Missing enclaveIdentifier query parameter", http.StatusBadRequest)
		return
	}

	// Initialize the Kurtosis context
	kurtosisCtx, err := kurtosis_context.NewKurtosisContextFromLocalEngine()
	if err != nil {
		http.Error(w, "Failed to create Kurtosis context: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Get the EnclaveContext
	enclaveCtx, err := kurtosisCtx.GetEnclaveContext(context.Background(), enclaveIdentifier)
	if err != nil {
		http.Error(w, "Failed to get EnclaveContext: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Get the service identifiers
	serviceIdentifiers, err := enclaveCtx.GetServices()
	if err != nil {
		http.Error(w, "Failed to get services: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Convert service identifiers to the format needed by GetServiceContexts
	serviceIdentifiersMap := make(map[string]bool)
	for serviceName := range serviceIdentifiers {
		serviceIdentifiersMap[string(serviceName)] = true
	}

	// Get the detailed service contexts
	serviceContexts, err := enclaveCtx.GetServiceContexts(serviceIdentifiersMap)
	if err != nil {
		http.Error(w, "Failed to get service contexts: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Construct the detailed information
	servicesInfo := make(map[string]interface{})
	for _, serviceContext := range serviceContexts {
		serviceInfo := map[string]interface{}{
			"service_uuid":       serviceContext.GetServiceUUID(),
			"private_ip_address": serviceContext.GetPrivateIPAddress(),
			"private_ports":      serviceContext.GetPrivatePorts(),
			"public_ip_address":  serviceContext.GetMaybePublicIPAddress(),
			"public_ports":       serviceContext.GetPublicPorts(),
		}
		servicesInfo[string(serviceContext.GetServiceName())] = serviceInfo
	}

	// Serialize the detailed information to JSON
	servicesInfoJSON, err := json.Marshal(servicesInfo)
	if err != nil {
		http.Error(w, "Failed to serialize services information: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Set CORS headers and return the response
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	w.Write(servicesInfoJSON)
}

func ExecServiceCommand(w http.ResponseWriter, r *http.Request) {
	var req ExecCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Initialize the Kurtosis context
	kurtosisCtx, err := kurtosis_context.NewKurtosisContextFromLocalEngine()
	if err != nil {
		http.Error(w, "Failed to create Kurtosis context: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Get the EnclaveContext
	enclaveCtx, err := kurtosisCtx.GetEnclaveContext(context.Background(), req.EnclaveIdentifier)
	if err != nil {
		http.Error(w, "Failed to get EnclaveContext: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Get the ServiceContext
	serviceCtx, err := enclaveCtx.GetServiceContext(req.ServiceName)
	if err != nil {
		http.Error(w, "Failed to get ServiceContext: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Execute the command
	exitCode, logs, err := serviceCtx.ExecCommand(req.Command)
	if err != nil {
		http.Error(w, "Failed to execute command: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Construct the response
	response := map[string]interface{}{
		"exit_code": exitCode,
		"logs":      logs,
	}

	// Serialize the response to JSON
	responseJSON, err := json.Marshal(response)
	if err != nil {
		http.Error(w, "Failed to serialize response: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Set CORS headers and return the response
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	w.Write(responseJSON)
}
