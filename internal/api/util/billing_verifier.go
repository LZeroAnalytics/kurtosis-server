package util

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// BillingResponse represents the structure of the response from the billing API
type BillingResponse struct {
	Billings []interface{} `json:"billings"`
	Balance  float64       `json:"balance"`
}

// CheckUserBilling checks if the user has billings
func CheckUserBilling(authorizationHeader string) (bool, error) {

	// Define the specific API key that should be accepted
	const apiKey = "6a3bc0d4-d0fa-40f3-bb16-1a3a16fc5082"

	parts := strings.Split(authorizationHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		return false, fmt.Errorf("invalid authorization header format")
	}

	token := parts[1]

	// Check if the token matches the specific API key
	if token == apiKey {
		// Bypass the billing API check and return true
		return true, nil
	}

	// Call the billing API
	billingAPIURL := "https://lzerobilling.lzeroanalytics.com/customers/me/billing"
	req, err := http.NewRequest("GET", billingAPIURL, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to call billing API: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("billing API responded with error: %s", string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("failed to read billing API response: %v", err)
	}

	// Parse the JSON response
	var billingResp BillingResponse
	err = json.Unmarshal(body, &billingResp)
	if err != nil {
		return false, fmt.Errorf("failed to parse billing API response: %v", err)
	}

	// Check if the user has billings
	hasBillings := len(billingResp.Billings) > 0
	return hasBillings, nil
}
