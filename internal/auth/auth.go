package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
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
	if username != a.config.Auth.Username || password != a.config.Auth.Password {
		return "", fmt.Errorf("invalid credentials")
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user": username,
		"exp":  time.Now().Add(24 * time.Hour).Unix(),
	})

	tokenString, err := token.SignedString([]byte(a.config.Auth.JWTSecret))
	if err != nil {
		return "", err
	}

	return tokenString, nil
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allow public access to login and HLS streams (viewers need token? user said "create login jwt to access dashboard", implying dashboard is protected. We can leave streams public for now or protect them later. Let's protect dashboard API).
		// For simplicity:
		// - /api/login: public
		// - /hls/: public (or token param?) - Keeping public as per initial request unless specified otherwise.
		// - /static/: public
		// - /view/: public (viewer page)
		// - /api/admin/*: protected

		path := r.URL.Path
		if path == "/api/login" || strings.HasPrefix(path, "/hls/") || strings.HasPrefix(path, "/static/") || strings.HasPrefix(path, "/view/") || path == "/" {
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
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		token, err := jwt.Parse(tokenStr, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return []byte(a.config.Auth.JWTSecret), nil
		})

		if err != nil || !token.Valid {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), UserKey, "admin")
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
