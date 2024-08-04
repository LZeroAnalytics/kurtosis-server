package util

import (
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/golang-jwt/jwt/v4"
	"net/http"
)

var cognitoIssuer = "https://cognito-idp.eu-west-1.amazonaws.com/eu-west-1_CG2ukNxze"
var cognitoJWKSURL = cognitoIssuer + "/.well-known/jwks.json"

// JWK represents a JSON Web Key as returned by Cognito's JWKS endpoint
type JWK struct {
	Keys []jsonWebKey `json:"keys"`
}

type jsonWebKey struct {
	Kty string   `json:"kty"`
	Kid string   `json:"kid"`
	Use string   `json:"use"`
	N   string   `json:"n"`
	E   string   `json:"e"`
	X5c []string `json:"x5c"`
}

// Fetch and parse the JWKS, and return the RSA public key for a given key ID (kid)
func getRSAPublicKey(kid string) (*rsa.PublicKey, error) {
	resp, err := http.Get(cognitoJWKSURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWKS: %v", err)
	}
	defer resp.Body.Close()

	var jwks JWK
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return nil, fmt.Errorf("failed to decode JWKS: %v", err)
	}

	for _, key := range jwks.Keys {
		if key.Kid == kid {
			return jwt.ParseRSAPublicKeyFromPEM([]byte(generatePEM(key.X5c[0])))
		}
	}

	return nil, errors.New("no matching key found in JWKS")
}

// Convert the x5c certificate into PEM format
func generatePEM(cert string) string {
	return fmt.Sprintf("-----BEGIN CERTIFICATE-----\n%s\n-----END CERTIFICATE-----", cert)
}

// GetUserIDFromToken extracts and verifies the user ID (sub) from the JWT access token
func GetUserIDFromToken(accessToken string) (string, error) {
	token, err := jwt.Parse(accessToken, func(token *jwt.Token) (interface{}, error) {
		// Verify the signing method is RSA
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}

		// Get the key ID (kid) from the token header
		kid, ok := token.Header["kid"].(string)
		if !ok {
			return nil, errors.New("missing key ID (kid) in token header")
		}

		// Get the RSA public key using the kid
		return getRSAPublicKey(kid)
	})

	if err != nil {
		return "", fmt.Errorf("token verification failed: %v", err)
	}

	// Validate issuer and extract claims
	if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
		if !claims.VerifyIssuer(cognitoIssuer, true) {
			return "", fmt.Errorf("invalid token issuer")
		}

		if userID, ok := claims["sub"].(string); ok {
			return userID, nil
		}
	}

	return "", fmt.Errorf("could not extract user ID from token")
}
