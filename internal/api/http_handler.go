package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/kurtosis_core_rpc_api_bindings"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/lib/enclaves"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/lib/services"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/lib/starlark_run_config"
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/lib/kurtosis_context"
	"kurtosis-server/internal/api/util"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
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

type CachedContexts struct {
	EnclaveContexts map[string]*enclaves.EnclaveContext
	ServiceContexts map[string]*services.ServiceContext
	Mutex           sync.Mutex
}

var (
	cachedKurtosisCtx     *kurtosis_context.KurtosisContext
	cachedKurtosisCtxOnce sync.Once
	cachedContexts        = CachedContexts{
		EnclaveContexts: make(map[string]*enclaves.EnclaveContext),
		ServiceContexts: make(map[string]*services.ServiceContext),
	}
)

func getKurtosisContext() (*kurtosis_context.KurtosisContext, error) {
	var err error
	cachedKurtosisCtxOnce.Do(func() {
		cachedKurtosisCtx, err = kurtosis_context.NewKurtosisContextFromLocalEngine()
	})
	return cachedKurtosisCtx, err
}

func getCachedEnclaveContext(enclaveIdentifier string) (*enclaves.EnclaveContext, error) {

	if ctx, exists := cachedContexts.EnclaveContexts[enclaveIdentifier]; exists {
		return ctx, nil
	}

	kurtosisCtx, err := getKurtosisContext()
	if err != nil {
		return nil, err
	}

	enclaveCtx, err := kurtosisCtx.GetEnclaveContext(context.Background(), enclaveIdentifier)
	if err != nil {
		return nil, err
	}

	cachedContexts.EnclaveContexts[enclaveIdentifier] = enclaveCtx
	return enclaveCtx, nil
}

func getCachedServiceContext(enclaveIdentifier, serviceName string) (*services.ServiceContext, error) {
	key := fmt.Sprintf("%s:%s", enclaveIdentifier, serviceName)

	cachedContexts.Mutex.Lock()
	serviceCtx, exists := cachedContexts.ServiceContexts[key]
	cachedContexts.Mutex.Unlock()

	if exists {
		return serviceCtx, nil
	}

	enclaveCtx, err := getCachedEnclaveContext(enclaveIdentifier)
	if err != nil {
		return nil, err
	}

	serviceCtx, err = enclaveCtx.GetServiceContext(serviceName)
	if err != nil {
		return nil, err
	}

	cachedContexts.Mutex.Lock()
	cachedContexts.ServiceContexts[key] = serviceCtx
	cachedContexts.Mutex.Unlock()
	return serviceCtx, nil
}

func HandleRoot(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "Welcome to the Kurtosis API Server")
}

func StartNetwork(w http.ResponseWriter, r *http.Request) {
	enclaveName := r.URL.Query().Get("enclaveName")
	sessionID := r.URL.Query().Get("sessionID")
	demo := r.URL.Query().Get("demo")

	isDemoMode := false
	if demo == "true" {
		isDemoMode = true
	}

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
		util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate, isDemoMode)
		log.Printf("Failed to update network status: %v", err)
		return
	}

	// Read message from request body
	var runPackageMessage RunPackageMessage
	err = json.NewDecoder(r.Body).Decode(&runPackageMessage)
	if err != nil {
		deletionDate := time.Now().Format(time.RFC3339)
		util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate, isDemoMode)
		http.Error(w, "Invalid message format: "+err.Error(), http.StatusBadRequest)
		return
	}

	paramsJSON, err := json.Marshal(runPackageMessage.Params)
	if err != nil {
		deletionDate := time.Now().Format(time.RFC3339)
		util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate, isDemoMode)
		http.Error(w, "Failed to serialize parameters: "+err.Error(), http.StatusInternalServerError)
		return
	}

	authorizationHeader := r.Header.Get("Authorization")

	if authorizationHeader == "" {
		deletionDate := time.Now().Format(time.RFC3339)
		util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate, isDemoMode)
		http.Error(w, "Authorization header missing", http.StatusUnauthorized)
		return
	}

	log.Printf("Got authorization header: %v", authorizationHeader)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Recovered from panic in goroutine: %v", r)
			}
			log.Printf("Goroutine for session ID %s has finished execution.", sessionID)
		}()

		log.Printf("Goroutine for session ID %s has started.", sessionID)

		bgContext := context.Background()

		kurtosisCtx, err := getKurtosisContext()
		if err != nil {
			deletionDate := time.Now().Format(time.RFC3339)
			util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate, isDemoMode)
			http.Error(w, "Failed to create Kurtosis context: "+err.Error(), http.StatusInternalServerError)
			return
		}

		status, err := util.GetNetworkStatus(enclaveName, isDemoMode)
		if err != nil {
			log.Printf("Failed to retrieve network status: %v", err)
			return
		}
		// Handle error because of user termination
		if status == "Terminated" {
			return
		}

		enclaveCtx, err := kurtosisCtx.CreateProductionEnclave(bgContext, enclaveName)
		cachedContexts.Mutex.Lock()
		cachedContexts.EnclaveContexts[enclaveName] = enclaveCtx
		cachedContexts.Mutex.Unlock()
		if err != nil {
			deletionDate := time.Now().Format(time.RFC3339)
			util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate, isDemoMode)
			log.Printf("Failed to create enclave %v", err)
			http.Error(w, "Failed to create enclave: "+err.Error(), http.StatusInternalServerError)
			return
		}

		status, err = util.GetNetworkStatus(enclaveName, isDemoMode)
		if err != nil {
			log.Printf("Failed to retrieve network status: %v", err)
			return
		}
		// Handle error because of user termination
		if status == "Terminated" {
			return
		}

		// Create ingresses for services
		createIngresses(bgContext, kurtosisCtx, runPackageMessage, sessionID, enclaveName, isDemoMode)

		status, err = util.GetNetworkStatus(enclaveName, isDemoMode)
		if err != nil {
			log.Printf("Failed to retrieve network status: %v", err)
			return
		}
		// Handle error because of user termination
		if status == "Terminated" {
			return
		}

		log.Printf("Running starlark with the following params %v", string(paramsJSON))
		starlarkRunOptions := starlark_run_config.WithSerializedParams(string(paramsJSON))
		starlarkRunConfig := starlark_run_config.NewRunStarlarkConfig(starlarkRunOptions)

		responseLines, _, err := enclaveCtx.RunStarlarkRemotePackage(bgContext, runPackageMessage.PackageURL, starlarkRunConfig)
		if err != nil {
			deletionDate := time.Now().Format(time.RFC3339)
			util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate, isDemoMode)
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
				status, err := util.GetNetworkStatus(enclaveName, isDemoMode)
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
				err = util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate, isDemoMode)
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
					if status == "SubscriptionPending" {
						err = util.UpdateNetworkStatus(enclaveName, "SubscriptionOperational", nil, isDemoMode)
					} else {
						err = util.UpdateNetworkStatus(enclaveName, "Operational", nil, isDemoMode)
					}

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
				err = util.UpdateNetworkStatus(enclaveName, "Error", &deletionDate, isDemoMode)
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

		// Stream service logs to redis
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

		// Iterate over service contexts
		for _, serviceContext := range serviceContexts {
			serviceName := string(serviceContext.GetServiceName())
			key := fmt.Sprintf("%s:%s", enclaveName, serviceName)
			cachedContexts.Mutex.Lock()
			cachedContexts.ServiceContexts[key] = serviceContext
			cachedContexts.Mutex.Unlock()

			// Check if the service name contains "node"
			if !strings.Contains(serviceName, "node") &&
				!strings.Contains(serviceName, "el-") &&
				!strings.Contains(serviceName, "cl-") &&
				!strings.Contains(serviceName, "hermes") {
				continue
			}

			logLineFilter := kurtosis_context.NewDoesContainTextLogLineFilter("")
			// Get logs for the node service
			logStream, cleanupFunc, err := kurtosisCtx.GetServiceLogs(
				context.Background(),
				enclaveName,
				map[services.ServiceUUID]bool{serviceContext.GetServiceUUID(): true},
				true,          // Follow logs in real-time
				true,          // Do not return all logs, but only the latest ones
				100,           // Number of log lines to retrieve
				logLineFilter, // Use an empty log line filter for now
			)
			if err != nil {
				log.Printf("Failed to get service logs for service '%s': %v", serviceName, err)
				continue
			}

			// Stream logs into Redis
			go func(enclaveName string, serviceName string) {
				defer cleanupFunc()

				for logContent := range logStream {
					logEntries := logContent.GetServiceLogsByServiceUuids()
					for _, logLines := range logEntries {
						for _, logLine := range logLines {
							logLineContent := logLine.GetContent()

							// Store the log line in Redis
							err := util.StoreNodeLog(enclaveName, serviceName, logLineContent)
							if err != nil {
								log.Printf("Failed to store log for service '%s': %v", serviceName, err)
							}
						}
					}
				}
			}(enclaveName, serviceName)
		}
	}()

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Network initiated successfully with session ID: " + sessionID))
}

func StopNetwork(w http.ResponseWriter, r *http.Request) {
	// Extract enclaveIdentifier from query parameters
	enclaveIdentifier := r.URL.Query().Get("enclaveIdentifier")
	demo := r.URL.Query().Get("demo")

	isDemoMode := false

	if demo == "true" {
		isDemoMode = true
	}

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
	status, err := util.GetNetworkStatus(enclaveIdentifier, isDemoMode)
	if err != nil {
		log.Printf("Failed to get network status: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Determine the new status based on the current status
	newStatus := "Terminated"

	// Set the status to SubscriptionTerminated if it's in a subscription state
	if status == "SubscriptionPending" || status == "SubscriptionOperational" || status == "SubscriptionError" {
		newStatus = "SubscriptionTerminated"
	}

	if status == "Error" || status == "SubscriptionError" {
		log.Printf("Network %s is in Error state. Skipping enclave deletion.", enclaveIdentifier)

		// Update the network status to Terminated
		err = util.UpdateNetworkStatus(enclaveIdentifier, newStatus, nil, isDemoMode)
		if err != nil {
			log.Printf("Failed to update network status: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		// Update the network status to Terminated
		deletionDate := time.Now().Format(time.RFC3339)
		err = util.UpdateNetworkStatus(enclaveIdentifier, newStatus, &deletionDate, isDemoMode)
		if err != nil {
			log.Printf("Failed to update network status: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write([]byte("Network termination process started."))

	// Run the network stop process in a goroutine
	go func() {

		// Initialize the Kurtosis context
		kurtosisCtx, err := kurtosis_context.NewKurtosisContextFromLocalEngine()
		if err != nil {
			log.Printf("Failed to create Kurtosis context: %v", err)
			return
		}

		enclaveCtx, err := kurtosisCtx.GetEnclaveContext(context.Background(), enclaveIdentifier)
		if err != nil {
			log.Printf("Failed to get EnclaveContext: %v", err)
			return
		}

		// Get the service identifiers
		serviceIdentifiers, err := enclaveCtx.GetServices()
		if err != nil {
			log.Printf("Failed to get services: %v", err)
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
			log.Printf("Failed to get service contexts: %v", err)
			return
		}

		// Iterate over service contexts
		for _, serviceContext := range serviceContexts {
			serviceName := string(serviceContext.GetServiceName())
			// Check if the service name contains "node"
			if !strings.Contains(serviceName, "node") {
				continue
			}
			util.GetRedisClient().Del(util.GetContext(), "node-logs:"+serviceName+"-"+enclaveIdentifier)
			util.GetRedisClient().Del(util.GetContext(), "log-channel:"+serviceName+"-"+enclaveIdentifier)
		}

		// Remove the corresponding Redis session
		util.GetRedisClient().Del(util.GetContext(), enclaveIdentifier)

		if status != "Error" {
			// Destroy the enclave
			err = kurtosisCtx.DestroyEnclave(context.Background(), enclaveIdentifier)
			if err != nil {
				log.Printf("Failed to destroy enclave: %v", err)
				return
			}
		}

		log.Printf("Enclave destroyed successfully and network marked as Terminated.")
	}()
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

	// Get the EnclaveContext
	enclaveCtx, err := getCachedEnclaveContext(enclaveIdentifier)
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

	// Get the ServiceContext
	log.Printf("Checked ownership and getting service context for %v", req.ServiceName)
	serviceCtx, err := getCachedServiceContext(req.EnclaveIdentifier, req.ServiceName)
	log.Printf("Service context error: %v", err)
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
	predefinedAPIKey := "6a3bc0d4-d0fa-40f3-bb16-1a3a16fc5082"

	// Allow requests that use API key
	apiKey := r.Header.Get("x-api-key")
	if apiKey == predefinedAPIKey {
		// If the API key matches, bypass ownership check
		return nil
	}

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

// GetServiceLogsBatch retrieves logs for a given service from Redis based on the specified index range
func GetServiceLogsBatch(w http.ResponseWriter, r *http.Request) {
	enclaveIdentifier := r.URL.Query().Get("enclaveIdentifier")
	serviceName := r.URL.Query().Get("serviceName")
	startIndexStr := r.URL.Query().Get("start")
	endIndexStr := r.URL.Query().Get("end")
	lastLogsStr := r.URL.Query().Get("last") // Optional: Retrieve the last 'n' logs

	if serviceName == "" {
		http.Error(w, "Missing serviceName query parameter", http.StatusBadRequest)
		return
	}

	var startIndex, endIndex int64
	var err error

	if lastLogsStr != "" {
		// Retrieve the last 'n' logs
		lastLogs, err := strconv.Atoi(lastLogsStr)
		if err != nil {
			http.Error(w, "Invalid 'last' value: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Calculate start and end indices for the last 'n' logs
		endIndex = -1
		startIndex = endIndex - int64(lastLogs) + 1
	} else if startIndexStr != "" && endIndexStr != "" {
		// Convert start and end indices to integers
		startIndex, err = strconv.ParseInt(startIndexStr, 10, 64)
		if err != nil {
			http.Error(w, "Invalid start index: "+err.Error(), http.StatusBadRequest)
			return
		}

		endIndex, err = strconv.ParseInt(endIndexStr, 10, 64)
		if err != nil {
			http.Error(w, "Invalid end index: "+err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		http.Error(w, "Either 'start' and 'end' or 'last' query parameter must be provided", http.StatusBadRequest)
		return
	}

	// Fetch logs from Redis using the range command
	logs, err := util.GetNodeLogs(enclaveIdentifier, serviceName, startIndex, endIndex)
	if err != nil {
		http.Error(w, "Failed to retrieve logs from Redis: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Determine the actual start and end indices of the returned logs
	actualStartIndex := startIndex
	actualEndIndex := startIndex + int64(len(logs)) - 1

	// Prepare the response in JSON format
	response := map[string]interface{}{
		"serviceName": serviceName,
		"startIndex":  actualStartIndex,
		"endIndex":    actualEndIndex,
		"logs":        logs,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, "Failed to encode response: "+err.Error(), http.StatusInternalServerError)
		return
	}
}

// PatchIngressesHandler updates ingresses with new hostname
func PatchIngressesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	requiredAPIKey := "6a3bc0d4-d0fa-40f3-bb16-1a3a16fc5082"
	apiKey := r.Header.Get("x-api-key")
	if apiKey != requiredAPIKey {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var requestData struct {
		IngressHostnames map[string]string `json:"ingressHostnames"`
		Namespace        string            `json:"namespace"`
	}

	if err := json.NewDecoder(r.Body).Decode(&requestData); err != nil {
		http.Error(w, "Failed to decode request body", http.StatusBadRequest)
		return
	}

	err := patchIngressesHostnames(requestData.IngressHostnames, requestData.Namespace)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to patch ingresses: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Ingress hostnames patched successfully"))
}
