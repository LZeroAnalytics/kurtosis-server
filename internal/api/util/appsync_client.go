package util

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

const (
	apiKey   = "da2-jidg4wy55raitfxkqzrjj533yy"
	endpoint = "https://n7larelp2vhahihlpzj7kgkvnu.appsync-api.eu-west-1.amazonaws.com/graphql"
)

func makeGraphqlRequest(query string, variables map[string]interface{}) ([]byte, error) {
	payload := map[string]interface{}{
		"query":     query,
		"variables": variables,
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("received non-OK HTTP status: %s", resp.Status)
	}

	return body, nil
}

func GetNetworkStatus(networkID string) (string, error) {
	query := `query GetNetwork($id: ID!) {
		getNetwork(id: $id) {
			id
			status
		}
	}`

	variables := map[string]interface{}{
		"id": networkID,
	}

	body, err := makeGraphqlRequest(query, variables)
	if err != nil {
		return "", err
	}

	var result map[string]interface{}
	err = json.Unmarshal(body, &result)
	if err != nil {
		return "", err
	}

	if data, ok := result["data"].(map[string]interface{}); ok {
		if network, ok := data["getNetwork"].(map[string]interface{}); ok {
			if status, ok := network["status"].(string); ok {
				return status, nil
			}
		}
	}

	return "", errors.New("could not retrieve network status")
}

func UpdateNetworkStatus(networkID, status string, deletionDate *string) error {
	// Fetch the current version of the network
	version, err := getNetworkVersion(networkID)
	if err != nil {
		return err
	}

	mutation := `mutation UpdateNetwork($input: UpdateNetworkInput!) {
		updateNetwork(input: $input) {
			id
			status
			deletionDate
			_version
		}
	}`

	input := map[string]interface{}{
		"id":       networkID,
		"status":   status,
		"_version": version,
	}

	if deletionDate != nil {
		input["deletionDate"] = *deletionDate
	}

	variables := map[string]interface{}{
		"input": input,
	}

	body, err := makeGraphqlRequest(mutation, variables)
	if err != nil {
		return err
	}

	log.Printf("Network %s status updated to %s with deletionDate %v, Response: %s", networkID, status, deletionDate, string(body))
	return nil
}

// Helper function to fetch the current version of the network
func getNetworkVersion(networkID string) (int, error) {
	query := `query GetNetworkVersion($id: ID!) {
		getNetwork(id: $id) {
			_version
		}
	}`

	variables := map[string]interface{}{
		"id": networkID,
	}

	body, err := makeGraphqlRequest(query, variables)
	if err != nil {
		return 0, err
	}

	var result map[string]interface{}
	err = json.Unmarshal(body, &result)
	if err != nil {
		return 0, err
	}

	if data, ok := result["data"].(map[string]interface{}); ok {
		if network, ok := data["getNetwork"].(map[string]interface{}); ok {
			if version, ok := network["_version"].(float64); ok {
				return int(version), nil
			}
		}
	}

	return 0, errors.New("could not retrieve network version")
}
