package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/kurtosis_core_rpc_api_bindings"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/lib/starlark_run_config"
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/lib/kurtosis_context"
	"kurtosis-server/internal/api/util"
	"log"
	"net/http"
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
		sessionID = uuid.New().String()
	}

	// Read message from request body
	var runPackageMessage RunPackageMessage
	err := json.NewDecoder(r.Body).Decode(&runPackageMessage)
	if err != nil {
		http.Error(w, "Invalid message format: "+err.Error(), http.StatusBadRequest)
		return
	}

	paramsJSON, err := json.Marshal(runPackageMessage.Params)
	if err != nil {
		http.Error(w, "Failed to serialize parameters: "+err.Error(), http.StatusInternalServerError)
		return
	}

	newRedisSession := &util.RedisSession{
		ResponseLines: []string{},
	}
	err = util.StoreSession(sessionID, newRedisSession)
	if err != nil {
		http.Error(w, "Error storing session: "+err.Error(), http.StatusInternalServerError)
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
			http.Error(w, "Failed to create Kurtosis context: "+err.Error(), http.StatusInternalServerError)
			return
		}

		enclaveCtx, err := kurtosisCtx.CreateEnclave(bgContext, enclaveName)
		if err != nil {
			http.Error(w, "Failed to create enclave: "+err.Error(), http.StatusInternalServerError)
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
				log.Printf("Failed to create ingress for service %s: %v", serviceMapping.ServiceName, err)
			}
		}

		starlarkRunOptions := starlark_run_config.WithSerializedParams(string(paramsJSON))
		starlarkRunConfig := starlark_run_config.NewRunStarlarkConfig(starlarkRunOptions)

		responseLines, _, err := enclaveCtx.RunStarlarkRemotePackage(bgContext, runPackageMessage.PackageURL, starlarkRunConfig)
		if err != nil {
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
				outputJSON = []byte("Error: " + detail.Error.String())
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
