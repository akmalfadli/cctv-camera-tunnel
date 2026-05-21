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

	// Ensure uniform timing regardless of whether the username exists
	var targetHash string
	if username == expectedUser {
		targetHash = passwordHash
	} else {
		// A dummy bcrypt hash (cost 10) to consume comparable CPU time
		targetHash = "$2a$10$S9R3k6J0qGzVbW1E3yRzUe5F3qGzVbW1E3yRzUe5F3qGzVbW1E3y."
	}

	if err := bcrypt.CompareHashAndPassword([]byte(targetHash), []byte(password)); err != nil {
		return "", fmt.Errorf("invalid credentials")
	}

	if username != expectedUser {
		return "", fmt.Errorf("invalid credentials")
	}

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
	case "/api/login", "/login", "/favicon.ico":
		return true
	}
	return strings.HasPrefix(path, "/static/")
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
