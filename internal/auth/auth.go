package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"camera-tunnel/internal/config"
)

type contextKey string

const UserKey contextKey = "user"

type Authenticator struct {
	config *config.Config
}

func NewAuthenticator(cfg *config.Config) *Authenticator {
	return &Authenticator{config: cfg}
}

func (a *Authenticator) Login(username, password string) (string, error) {
	expectedUser, passwordHash, jwtSecret := a.config.Credentials()

	// DEBUG: log what we're comparing
	fmt.Printf("[DEBUG] Login attempt - user: %q, expected: %q\n", username, expectedUser)
	fmt.Printf("[DEBUG] Hash from DB (first 20 chars): %q\n", passwordHash[:min(20, len(passwordHash))])
	fmt.Printf("[DEBUG] Password length: %d\n", len(password))

	if username != expectedUser {
		fmt.Println("[DEBUG] Username mismatch")
		return "", fmt.Errorf("invalid credentials")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
		fmt.Printf("[DEBUG] bcrypt.CompareHashAndPassword error: %v\n", err)
		return "", fmt.Errorf("invalid credentials")
	}
	fmt.Println("[DEBUG] Password verified OK")

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user": username,
		"exp":  time.Now().Add(24 * time.Hour).Unix(),
	})

	tokenString, err := token.SignedString([]byte(jwtSecret))
	if err != nil {
		return "", err
	}

	return tokenString, nil
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if isPublicPath(path) {
			next.ServeHTTP(w, r)
			return
		}

		// Check for cookie or header
		tokenStr := ""
		cookie, err := r.Cookie("token")
		if err == nil {
			tokenStr = cookie.Value
		} else {
			authHeader := r.Header.Get("Authorization")
			if strings.HasPrefix(authHeader, "Bearer ") {
				tokenStr = strings.TrimPrefix(authHeader, "Bearer ")
			}
		}

		if tokenStr == "" {
			handleUnauthorized(w, r)
			return
		}

		token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			_, _, secret := a.config.Credentials()
			return []byte(secret), nil
		})

		if err != nil || !token.Valid {
			handleUnauthorized(w, r)
			return
		}

		ctx := context.WithValue(r.Context(), UserKey, tokenUsername(token))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func isPublicPath(path string) bool {
	switch path {
	case "/api/login", "/login", "/api/cameras", "/favicon.ico":
		return true
	}
	return strings.HasPrefix(path, "/hls/") || strings.HasPrefix(path, "/static/") || strings.HasPrefix(path, "/view/")
}

func handleUnauthorized(w http.ResponseWriter, r *http.Request) {
	if wantsHTML(r) {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}

func wantsHTML(r *http.Request) bool {
	if r.Method == http.MethodGet {
		accept := r.Header.Get("Accept")
		return strings.Contains(accept, "text/html") || strings.Contains(r.Header.Get("User-Agent"), "Mozilla")
	}
	return false
}

func tokenUsername(token *jwt.Token) string {
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "admin"
	}
	if user, ok := claims["user"].(string); ok {
		return user
	}
	return "admin"
}
