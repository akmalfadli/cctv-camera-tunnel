package api

import (
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
	mux.HandleFunc("/api/cameras", s.handleCameras) // Public list? Or protected? Keeping public for now as per dashboard reqs might vary. Let's keep READ public, WRITE protected.
	mux.HandleFunc("/api/status", s.handleStatus)

	// Admin API (Protected by Middleware)
	mux.HandleFunc("/api/admin/camera", s.handleAdminCamera) // POST (Create), PUT (Update), DELETE
	mux.HandleFunc("/api/admin/camera/toggle", s.handleAdminToggle)

	// HLS Stream Serving
	fileServer := http.FileServer(http.Dir(s.config.HLSOutputRoot))
	mux.Handle("/hls/", http.StripPrefix("/hls/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})))

	// Static Files
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web/static"))))

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
		var cam config.Camera
		if err := json.NewDecoder(r.Body).Decode(&cam); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		
		id := cam.ID
		if id == "" {
			// Generate ID from name if missing for Create
			id = strings.ToLower(strings.ReplaceAll(cam.Name, " ", "_"))
		}

		if err := s.registry.Set(id, cam); err != nil {
			http.Error(w, "Failed to save camera", http.StatusInternalServerError)
			return
		}
		
		// If enabled, start stream. If disabled or update, restart/stop.
		if cam.Enabled {
			s.streamMgr.RestartStream(id, cam)
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

// Pages

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.templates.ExecuteTemplate(w, "login.html", nil)
}

func (s *Server) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	cams := s.registry.GetAll()
	s.templates.ExecuteTemplate(w, "admin.html", cams)
}

func (s *Server) handleCameras(w http.ResponseWriter, r *http.Request) {
	cams := s.registry.GetAll()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(cams)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := s.streamMgr.GetStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	cams := s.registry.GetAll()
	s.templates.ExecuteTemplate(w, "index.html", cams)
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
	
	s.templates.ExecuteTemplate(w, "viewer.html", data)
}
