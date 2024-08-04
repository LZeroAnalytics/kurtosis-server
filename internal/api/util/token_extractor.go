package util

import (
	"fmt"
	"github.com/MicahParks/keyfunc"
	"github.com/golang-jwt/jwt/v4"
	"time"
)

var cognitoIssuer = "https://cognito-idp.eu-west-1.amazonaws.com/eu-west-1_CG2ukNxze"
var cognitoJWKSURL = cognitoIssuer + "/.well-known/jwks.json"

func GetUserIDFromToken(accessToken string) (string, error) {
	// Fetch the JWKS from the specified URL
	jwks, err := keyfunc.Get(cognitoJWKSURL, keyfunc.Options{
		RefreshTimeout: 10 * time.Second,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create JWKS from URL: %v", err)
	}

	// Parse the token using the JWKS
	token, err := jwt.Parse(accessToken, jwks.Keyfunc)
	if err != nil {
		return "", fmt.Errorf("token verification failed: %v", err)
	}

	// Validate issuer and extract claims
	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		if userID, ok := claims["sub"].(string); ok {
			return userID, nil
		}
	}

	return "", fmt.Errorf("could not extract user ID from token")
}
