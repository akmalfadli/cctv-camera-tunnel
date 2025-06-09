package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/term"
)

// Configuration
type Config struct {
	// VPS Configuration
	VPSHost       string `json:"vps_host"`
	VPSUser       string `json:"vps_user"`
	VPSPort       int    `json:"vps_port"`
	SSHKeyPath    string `json:"ssh_key_path"`
	SSHPassphrase string `json:"ssh_passphrase,omitempty"` // Optional, leave empty to prompt

	// HTTP Server Configuration
	LocalHTTPPort int `json:"local_http_port"`
	VPSHTTPPort   int `json:"vps_http_port"`

	// Camera Configuration
	Cameras map[string]Camera `json:"cameras"`
}

type Camera struct {
	Name        string `json:"name"`
	RTSPURL     string `json:"rtsp_url"`
	Description string `json:"description"`
}

// Default configuration
func getDefaultConfig() *Config {
	return &Config{
		VPSHost:       "my.perwirateknologi.com",
		VPSUser:       "perwira",
		VPSPort:       22,
		SSHKeyPath:    "~/.ssh/id_rsa",
		LocalHTTPPort: 8080,
		VPSHTTPPort:   8081,
		Cameras: map[string]Camera{
			"depan": {
				Name:        "Kamera Pelayanan",
				RTSPURL:     "rtsp://admin:pAdli3nb31!@192.168.1.10:554",
				Description: "Kamera Pelayanan Kantor",
			},
			"pelayanan": {
				Name:        "Kamera Depan",
				RTSPURL:     "rtsp://admin:pAdli3nb31!@192.168.1.74:554",
				Description: "Kamera Depan kantor",
			},
			"garasi": {
				Name:        "Kamera Garasi",
				RTSPURL:     "rtsp://admin:pAdli3nb31!@192.168.1.17:554",
				Description: "Kamera Garasi",
			},
		},
	}
}

type Server struct {
	config     *Config
	httpServer *http.Server
	sshConn    ssh.Conn
	sshClient  *ssh.Client
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	logger     *log.Logger
	templates  *template.Template
}

// HTML Templates - removed as they're now in external files

// NewServer creates a new server instance
func NewServer(config *Config) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	
	server := &Server{
		config: config,
		ctx:    ctx,
		cancel: cancel,
		logger: log.New(os.Stdout, "[CAMERA-SERVER] ", log.LstdFlags),
	}
	
	// Load templates
	server.loadTemplates()
	
	return server
}

// loadTemplates loads HTML templates from files
func (s *Server) loadTemplates() {
	var err error
	s.templates, err = template.ParseGlob("templates/*.html")
	if err != nil {
		s.logger.Printf("Warning: Could not load templates from files: %v", err)
		s.logger.Println("Using embedded fallback templates...")
		s.loadEmbeddedTemplates()
	} else {
		s.logger.Println("Successfully loaded templates from templates/ directory")
	}
}

// loadEmbeddedTemplates provides fallback templates if files aren't found
func (s *Server) loadEmbeddedTemplates() {
	mainTemplate := `<!DOCTYPE html>
<html><head><title>Multi-Camera CCTV Viewer</title></head>
<body><h1>Template files not found</h1>
<p>Please create templates/main_viewer.html and templates/single_camera.html</p>
<p>Or run from the directory containing the templates/ folder</p></body></html>`

	singleTemplate := `<!DOCTYPE html>
<html><head><title>Single Camera View</title></head>
<body><h1>Template files not found</h1>
<p>Please create templates/single_camera.html</p></body></html>`

	s.templates = template.New("main")
	template.Must(s.templates.New("main_viewer.html").Parse(mainTemplate))
	template.Must(s.templates.New("single_camera.html").Parse(singleTemplate))
}

// getPassphrase prompts for SSH key passphrase if needed
func (s *Server) getPassphrase() (string, error) {
	// If passphrase is set in config, use it
	if s.config.SSHPassphrase != "" {
		return s.config.SSHPassphrase, nil
	}

	// Prompt for passphrase
	fmt.Print("Enter SSH key passphrase: ")
	passphrase, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println() // New line after password input
	if err != nil {
		return "", fmt.Errorf("failed to read passphrase: %v", err)
	}

	return string(passphrase), nil
}

// parseSSHKey parses SSH private key with optional passphrase
func (s *Server) parseSSHKey(keyPath string) (ssh.Signer, error) {
	// Check if file exists
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("SSH key file does not exist: %s", keyPath)
	}

	privateKeyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read SSH key file %s: %v", keyPath, err)
	}

	// Check if file is empty
	if len(privateKeyBytes) == 0 {
		return nil, fmt.Errorf("SSH key file is empty: %s", keyPath)
	}

	s.logger.Printf("Attempting to parse SSH key: %s (%d bytes)", keyPath, len(privateKeyBytes))

	// Try parsing without passphrase first
	privateKey, err := ssh.ParsePrivateKey(privateKeyBytes)
	if err != nil {
		// If error suggests passphrase protection, try with passphrase
		if strings.Contains(err.Error(), "passphrase") {
			s.logger.Println("SSH key appears to be passphrase protected")
			passphrase, passphraseErr := s.getPassphrase()
			if passphraseErr != nil {
				return nil, passphraseErr
			}

			privateKey, err = ssh.ParsePrivateKeyWithPassphrase(privateKeyBytes, []byte(passphrase))
			if err != nil {
				return nil, fmt.Errorf("failed to parse SSH key with passphrase: %v", err)
			}
			s.logger.Println("Successfully parsed SSH key with passphrase")
		} else {
			return nil, fmt.Errorf("failed to parse SSH key %s: %v", keyPath, err)
		}
	} else {
		s.logger.Println("Successfully parsed SSH key without passphrase")
	}

	return privateKey, nil
}
func (s *Server) expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			s.logger.Printf("Error getting home directory: %v", err)
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// testLocalHTTPServer tests if the local HTTP server is responding
func (s *Server) testLocalHTTPServer() error {
	url := fmt.Sprintf("http://localhost:%d", s.config.LocalHTTPPort)
	s.logger.Printf("Testing local HTTP server: %s", url)
	
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("local HTTP server not responding: %v", err)
	}
	defer resp.Body.Close()
	
	s.logger.Printf("Local HTTP server responding with status: %d", resp.StatusCode)
	return nil
}
func (s *Server) testCameras() []string {
	s.logger.Println("Testing camera connections...")
	var workingCameras []string

	for cameraID, camera := range s.config.Cameras {
		// Extract IP from RTSP URL
		parts := strings.Split(camera.RTSPURL, "@")
		if len(parts) < 2 {
			s.logger.Printf("? %s - Could not extract IP from URL", camera.Name)
			continue
		}
		
		ipPort := strings.Split(parts[1], "/")[0]
		if strings.Contains(ipPort, ":") {
			host, port, err := net.SplitHostPort(ipPort)
			if err != nil {
				s.logger.Printf("? %s - Could not parse host:port", camera.Name)
				continue
			}
			
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 3*time.Second)
			if err != nil {
				s.logger.Printf("✗ %s (%s) - Connection failed: %v", camera.Name, host, err)
			} else {
				conn.Close()
				s.logger.Printf("✓ %s (%s) - Connected", camera.Name, host)
				workingCameras = append(workingCameras, cameraID)
			}
		}
	}

	return workingCameras
}

// checkFFmpeg checks if FFmpeg is available
func (s *Server) checkFFmpeg() bool {
	cmd := exec.Command("ffmpeg", "-version")
	err := cmd.Run()
	if err != nil {
		s.logger.Println("FFmpeg not found. Please install FFmpeg.")
		s.logger.Println("Ubuntu/Debian: sudo apt install ffmpeg")
		s.logger.Println("macOS: brew install ffmpeg")
		s.logger.Println("Windows: Download from https://ffmpeg.org/")
		return false
	}
	
	s.logger.Println("FFmpeg found and working")
	return true
}

// HTTP Handlers
func (s *Server) handleMainViewer(w http.ResponseWriter, r *http.Request) {
	data := struct {
		VPSHost     string
		VPSHTTPPort int
		Cameras     map[string]Camera
		CameraCount int
	}{
		VPSHost:     s.config.VPSHost,
		VPSHTTPPort: s.config.VPSHTTPPort,
		Cameras:     s.config.Cameras,
		CameraCount: len(s.config.Cameras),
	}

	w.Header().Set("Content-Type", "text/html")
	w.Header().Set("Cache-Control", "no-cache")
	
	err := s.templates.ExecuteTemplate(w, "main_viewer.html", data)
	if err != nil {
		s.logger.Printf("Error executing main template: %v", err)
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

func (s *Server) handleSingleCamera(w http.ResponseWriter, r *http.Request) {
	cameraID := strings.TrimPrefix(r.URL.Path, "/camera/")
	
	camera, exists := s.config.Cameras[cameraID]
	if !exists {
		http.Error(w, fmt.Sprintf("Camera '%s' not found", cameraID), http.StatusNotFound)
		return
	}

	data := struct {
		CameraID string
		Camera   Camera
	}{
		CameraID: cameraID,
		Camera:   camera,
	}

	w.Header().Set("Content-Type", "text/html")
	w.Header().Set("Cache-Control", "no-cache")
	
	err := s.templates.ExecuteTemplate(w, "single_camera.html", data)
	if err != nil {
		s.logger.Printf("Error executing single camera template: %v", err)
		http.Error(w, "Template error", http.StatusInternalServerError)
	}
}

func (s *Server) handleCameraList(w http.ResponseWriter, r *http.Request) {
	var cameraList []map[string]interface{}
	
	for id, camera := range s.config.Cameras {
		cameraList = append(cameraList, map[string]interface{}{
			"id":          id,
			"name":        camera.Name,
			"description": camera.Description,
			"stream_url":  fmt.Sprintf("/stream/%s", id),
			"viewer_url":  fmt.Sprintf("/camera/%s", id),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET")
	
	json.NewEncoder(w).Encode(cameraList)
}

func (s *Server) handleCameraStream(w http.ResponseWriter, r *http.Request) {
	cameraID := strings.TrimPrefix(r.URL.Path, "/stream/")
	
	camera, exists := s.config.Cameras[cameraID]
	if !exists {
		http.Error(w, fmt.Sprintf("Camera '%s' not found", cameraID), http.StatusNotFound)
		return
	}

	s.logger.Printf("Starting stream for %s (%s)", camera.Name, cameraID)

	// FFmpeg command for streaming
	args := []string{
		"-rtsp_transport", "tcp",
		"-i", camera.RTSPURL,
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-crf", "28",
		"-maxrate", "2M",
		"-bufsize", "4M",
		"-g", "30",
		"-c:a", "aac",
		"-b:a", "128k",
		"-f", "mp4",
		"-movflags", "frag_keyframe+empty_moov+faststart",
		"-reset_timestamps", "1",
		"-avoid_negative_ts", "make_zero",
		"-fflags", "+genpts",
		"-r", "15",
		"pipe:1",
	}

	cmd := exec.CommandContext(s.ctx, "ffmpeg", args...)
	
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "Failed to create FFmpeg pipe", http.StatusInternalServerError)
		return
	}

	// Set HTTP headers for streaming
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Connection", "close")

	// Start FFmpeg
	if err := cmd.Start(); err != nil {
		http.Error(w, fmt.Sprintf("Failed to start FFmpeg: %v", err), http.StatusInternalServerError)
		return
	}

	// Stream the output
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	// Copy data from FFmpeg to HTTP response
	_, err = io.Copy(w, stdout)
	if err != nil {
		s.logger.Printf("Client disconnected from %s stream: %v", camera.Name, err)
	}
}

// setupRoutes sets up HTTP routes
func (s *Server) setupRoutes() *http.ServeMux {
	mux := http.NewServeMux()
	
	mux.HandleFunc("/", s.handleMainViewer)
	mux.HandleFunc("/api/cameras", s.handleCameraList)
	mux.HandleFunc("/camera/", s.handleSingleCamera)
	mux.HandleFunc("/stream/", s.handleCameraStream)
	
	return mux
}

// startHTTPServer starts the HTTP server
func (s *Server) startHTTPServer() error {
	mux := s.setupRoutes()
	
	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.config.LocalHTTPPort),
		Handler: mux,
	}

	s.logger.Printf("Starting HTTP server on port %d", s.config.LocalHTTPPort)
	
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Printf("HTTP server error: %v", err)
		}
	}()

	return nil
}

// createSystemSSHTunnel creates SSH tunnel using system ssh command (fallback method)
func (s *Server) createSystemSSHTunnel() error {
	s.logger.Println("Creating SSH tunnel using system ssh command...")
	
	keyPath := s.expandPath(s.config.SSHKeyPath)
	
	// Build SSH command similar to manual tunnel
	sshCmd := exec.CommandContext(s.ctx,
		"ssh",
		"-i", keyPath,
		"-R", fmt.Sprintf("0.0.0.0:%d:localhost:%d", s.config.VPSHTTPPort, s.config.LocalHTTPPort),
		"-N",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StrictHostKeyChecking=no",
		fmt.Sprintf("%s@%s", s.config.VPSUser, s.config.VPSHost),
	)
	
	s.logger.Printf("SSH command: %s", strings.Join(sshCmd.Args, " "))
	
	// Start SSH process
	if err := sshCmd.Start(); err != nil {
		return fmt.Errorf("failed to start SSH command: %v", err)
	}
	
	// Give it time to establish
	time.Sleep(3 * time.Second)
	
	// Check if process is still running
	if sshCmd.Process != nil {
		s.logger.Printf("System SSH tunnel started with PID: %d", sshCmd.Process.Pid)
		s.logger.Printf("Multi-camera viewer should be accessible at: http://%s:%d", s.config.VPSHost, s.config.VPSHTTPPort)
		
		// Monitor the process
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			err := sshCmd.Wait()
			if err != nil && s.ctx.Err() == nil {
				s.logger.Printf("SSH tunnel process exited: %v", err)
			}
		}()
		
		return nil
	}
	
	return fmt.Errorf("SSH tunnel process failed to start")
}
func (s *Server) createSSHTunnel() error {
	var authMethods []ssh.AuthMethod

	// Try SSH agent first
	if sshAgent, err := net.Dial("unix", os.Getenv("SSH_AUTH_SOCK")); err == nil {
		authMethods = append(authMethods, ssh.PublicKeysCallback(agent.NewClient(sshAgent).Signers))
		s.logger.Println("Using SSH agent for authentication")
	}

	// Fallback to key file
	keyPath := s.expandPath(s.config.SSHKeyPath)
	if privateKey, err := s.parseSSHKey(keyPath); err == nil {
		authMethods = append(authMethods, ssh.PublicKeys(privateKey))
		s.logger.Println("Using SSH key file for authentication")
	} else {
		if len(authMethods) == 0 {
			return fmt.Errorf("no valid authentication methods: %v", err)
		}
		s.logger.Printf("SSH key file failed, using agent only: %v", err)
	}

	// SSH client configuration
	sshConfig := &ssh.ClientConfig{
		User:            s.config.VPSUser,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	// Connect to SSH server
	sshAddr := fmt.Sprintf("%s:%d", s.config.VPSHost, s.config.VPSPort)
	s.logger.Printf("Connecting to SSH server: %s", sshAddr)
	
	client, err := ssh.Dial("tcp", sshAddr, sshConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to SSH server: %v", err)
	}

	s.sshClient = client
	s.logger.Println("SSH connection established")

	// Create reverse tunnel - use the same format as manual SSH: -R 0.0.0.0:port:localhost:port
	// Try different remote address formats
	remoteAddresses := []string{
		fmt.Sprintf("0.0.0.0:%d", s.config.VPSHTTPPort),
		fmt.Sprintf(":%d", s.config.VPSHTTPPort),
		fmt.Sprintf("*:%d", s.config.VPSHTTPPort),
	}
	
	localAddr := fmt.Sprintf("127.0.0.1:%d", s.config.LocalHTTPPort)
	
	var listener net.Listener
	
	// Try each remote address format until one works
	for i, remoteAddr := range remoteAddresses {
		s.logger.Printf("Attempt %d: Creating reverse tunnel %s -> %s", i+1, remoteAddr, localAddr)
		
		listener, err = client.Listen("tcp", remoteAddr)
		if err != nil {
			s.logger.Printf("Attempt %d failed: %v", i+1, err)
			continue
		}
		
		s.logger.Printf("Success! Remote listener created with address format: %s", remoteAddr)
		break
	}
	
	if listener == nil {
		s.logger.Printf("All tunnel creation attempts failed.")
		s.logger.Printf("Manual tunnel works, so this suggests a difference in how Go SSH client handles remote binding.")
		s.logger.Printf("Try checking if the Go application has the same SSH permissions as your manual SSH.")
		return fmt.Errorf("failed to create remote listener with any address format")
	}

	s.logger.Printf("SSH tunnel established successfully!")
	s.logger.Printf("Remote listener bound to: %s", listener.Addr().String())
	s.logger.Printf("Multi-camera viewer should be accessible at: http://%s:%d", s.config.VPSHost, s.config.VPSHTTPPort)

	// Handle incoming connections
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer listener.Close()

		for {
			select {
			case <-s.ctx.Done():
				return
			default:
				conn, err := listener.Accept()
				if err != nil {
					if s.ctx.Err() == nil {
						s.logger.Printf("Failed to accept connection: %v", err)
					}
					continue
				}

				s.logger.Printf("New connection from: %s", conn.RemoteAddr().String())
				go s.handleTunnelConnection(conn, localAddr)
			}
		}
	}()

	return nil
}

// handleTunnelConnection handles incoming tunnel connections
func (s *Server) handleTunnelConnection(remoteConn net.Conn, localAddr string) {
	defer remoteConn.Close()

	s.logger.Printf("Handling connection from %s -> %s", remoteConn.RemoteAddr().String(), localAddr)

	// Connect to local HTTP server with timeout
	localConn, err := net.DialTimeout("tcp", localAddr, 10*time.Second)
	if err != nil {
		s.logger.Printf("Failed to connect to local server %s: %v", localAddr, err)
		return
	}
	defer localConn.Close()

	s.logger.Printf("Connected to local server, starting data transfer")

	// Bidirectional copy with error handling
	done := make(chan error, 2)

	go func() {
		_, err := io.Copy(localConn, remoteConn)
		done <- err
	}()

	go func() {
		_, err := io.Copy(remoteConn, localConn)
		done <- err
	}()

	// Wait for either direction to complete or error
	err = <-done
	if err != nil {
		s.logger.Printf("Connection transfer error: %v", err)
	} else {
		s.logger.Printf("Connection completed successfully")
	}
}

// monitorSSHTunnel monitors SSH tunnel and reconnects if needed
func (s *Server) monitorSSHTunnel() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if s.sshClient != nil {
				// Send keepalive
				_, _, err := s.sshClient.SendRequest("keepalive@openssh.com", true, nil)
				if err != nil {
					s.logger.Printf("SSH tunnel disconnected: %v", err)
					s.logger.Println("Attempting to reconnect...")
					
					s.sshClient.Close()
					time.Sleep(5 * time.Second)
					
					if err := s.createSSHTunnel(); err != nil {
						s.logger.Printf("Failed to reconnect SSH tunnel: %v", err)
					} else {
						s.logger.Println("SSH tunnel reconnected successfully")
					}
				}
			}
		}
	}
}

// Start starts the server
func (s *Server) Start() error {
	s.logger.Println("Starting Multi-Camera HTTP Streaming SSH Tunnel Service")
	s.logger.Printf("Cameras configured: %d", len(s.config.Cameras))
	
	for _, camera := range s.config.Cameras {
		s.logger.Printf("  - %s: %s", camera.Name, camera.Description)
	}

	s.logger.Printf("Local HTTP: localhost:%d", s.config.LocalHTTPPort)
	s.logger.Printf("VPS: %s@%s:%d", s.config.VPSUser, s.config.VPSHost, s.config.VPSPort)
	s.logger.Printf("Public access: http://%s:%d", s.config.VPSHost, s.config.VPSHTTPPort)

	// Check dependencies
	if !s.checkFFmpeg() {
		return fmt.Errorf("FFmpeg not found")
	}

	// Test cameras
	workingCameras := s.testCameras()
	if len(workingCameras) == 0 {
		return fmt.Errorf("no cameras are accessible")
	}
	s.logger.Printf("Found %d working cameras", len(workingCameras))

	// Start HTTP server
	if err := s.startHTTPServer(); err != nil {
		return fmt.Errorf("failed to start HTTP server: %v", err)
	}

	// Give HTTP server a moment to start
	time.Sleep(2 * time.Second)

	// Test local HTTP server before creating tunnel
	if err := s.testLocalHTTPServer(); err != nil {
		return fmt.Errorf("local HTTP server test failed: %v", err)
	}

	// Create SSH tunnel - try Go SSH client first, fallback to system ssh
	if err := s.createSSHTunnel(); err != nil {
		s.logger.Printf("Go SSH client failed: %v", err)
		s.logger.Println("Trying system SSH command as fallback...")
		
		if err := s.createSystemSSHTunnel(); err != nil {
			return fmt.Errorf("both Go SSH client and system SSH failed: %v", err)
		}
	}

	// Start monitoring
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.monitorSSHTunnel()
	}()

	s.logger.Println(strings.Repeat("=", 60))
	s.logger.Println("🎥 MULTI-CAMERA SYSTEM READY!")
	s.logger.Println(strings.Repeat("=", 60))
	s.logger.Printf("📱 Main viewer: http://%s:%d", s.config.VPSHost, s.config.VPSHTTPPort)
	s.logger.Printf("🎯 API endpoint: http://%s:%d/api/cameras", s.config.VPSHost, s.config.VPSHTTPPort)
	s.logger.Println("")
	s.logger.Println("Individual camera streams:")
	for cameraID, camera := range s.config.Cameras {
		s.logger.Printf("  📹 %s: http://%s:%d/stream/%s", camera.Name, s.config.VPSHost, s.config.VPSHTTPPort, cameraID)
	}
	s.logger.Println(strings.Repeat("=", 60))

	return nil
}

// Stop stops the server
func (s *Server) Stop() {
	s.logger.Println("Stopping server...")
	
	s.cancel()

	if s.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.httpServer.Shutdown(ctx)
	}

	if s.sshClient != nil {
		s.sshClient.Close()
	}

	s.wg.Wait()
	s.logger.Println("Server stopped")
}

// saveConfig saves configuration to file
func saveConfig(config *Config, filename string) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0644)
}

// loadConfig loads configuration from file
func loadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var config Config
	err = json.Unmarshal(data, &config)
	return &config, err
}

func main() {
	configFile := "camera_config.json"

	// Load or create config
	config, err := loadConfig(configFile)
	if err != nil {
		log.Printf("Config file not found, creating default: %s", configFile)
		config = getDefaultConfig()
		if err := saveConfig(config, configFile); err != nil {
			log.Fatalf("Failed to save config: %v", err)
		}
		log.Printf("Please edit %s with your actual configuration and restart", configFile)
		return
	}

	// Create server
	server := NewServer(config)

	// Handle graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-c
		log.Println("Received interrupt signal")
		server.Stop()
		os.Exit(0)
	}()

	// Start server
	if err := server.Start(); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}

	// Keep running
	select {}
}
