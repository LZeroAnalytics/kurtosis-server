package api

import (
	"fmt"
	"net/http"
)

func HandleRoot(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "Welcome to the Kurtosis API Server")
}

func HandleStartKurtosis(w http.ResponseWriter, r *http.Request) {
	// Start Kurtosis engine
	fmt.Fprintln(w, "Starting Kurtosis Engine...")
}
