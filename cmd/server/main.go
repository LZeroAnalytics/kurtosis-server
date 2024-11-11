package main

import (
	"kurtosis-server/internal/api"
	"kurtosis-server/internal/api/util"
	"log"
	"net/http"
)

func main() {

	// Initialize Redis client
	util.Init()

	mux := http.NewServeMux()
	mux.HandleFunc("/", api.HandleRoot)
	mux.HandleFunc("/start", api.StartNetwork)
	mux.HandleFunc("/stop", api.StopNetwork)
	mux.HandleFunc("/services", api.GetServicesInfo)
	mux.HandleFunc("/exec", api.ExecServiceCommand)
	mux.HandleFunc("/stream", api.StreamOutput)
	mux.HandleFunc("/stream-logs", api.StreamServiceLogs)
	mux.HandleFunc("/node-logs", api.GetServiceLogsBatch)
	mux.HandleFunc("/patch-hostnames", api.PatchIngressesHandler)

	log.Println("Starting server on :8080")
	if err := http.ListenAndServe(":8080", withCORS(mux)); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func withCORS(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		handler.ServeHTTP(w, r)
	})
}
