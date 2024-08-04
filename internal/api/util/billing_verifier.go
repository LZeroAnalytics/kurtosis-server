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

	parts := strings.Split(authorizationHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" {
		return false, fmt.Errorf("invalid authorization header format")
	}

	token := parts[1]

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
