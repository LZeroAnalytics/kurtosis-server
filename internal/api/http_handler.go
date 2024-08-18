package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/kurtosis_core_rpc_api_bindings"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/lib/services"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/lib/starlark_run_config"
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/lib/kurtosis_context"
	"kurtosis-server/internal/api/util"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type RunPackageMessage struct {
	PackageURL      string                 `json:"package_url"`
	Params          map[string]interface{} `json:"params"`
	ServiceMappings []ServiceMapping       `json:"service_mappings"`
}

type ServiceMapping struct {
	ServiceName string `json:"service_name"`
	Ports       []Port `json:"ports"`
}

type ExecCommandRequest struct {
	EnclaveIdentifier string   `json:"enclaveIdentifier"`
	ServiceName       string   `json:"serviceName"`
	Command           []string `json:"command"`
}

func HandleRoot(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "Welcome to the Kurtosis API Server")
}

func StartNetwork(w http.ResponseWriter, r *http.Request) {
	enclaveName := r.URL.Query().Get("enclaveName")
	sessionID := r.URL.Query().Get("sessionID")

	if sessionID == "" {
		http.Error(w, "Session ID missing", http.StatusBadRequest)
		return
	}

	newRedisSession := &util.RedisSession{
		ResponseLines: []string{},
	}
	err := util.StoreSession(sessionID, newRedisSession)
	if err != nil {
		http.Error(w, "Error storing session: "+err.Error(), http.StatusInternalServerError)
		deletionDate := time.Now().Format(time.RFC3339)
		util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate)
		if err != nil {
			log.Printf("Failed to update network status: %v", err)
		}
		return
	}

	// Read message from request body
	var runPackageMessage RunPackageMessage
	err = json.NewDecoder(r.Body).Decode(&runPackageMessage)
	if err != nil {
		deletionDate := time.Now().Format(time.RFC3339)
		util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate)
		http.Error(w, "Invalid message format: "+err.Error(), http.StatusBadRequest)
		return
	}

	paramsJSON, err := json.Marshal(runPackageMessage.Params)
	if err != nil {
		deletionDate := time.Now().Format(time.RFC3339)
		util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate)
		http.Error(w, "Failed to serialize parameters: "+err.Error(), http.StatusInternalServerError)
		return
	}

	authorizationHeader := r.Header.Get("Authorization")

	if authorizationHeader == "" {
		deletionDate := time.Now().Format(time.RFC3339)
		util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate)
		http.Error(w, "Authorization header missing", http.StatusUnauthorized)
		return
	}

	log.Printf("Got authorization header: %v", authorizationHeader)

	hasBillings, err := util.CheckUserBilling(authorizationHeader)
	if err != nil {
		deletionDate := time.Now().Format(time.RFC3339)
		util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate)
		http.Error(w, "Error checking billings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if !hasBillings {
		deletionDate := time.Now().Format(time.RFC3339)
		util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate)
		http.Error(w, "User does not have billing enabled", http.StatusUnauthorized)
		return
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Recovered from panic in goroutine: %v", r)
			}
			log.Printf("Goroutine for session ID %s has finished execution.", sessionID)
		}()

		log.Printf("Goroutine for session ID %s has started.", sessionID)

		bgContext := context.Background()

		kurtosisCtx, err := kurtosis_context.NewKurtosisContextFromLocalEngine()
		if err != nil {
			deletionDate := time.Now().Format(time.RFC3339)
			util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate)
			http.Error(w, "Failed to create Kurtosis context: "+err.Error(), http.StatusInternalServerError)
			return
		}

		status, err := util.GetNetworkStatus(enclaveName)
		if err != nil {
			log.Printf("Failed to retrieve network status: %v", err)
			return
		}
		// Handle error because of user termination
		if status == "Terminated" {
			return
		}

		enclaveCtx, err := kurtosisCtx.CreateEnclave(bgContext, enclaveName)
		if err != nil {
			deletionDate := time.Now().Format(time.RFC3339)
			util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate)
			http.Error(w, "Failed to create enclave: "+err.Error(), http.StatusInternalServerError)
			return
		}

		status, err = util.GetNetworkStatus(enclaveName)
		if err != nil {
			log.Printf("Failed to retrieve network status: %v", err)
			return
		}
		// Handle error because of user termination
		if status == "Terminated" {
			return
		}

		// Create ingresses for services
		for _, serviceMapping := range runPackageMessage.ServiceMappings {
			ingressData := IngressData{
				ServiceName: serviceMapping.ServiceName,
				SessionID:   sessionID[:18],
				Namespace:   "kt-" + enclaveName,
				Ports:       serviceMapping.Ports,
			}

			if err := createIngress(ingressData); err != nil {
				deletionDate := time.Now().Format(time.RFC3339)
				util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate)
				kurtosisCtx.DestroyEnclave(bgContext, enclaveName)
				log.Printf("Failed to create ingress for service %s: %v", serviceMapping.ServiceName, err)
			}
		}

		status, err = util.GetNetworkStatus(enclaveName)
		if err != nil {
			log.Printf("Failed to retrieve network status: %v", err)
			return
		}
		// Handle error because of user termination
		if status == "Terminated" {
			return
		}

		starlarkRunOptions := starlark_run_config.WithSerializedParams(string(paramsJSON))
		starlarkRunConfig := starlark_run_config.NewRunStarlarkConfig(starlarkRunOptions)

		responseLines, _, err := enclaveCtx.RunStarlarkRemotePackage(bgContext, runPackageMessage.PackageURL, starlarkRunConfig)
		if err != nil {
			deletionDate := time.Now().Format(time.RFC3339)
			util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate)
			kurtosisCtx.DestroyEnclave(bgContext, enclaveName)
			log.Printf("Failed to run Starlark package for session ID %s: %v", sessionID, err)
			return
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
						"type":         "progress",
					}
					outputJSON, err = json.Marshal(output)
				}
			case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_Instruction:
				instruction := detail.Instruction
				output := map[string]interface{}{
					"name":        instruction.InstructionName,
					"instruction": instruction.ExecutableInstruction,
					"arguments":   instruction.Arguments,
					"type":        "instruction",
				}
				outputJSON, err = json.Marshal(output)
			case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_InstructionResult:
				result := detail.InstructionResult
				output := map[string]interface{}{
					"info": result.SerializedInstructionResult,
					"type": "result",
				}
				outputJSON, err = json.Marshal(output)
			case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_Error:
				log.Printf("Error during Starlark execution: %v", detail.Error)
				status, err := util.GetNetworkStatus(enclaveName)
				if err != nil {
					log.Printf("Failed to retrieve network status: %v", err)
					return
				}
				// Handle error because of user termination
				if status == "Terminated" {
					return
				}
				outputJSON = []byte("Error: " + detail.Error.String())
				deletionDate := time.Now().Format(time.RFC3339)
				err = util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate)
				if err != nil {
					log.Printf("Failed to update network status: %v", err)
				}
				if err := kurtosisCtx.DestroyEnclave(bgContext, enclaveName); err != nil {
					log.Printf("Failed to destroy enclave after error: %v", err)
				}

			case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_Warning:
				log.Printf("Warning: %s", detail.Warning.WarningMessage)
				continue
			case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_RunFinishedEvent:
				if detail.RunFinishedEvent.IsRunSuccessful {
					output := map[string]interface{}{
						"info": "Network run successfully",
						"type": "progress",
					}
					outputJSON, err = json.Marshal(output)
					err = util.UpdateNetworkStatus(enclaveName, "Operational", nil)
					if err != nil {
						log.Printf("Failed to update network status: %v", err)
					}
				} else {
					log.Println("Starlark script failed to complete.")
				}
			case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_Info:
				info := detail.Info.GetInfoMessage()
				outputJSON, err = json.Marshal(map[string]interface{}{
					"info": info,
					"type": "log",
				})
			default:
				log.Printf("Received unexpected type in response line: %T", detail)
				continue
			}

			if err != nil {
				log.Printf("Error marshaling output: %v", err)
				continue
			}

			newRedisSession.ResponseLines = append(newRedisSession.ResponseLines, string(outputJSON))

			err = util.StoreSession(sessionID, newRedisSession)
			if err != nil {
				deletionDate := time.Now().Format(time.RFC3339)
				err = util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate)
				kurtosisCtx.DestroyEnclave(bgContext, enclaveName)
				log.Printf("Error storing session: %v", err)
			}

			// Publish the new response line to the Redis channel
			util.GetRedisClient().Publish(util.GetContext(), sessionID, string(outputJSON))

			// Add LoadBalancer creation logic here
			var payload map[string]interface{}
			if err := json.Unmarshal(outputJSON, &payload); err != nil {
				log.Printf("Failed to unmarshal outputJSON: %v", err)
				continue
			}

			info, ok := payload["info"].(string)
			log.Printf("Info: %s is ok: %v", info, ok)
			if !ok {
				continue
			}
		}
	}()

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Network initiated successfully with session ID: " + sessionID))
}

func StopNetwork(w http.ResponseWriter, r *http.Request) {
	// Extract enclaveIdentifier from query parameters
	enclaveIdentifier := r.URL.Query().Get("enclaveIdentifier")
	if enclaveIdentifier == "" {
		http.Error(w, "Missing enclaveIdentifier query parameter", http.StatusBadRequest)
		return
	}

	log.Printf("Trying to stop the network: %v", enclaveIdentifier)

	err := CheckOwnership(r, enclaveIdentifier)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	// Get the current network status
	status, err := util.GetNetworkStatus(enclaveIdentifier)
	if err != nil {
		http.Error(w, "Failed to get network status: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if status == "Error" {
		log.Printf("Network %s is in Error state. Skipping enclave deletion.", enclaveIdentifier)

		// Update the network status to Terminated
		err = util.UpdateNetworkStatus(enclaveIdentifier, "Terminated", nil)
		if err != nil {
			http.Error(w, "Failed to update network status: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// Remove the corresponding Redis session
		util.GetRedisClient().Del(util.GetContext(), enclaveIdentifier)

		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Write([]byte("Enclave was already in Error state and has now been marked as Terminated."))
		return
	}

	// Update the network status to Terminated
	deletionDate := time.Now().Format(time.RFC3339)
	err = util.UpdateNetworkStatus(enclaveIdentifier, "Terminated", &deletionDate)
	if err != nil {
		http.Error(w, "Failed to update network status: "+err.Error(), http.StatusInternalServerError)
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

	// Remove the corresponding Redis session
	util.GetRedisClient().Del(util.GetContext(), enclaveIdentifier)

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write([]byte("Enclave destroyed successfully and network marked as Terminated."))
}

func GetServicesInfo(w http.ResponseWriter, r *http.Request) {
	// Extract enclaveIdentifier from query parameters
	enclaveIdentifier := r.URL.Query().Get("enclaveIdentifier")

	if enclaveIdentifier == "" {
		http.Error(w, "Missing enclaveIdentifier query parameter", http.StatusBadRequest)
		return
	}

	err := CheckOwnership(r, enclaveIdentifier)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
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
		privatePorts := make(map[string]Port)
		for portName, portSpec := range serviceContext.GetPrivatePorts() {
			privatePorts[portName] = Port{
				Port:     int32(portSpec.GetNumber()),
				PortName: portName,
			}
		}

		publicPorts := make(map[string]Port)
		for portName, portSpec := range serviceContext.GetPublicPorts() {
			publicPorts[portName] = Port{
				Port:     int32(portSpec.GetNumber()),
				PortName: portName,
			}
		}

		serviceInfo := map[string]interface{}{
			"service_uuid":       serviceContext.GetServiceUUID(),
			"private_ip_address": serviceContext.GetPrivateIPAddress(),
			"private_ports":      privatePorts,
			"public_ip_address":  serviceContext.GetMaybePublicIPAddress(),
			"public_ports":       publicPorts,
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

	err := CheckOwnership(r, req.EnclaveIdentifier)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
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

func CheckOwnership(r *http.Request, networkID string) error {
	authorizationHeader := r.Header.Get("Authorization")

	if authorizationHeader == "" {
		return fmt.Errorf("authorization header missing")
	}

	parts := strings.Split(authorizationHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		return fmt.Errorf("invalid authorization header format")
	}

	accessToken := parts[1]

	// Retrieve and verify the user ID
	userID, err := util.GetUserIDFromToken(accessToken)
	if err != nil {
		return fmt.Errorf("invalid token: %v", err)
	}

	// Check if the user is the owner of the network
	isOwner, err := util.IsNetworkOwner(networkID, userID)
	if err != nil {
		return fmt.Errorf("error checking network ownership: %v", err)
	}

	if !isOwner {
		return fmt.Errorf("unauthorized: user is not the owner of the network")
	}

	return nil
}

func GetServiceLogsBatch(w http.ResponseWriter, r *http.Request) {
	enclaveIdentifier := r.URL.Query().Get("enclaveIdentifier")
	serviceName := r.URL.Query().Get("serviceName")
	limit := r.URL.Query().Get("limit") // Number of log lines to fetch

	if enclaveIdentifier == "" || serviceName == "" || limit == "" {
		http.Error(w, "Missing parameters", http.StatusBadRequest)
		return
	}

	numLogLines, err := strconv.ParseUint(limit, 10, 32)
	if err != nil || numLogLines == 0 {
		http.Error(w, "Invalid limit parameter", http.StatusBadRequest)
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

	// Get the service UUID for the given service name
	serviceCtx, err := enclaveCtx.GetServiceContext(serviceName)
	if err != nil {
		log.Printf("Failed to get Service UUID for service name '%s': %v", serviceName, err)
		http.Error(w, "Failed to get Service UUID: "+err.Error(), http.StatusInternalServerError)
		return
	}
	serviceUUID := serviceCtx.GetServiceUUID()

	logLineFilter := kurtosis_context.NewDoesContainTextLogLineFilter("")

	logStream, cleanupFunc, err := kurtosisCtx.GetServiceLogs(context.Background(), enclaveIdentifier, map[services.ServiceUUID]bool{serviceUUID: true}, false, false, uint32(numLogLines), logLineFilter)
	if err != nil {
		log.Printf("Failed to get service logs: %v", err)
		http.Error(w, "Failed to get service logs", http.StatusInternalServerError)
		return
	}
	defer cleanupFunc()

	var logs []string
	for logContent := range logStream {
		for _, logLine := range logContent.GetServiceLogsByServiceUuids()[serviceUUID] {
			logs = append(logs, logLine.GetContent())
		}
	}

	jsonResponse, err := json.Marshal(logs)
	if err != nil {
		log.Printf("Failed to marshal logs: %v", err)
		http.Error(w, "Failed to process logs", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(jsonResponse)
}
