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
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func HandleRoot(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "Welcome to the Kurtosis API Server")
}

func HandleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	enclaveName := r.URL.Query().Get("enclaveName")
	packageURL := r.URL.Query().Get("packageURL")
	if enclaveName == "" || packageURL == "" {
		http.Error(w, "Missing required parameters", http.StatusBadRequest)
		return
	}

	// Now you have req.EnclaveName and req.PackageURL
	runPackage(w, r, enclaveName, packageURL)
}

func runPackage(w http.ResponseWriter, r *http.Request, enclaveName, packageURL string) {
	// Upgrade HTTP connection to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade to WebSocket: %v", err)
		return
	}
	defer conn.Close()

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

	// Define the StarlarkRunConfig
	starlarkRunConfig := starlark_run_config.NewRunStarlarkConfig()

	// Run the Starlark package
	responseLines, cancelFunc, err := enclaveCtx.RunStarlarkRemotePackage(r.Context(), packageURL, starlarkRunConfig)
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
