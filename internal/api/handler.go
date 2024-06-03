package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/kurtosis_core_rpc_api_bindings"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/lib/starlark_run_config"
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/lib/kurtosis_context"
	"log"
	"net/http"
	"sync"
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

type Session struct {
	Conn          *websocket.Conn
	ResponseLines []string
	CancelFunc    context.CancelFunc
}

var (
	sessions   = make(map[string]*Session)
	sessionsMu sync.Mutex
)

func HandleRoot(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "Welcome to the Kurtosis API Server")
}

func RunPackage(w http.ResponseWriter, r *http.Request) {
	// Extract enclaveName and sessionID from query parameters
	enclaveName := r.URL.Query().Get("enclaveName")
	sessionID := r.URL.Query().Get("sessionID")

	// Upgrade HTTP connection to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade to WebSocket: %v", err)
		return
	}
	defer conn.Close()

	// Handle reconnection logic
	sessionsMu.Lock()
	if session, exists := sessions[sessionID]; exists {
		// Existing session found, re-attach the WebSocket connection
		session.Conn = conn
		for _, message := range session.ResponseLines {
			conn.WriteMessage(websocket.TextMessage, []byte(message))
		}
		sessionsMu.Unlock()
		return
	}
	sessionsMu.Unlock()

	// Generate a new session ID if not provided
	if sessionID == "" {
		sessionID = uuid.New().String()
	}

	// Read the initial message containing the package URL and parameters
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

	// Serialize the Params to JSON
	paramsJSON, err := json.Marshal(runPackageMessage.Params)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to serialize parameters: "+err.Error()))
		return
	}

	// Initialize the Kurtosis context
	kurtosisCtx, err := kurtosis_context.NewKurtosisContextFromLocalEngine()
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to create Kurtosis context: "+err.Error()))
		return
	}

	// Create an enclave
	enclaveCtx, err := kurtosisCtx.CreateEnclave(context.Background(), enclaveName)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to create enclave: "+err.Error()))
		return
	}

	// Define the StarlarkRunConfig with parameters
	starlarkRunOptions := starlark_run_config.WithSerializedParams(string(paramsJSON))
	starlarkRunConfig := starlark_run_config.NewRunStarlarkConfig(starlarkRunOptions)

	// Run the Starlark package
	responseLines, cancelFunc, err := enclaveCtx.RunStarlarkRemotePackage(r.Context(), runPackageMessage.PackageURL, starlarkRunConfig)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to run Starlark package: "+err.Error()))
		return
	}
	defer cancelFunc()

	// Store the session
	sessionsMu.Lock()
	sessions[sessionID] = &Session{
		Conn:          conn,
		ResponseLines: []string{},
		CancelFunc:    cancelFunc,
	}
	sessionsMu.Unlock()

	// Stream response lines
	for line := range responseLines {
		if line == nil {
			continue
		}

		var outputJSON []byte
		switch detail := line.RunResponseLine.(type) {
		case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_ProgressInfo:
			// Handle ProgressInfo
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
			// Handle InstructionResult
			result := detail.InstructionResult
			output := map[string]interface{}{
				"info": result.SerializedInstructionResult,
			}
			outputJSON, err = json.Marshal(output)
		case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_Error:
			// Handle Error
			log.Printf("Error during Starlark execution: %v", detail.Error)
			outputJSON = []byte("Error: " + detail.Error.String())
		case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_Warning:
			// Handle Warning
			log.Printf("Warning: %s", detail.Warning.WarningMessage)
			continue
		case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_RunFinishedEvent:
			// Handle RunFinishedEvent
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
			// Handle unexpected type
			log.Printf("Received unexpected type in response line: %T", detail)
			continue
		}

		if err != nil {
			log.Printf("Error marshaling output: %v", err)
			continue
		}

		// Send the message and store it in the session
		conn.WriteMessage(websocket.TextMessage, outputJSON)
		sessionsMu.Lock()
		sessions[sessionID].ResponseLines = append(sessions[sessionID].ResponseLines, string(outputJSON))
		sessionsMu.Unlock()
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
