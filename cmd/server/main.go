package main

import (
	"fmt"
	"kurtosis-server/internal/api"
	"log"
	"net/http"
	"os"
	"os/exec"
)

func main() {

	err := configureKubernetes()
	if err != nil {
		log.Println(err)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", api.HandleRoot)
	mux.HandleFunc("/start", api.HandleStartKurtosis)

	log.Println("Starting server on :8080")
	if err := http.ListenAndServe(":8080", mux); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func configureKubernetes() error {

	// Set up the command to update kubeconfig using AWS CLI
	cmd := exec.Command("aws", "eks", "update-kubeconfig", "--name", "lzeroCluster")

	// Inherit environment, including AWS credentials
	cmd.Env = os.Environ()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to update kubeconfig: %s, %v", string(output), err)
	}

	fmt.Println("Kubernetes configured successfully.")
	return nil
}
