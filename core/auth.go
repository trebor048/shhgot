package core

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var jwtSecret = []byte("shhgit-secret-key") // TODO: Load from environment variable

// Claims defines JWT claims structure
type Claims struct {
	UserID   string `json:"user_id"`
	APIKey   string `json:"api_key"`
	Scope    string `json:"scope"`
	IssuedAt int64  `json:"iat"`
	jwt.RegisteredClaims
}

// GenerateToken creates a JWT token for API access
func GenerateToken(userID, apiKey string, expiresIn time.Duration) (string, error) {
	claims := &Claims{
		UserID: userID,
		APIKey: apiKey,
		Scope:  "read:matches,write:matches",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiresIn)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

// ValidateToken verifies a JWT token
func ValidateToken(tokenString string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return jwtSecret, nil
	})

	if err != nil {
		return nil, err
	}

	if !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	return claims, nil
}

// AuthMiddleware is an HTTP middleware that validates JWT or API key
func AuthMiddleware(next http.HandlerFunc, requireAuth bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for health check
		if r.URL.Path == "/health" {
			next(w, r)
			return
		}

		// Skip auth if not required (e.g., for internal health checks)
		if !requireAuth {
			next(w, r)
			return
		}

		var token string

		// Try to get token from Authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader != "" {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && parts[0] == "Bearer" {
				token = parts[1]
			}
		}

		// Try to get API key from query parameter or header
		if token == "" {
			token = r.URL.Query().Get("api_key")
		}
		if token == "" {
			token = r.Header.Get("X-API-Key")
		}

		// Validate token
		if token == "" {
			http.Error(w, `{"error": "Missing authentication token"}`, http.StatusUnauthorized)
			return
		}

		claims, err := ValidateToken(token)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "Invalid token: %s"}`, err.Error()), http.StatusUnauthorized)
			return
		}

		// Store claims in request context for later use
		r.Header.Set("X-User-ID", claims.UserID)
		r.Header.Set("X-API-Key", claims.APIKey)

		next(w, r)
	}
}
