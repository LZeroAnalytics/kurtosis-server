package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/kurtosis_core_rpc_api_bindings"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/lib/starlark_run_config"
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/lib/kurtosis_context"
	"log"
	"net/http"
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

func HandleRoot(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "Welcome to the Kurtosis API Server")
}

func RunPackage(w http.ResponseWriter, r *http.Request) {
	// Extract enclaveName from query parameters
	enclaveName := r.URL.Query().Get("enclaveName")
	if enclaveName == "" {
		http.Error(w, "Missing enclaveName query parameter", http.StatusBadRequest)
		return
	}

	// Upgrade HTTP connection to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade to WebSocket: %v", err)
		return
	}
	defer conn.Close()

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

	// Stream response lines
	for line := range responseLines {
		if line == nil {
			continue
		}

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
				outputJSON, err := json.Marshal(output)
				if err != nil {
					log.Printf("Error marshaling progress output: %v", err)
					continue
				}
				conn.WriteMessage(websocket.TextMessage, outputJSON)
			}
		case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_InstructionResult:
			// Handle InstructionResult
			result := detail.InstructionResult
			output := map[string]interface{}{
				"info": result.SerializedInstructionResult,
			}
			outputJSON, err := json.Marshal(output)
			if err != nil {
				log.Printf("Error marshaling instruction result output: %v", err)
				continue
			}
			conn.WriteMessage(websocket.TextMessage, outputJSON)
		case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_Error:
			// Handle Error
			log.Printf("Error during Starlark execution: %v", detail.Error)
			conn.WriteMessage(websocket.TextMessage, []byte("Error: "+detail.Error.String()))
		case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_Warning:
			// Handle Warning
			log.Printf("Warning: %s", detail.Warning.WarningMessage)
		case *kurtosis_core_rpc_api_bindings.StarlarkRunResponseLine_RunFinishedEvent:
			// Handle RunFinishedEvent
			if detail.RunFinishedEvent.IsRunSuccessful {
				output := map[string]interface{}{
					"info": "Network run successfully",
				}
				outputJSON, err := json.Marshal(output)
				if err != nil {
					log.Printf("Error marshaling progress output: %v", err)
					continue
				}
				conn.WriteMessage(websocket.TextMessage, outputJSON)
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
