package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

type Config struct {
	mu sync.RWMutex `json:"-"`
	filename string `json:"-"`

	// Server Configuration
	ServerPort    int `json:"server_port"`
	MetricsPort   int `json:"metrics_port"`

	// Auth Configuration
	Auth AuthConfig `json:"auth"`

	// HLS Configuration
	HLSOutputRoot string `json:"hls_output_root"`
	SegmentDuration int `json:"segment_duration"` // Seconds
	PlaylistSize    int `json:"playlist_size"`    // Number of segments in playlist
	
	// Camera Configuration
	Cameras map[string]Camera `json:"cameras"`
	
	// Nginx Configuration
	NginxConfigPath string `json:"nginx_config_path"`
}

type AuthConfig struct {
	Username  string `json:"username"`
	Password  string `json:"password"` // Plain text for simplicity in this demo, ideally hashed
	JWTSecret string `json:"jwt_secret"`
}

type Camera struct {
	ID          string `json:"id,omitempty"` // Helper field, not usually in JSON map value
	Name        string `json:"name"`
	RTSPURL     string `json:"rtsp_url"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	
	// FFmpeg Overrides (optional)
	FPS         int    `json:"fps,omitempty"`
	Bitrate     string `json:"bitrate,omitempty"` // e.g., "2048k"
}

// LoadConfig loads configuration from file
func LoadConfig(filename string) (*Config, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	config.filename = filename

	// Set defaults
	if config.ServerPort == 0 {
		config.ServerPort = 8080
	}
	if config.HLSOutputRoot == "" {
		config.HLSOutputRoot = "./hls"
	}
	if config.SegmentDuration == 0 {
		config.SegmentDuration = 2
	}
	if config.PlaylistSize == 0 {
		config.PlaylistSize = 5
	}
	// Default auth if missing
	if config.Auth.Username == "" {
		config.Auth.Username = "admin"
		config.Auth.Password = "admin" // Change me!
		config.Auth.JWTSecret = "change-me-to-a-secure-secret"
	}

	return &config, nil
}

func (c *Config) Save() error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.filename, data, 0644)
}

func (c *Config) UpdateCamera(id string, cam Camera) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Cameras == nil {
		c.Cameras = make(map[string]Camera)
	}
	c.Cameras[id] = cam
}

func (c *Config) DeleteCamera(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.Cameras, id)
}

func (c *Config) UpdateSettings(serverPort int, duration int, playlist int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ServerPort = serverPort
	c.SegmentDuration = duration
	c.PlaylistSize = playlist
}
