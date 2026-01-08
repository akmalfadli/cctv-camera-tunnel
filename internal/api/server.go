package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"camera-tunnel/internal/auth"
	"camera-tunnel/internal/camera"
	"camera-tunnel/internal/config"
	"camera-tunnel/internal/ffmpeg"
)

type Server struct {
	config    *config.Config
	registry  *camera.Registry
	streamMgr *ffmpeg.StreamManager
	auth      *auth.Authenticator
	templates *template.Template
}

func NewServer(cfg *config.Config, reg *camera.Registry, sm *ffmpeg.StreamManager, aut *auth.Authenticator) *Server {
	return &Server{
		config:    cfg,
		registry:  reg,
		streamMgr: sm,
		auth:      aut,
	}
}

func (s *Server) LoadTemplates() error {
	var err error
	s.templates, err = template.ParseGlob("internal/templates/*.html")
	if err != nil {
		return err
	}
	return nil
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Public API
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/cameras", s.handleCameras)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/metrics", s.handleMetrics)

	// Admin API (Protected by Middleware)
	mux.HandleFunc("/api/admin/camera", s.handleAdminCamera) // POST (Create), PUT (Update), DELETE
	mux.HandleFunc("/api/admin/camera/get", s.handleAdminCameraGet)
	mux.HandleFunc("/api/admin/camera/toggle", s.handleAdminToggle)
	mux.HandleFunc("/api/admin/settings", s.handleAdminSettings)

	// HLS Stream Serving
	fileServer := http.FileServer(http.Dir(s.config.HLSOutputRoot))
	mux.Handle("/hls/", http.StripPrefix("/hls/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})))

	// Static Files
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))
	mux.HandleFunc("/favicon.ico", s.handleFavicon)

	// Pages
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/login", s.handleLoginPage)
	mux.HandleFunc("/admin", s.handleAdminPage)
	mux.HandleFunc("/view/", s.handleViewer)

	// Apply Auth Middleware
	return s.auth.Middleware(mux)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	token, err := s.auth.Login(creds.Username, creds.Password)
	if err != nil {
		http.Error(w, "Invalid credentials", http.StatusUnauthorized)
		return
	}

	// Set cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "token",
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(24 * time.Hour),
		HttpOnly: true,
	})

	json.NewEncoder(w).Encode(map[string]string{"token": token})
}

// Admin Handlers

func (s *Server) handleAdminCamera(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" || r.Method == "PUT" {
		var payload struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Enabled     bool   `json:"enabled"`
			RTSPURL     string `json:"rtsp_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		payload.Name = strings.TrimSpace(payload.Name)
		payload.Description = strings.TrimSpace(payload.Description)
		payload.RTSPURL = strings.TrimSpace(payload.RTSPURL)

		if payload.Name == "" || payload.RTSPURL == "" {
			http.Error(w, "Missing required fields", http.StatusBadRequest)
			return
		}

		id := strings.TrimSpace(payload.ID)
		if id == "" {
			id = generateCameraID()
		}

		cam := config.Camera{
			ID:          id,
			Name:        payload.Name,
			Description: payload.Description,
			Enabled:     payload.Enabled,
			RTSPURL:     payload.RTSPURL,
		}

		if err := s.registry.Set(id, cam); err != nil {
			http.Error(w, "Failed to save camera", http.StatusInternalServerError)
			return
		}

		storedCam, _ := s.registry.Get(id)

		// If enabled, start stream. If disabled or update, restart/stop.
		if cam.Enabled {
			s.streamMgr.RestartStream(id, storedCam)
		} else {
			s.streamMgr.StopStream(id)
		}

		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method == "DELETE" {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "Missing ID", http.StatusBadRequest)
			return
		}

		s.streamMgr.StopStream(id)
		if err := s.registry.Delete(id); err != nil {
			http.Error(w, "Failed to delete camera", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) handleAdminCameraGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "Missing ID", http.StatusBadRequest)
		return
	}
	cam, ok := s.registry.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cam)
}

func (s *Server) handleAdminToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	cam, ok := s.registry.Get(req.ID)
	if !ok {
		http.NotFound(w, r)
		return
	}

	cam.Enabled = req.Enabled
	if err := s.registry.Set(req.ID, cam); err != nil {
		http.Error(w, "Failed to update camera", http.StatusInternalServerError)
		return
	}

	if cam.Enabled {
		s.streamMgr.StartStream(req.ID, cam)
	} else {
		s.streamMgr.StopStream(req.ID)
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.config.SettingsSnapshot())
		return
	case http.MethodPut:
		var payload config.AppSettings
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		if err := s.config.UpdateSettings(payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleCameras(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cams := s.registry.GetAll()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(publicCameras(cams))
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := s.streamMgr.GetStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	metrics := s.streamMgr.GetMetrics()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	cams := s.registry.GetAll()
	s.renderTemplate(w, "index.html", cams)
}

func (s *Server) handleViewer(w http.ResponseWriter, r *http.Request) {
	id := filepath.Base(r.URL.Path)
	cam, ok := s.registry.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	data := struct {
		ID     string
		Camera config.Camera
	}{
		ID:     id,
		Camera: cam,
	}

	s.renderTemplate(w, "viewer.html", data)
}

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write([]byte(faviconSVG))
}

const faviconSVG = `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
  <rect width="64" height="64" rx="12" fill="#0b5ed7"/>
  <path d="M18 22h28v6H18zm0 12h28v6H18zm0 12h16v6H18z" fill="#fff"/>
</svg>`

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "login.html", nil)
}

func (s *Server) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	cams := s.registry.GetAll()
	s.renderTemplate(w, "admin.html", cams)
}

func (s *Server) renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "Template rendering error", http.StatusInternalServerError)
	}
}

type publicCamera struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

func publicCameras(source map[string]config.Camera) []publicCamera {
	resp := make([]publicCamera, 0, len(source))
	for id, cam := range source {
		resp = append(resp, publicCamera{
			ID:          id,
			Name:        cam.Name,
			Description: cam.Description,
			Enabled:     cam.Enabled,
		})
	}
	return resp
}

func generateCameraID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "cam_" + hex.EncodeToString(b)
}
