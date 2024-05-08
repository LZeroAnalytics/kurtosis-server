package main

import (
	"github.com/kurtosis-tech/kurtosis/api/golang/engine/lib/kurtosis_context"
	"kurtosis-server/internal/api"
	"log"
	"net/http"
)

func main() {

	kurtosisCtx, err := kurtosis_context.NewKurtosisContextFromLocalEngine()
	if err != nil {
		log.Fatalf("Failed to get Kurtosis engine: %v", err)
	}

	log.Println(kurtosisCtx)

	mux := http.NewServeMux()
	mux.HandleFunc("/", api.HandleRoot)
	mux.HandleFunc("/start", api.HandleStartKurtosis)

	log.Println("Starting server on :8080")
	if err := http.ListenAndServe(":8080", mux); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
