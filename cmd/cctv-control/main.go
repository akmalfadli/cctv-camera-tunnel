package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"camera-tunnel/internal/api"
	"camera-tunnel/internal/auth"
	"camera-tunnel/internal/camera"
	"camera-tunnel/internal/config"
	"camera-tunnel/internal/ffmpeg"
	"camera-tunnel/internal/hls"
)

func main() {
	configPath := flag.String("config", "camera_config.json", "Path to configuration file")
	flag.Parse()

	// 1. Load Configuration
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 2. Initialize Components
	authMgr := auth.NewAuthenticator(cfg)
	camRegistry := camera.NewRegistry(cfg)
	streamMgr := ffmpeg.NewStreamManager(cfg)
	hlsMgr := hls.NewManager(cfg)
	
	// 3. Ensure Directories
	if err := hlsMgr.EnsureDirectory(); err != nil {
		log.Fatalf("Failed to create HLS directory: %v", err)
	}

	// 4. Start FFmpeg Supervisor
	streamMgr.StartAll()
	hlsMgr.Cleanup() // Start background cleanup

	// 5. Start API & Web Server
	server := api.NewServer(cfg, camRegistry, streamMgr, authMgr)
	if err := server.LoadTemplates(); err != nil {
		log.Fatalf("Failed to load templates: %v", err)
	}

	go func() {
		log.Printf("Starting HTTP server on :%d", cfg.ServerPort)
		if err := http.ListenAndServe(fmt.Sprintf(":%d", cfg.ServerPort), server.Routes()); err != nil {
			log.Fatalf("HTTP Server failed: %v", err)
		}
	}()

	// 6. Wait for Shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")
	streamMgr.StopAll()
	log.Println("Bye!")
}
