package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/kurtosis-tech/kurtosis/api/golang/core/lib/starlark_run_config"
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/lib/kurtosis_context"
	"log"
	"net/http"
)

// Define a struct to parse the JSON request body
type runRequest struct {
	EnclaveName string `json:"enclaveName"`
	PackageURL  string `json:"packageUrl"`
}

func HandleRoot(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "Welcome to the Kurtosis API Server")
}

func HandleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req runRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Now you have req.EnclaveName and req.PackageURL
	runPackage(w, r.Context(), req.EnclaveName, req.PackageURL)
}

func runPackage(w http.ResponseWriter, ctx context.Context, enclaveName, packageURL string) {
	// Initialize the Kurtosis context
	kurtosisCtx, err := kurtosis_context.NewKurtosisContextFromLocalEngine()
	if err != nil {
		http.Error(w, "Failed to create Kurtosis context: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Create an enclave
	enclaveCtx, err := kurtosisCtx.CreateEnclave(ctx, enclaveName)
	if err != nil {
		http.Error(w, "Failed to create enclave: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Define the StarlarkRunConfig
	starlarkRunConfig := starlark_run_config.NewRunStarlarkConfig()

	// Run the Starlark package
	response, err := enclaveCtx.RunStarlarkRemotePackageBlocking(ctx, packageURL, starlarkRunConfig)
	if err != nil {
		http.Error(w, "Failed to run Starlark package: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Println(response)

	// Write a success response
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Enclave run completed successfully"))
}
