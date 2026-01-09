package config

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

type Config struct {
	mu sync.RWMutex
	db *sql.DB

	ServerPort      int
	MetricsPort     int
	HLSOutputRoot   string
	SegmentDuration int
	PlaylistSize    int
	Auth            AuthConfig
	Cameras         map[string]Camera
}

type AuthConfig struct {
	Username     string
	PasswordHash string
	JWTSecret    string
}

type AppSettings struct {
	ServerPort      int    `json:"server_port"`
	HLSOutputRoot   string `json:"hls_output_root"`
	SegmentDuration int    `json:"segment_duration"`
	PlaylistSize    int    `json:"playlist_size"`
	AuthUsername    string `json:"auth_username"`
	NewPassword     string `json:"new_password"`
}

type Camera struct {
	ID          string
	Name        string
	Description string
	Enabled     bool
	RTSPURL     string `json:"rtsp_url,omitempty"`
	RTSPURLEnv  string `json:"rtsp_url_env,omitempty"`
	FPS         int
	Bitrate     string
	GOP         int
}

const settingsRowID = 1

func LoadConfig(dbPath string) (*Config, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_busy_timeout=5000&_foreign_keys=on", dbPath))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	cfg := &Config{db: db, Cameras: make(map[string]Camera)}
	if err := cfg.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	if err := cfg.ensureSeed(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	if err := cfg.ensureUsers(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	if err := cfg.refresh(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return cfg, nil
}

func (c *Config) migrate(ctx context.Context) error {
	stmts := []string{
		"PRAGMA journal_mode=WAL;",
		`CREATE TABLE IF NOT EXISTS app_settings (
            id INTEGER PRIMARY KEY CHECK (id = 1),
            server_port INTEGER NOT NULL,
            metrics_port INTEGER NOT NULL,
            hls_output_root TEXT NOT NULL,
            segment_duration INTEGER NOT NULL,
            playlist_size INTEGER NOT NULL,
            auth_username TEXT NOT NULL,
            password_hash TEXT NOT NULL,
            jwt_secret TEXT NOT NULL
        );`,
		`CREATE TABLE IF NOT EXISTS cameras (
            id TEXT PRIMARY KEY,
            name TEXT NOT NULL,
            description TEXT,
            enabled INTEGER NOT NULL DEFAULT 1,
            rtsp_env TEXT,
            rtsp_url TEXT,
            fps INTEGER,
            bitrate TEXT,
            gop INTEGER,
            created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
            updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
        );`,
		`CREATE TRIGGER IF NOT EXISTS cameras_updated_at
        AFTER UPDATE ON cameras
        FOR EACH ROW BEGIN
            UPDATE cameras SET updated_at = CURRENT_TIMESTAMP WHERE id = OLD.id;
        END;`,
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'admin',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TRIGGER IF NOT EXISTS users_updated_at
		AFTER UPDATE ON users
		FOR EACH ROW BEGIN
			UPDATE users SET updated_at = CURRENT_TIMESTAMP WHERE id = OLD.id;
		END;`,
	}
	for _, stmt := range stmts {
		if _, err := c.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	if err := c.ensureCameraColumns(ctx); err != nil {
		return err
	}
	return nil
}

func (c *Config) ensureCameraColumns(ctx context.Context) error {
	rows, err := c.db.QueryContext(ctx, `PRAGMA table_info(cameras)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns := make(map[string]struct{})
	for rows.Next() {
		var (
			cid          int
			name         string
			typeStr      string
			notnull      int
			defaultValue interface{}
			pk           int
		)
		if err := rows.Scan(&cid, &name, &typeStr, &notnull, &defaultValue, &pk); err != nil {
			return err
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	needed := []struct {
		name string
		sql  string
	}{
		{"rtsp_env", "ALTER TABLE cameras ADD COLUMN rtsp_env TEXT"},
		{"rtsp_url", "ALTER TABLE cameras ADD COLUMN rtsp_url TEXT"},
		{"fps", "ALTER TABLE cameras ADD COLUMN fps INTEGER"},
		{"bitrate", "ALTER TABLE cameras ADD COLUMN bitrate TEXT"},
		{"gop", "ALTER TABLE cameras ADD COLUMN gop INTEGER"},
	}
	for _, col := range needed {
		if _, exists := columns[col.name]; exists {
			continue
		}
		if _, err := c.db.ExecContext(ctx, col.sql); err != nil {
			return fmt.Errorf("add column %s: %w", col.name, err)
		}
	}
	return nil
}

func (c *Config) ensureSeed(ctx context.Context) error {
	var exists int
	if err := c.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM app_settings").Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}

	serverPort := 8080
	metricsPort := 9090
	hlsOutputRoot := "./hls"
	segmentDuration := 2
	playlistSize := 5
	username := os.Getenv("CCTV_ADMIN_USERNAME")
	if username == "" {
		username = "admin"
	}

	passwordHash := os.Getenv("CCTV_ADMIN_PASSWORD_HASH")
	usedDefault := false
	if passwordHash == "" {
		if plain := os.Getenv("CCTV_ADMIN_PASSWORD"); plain != "" {
			if hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost); err == nil {
				passwordHash = string(hash)
			}
		}
	}
	if passwordHash == "" {
		hash, err := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		passwordHash = string(hash)
		usedDefault = true
	}

	jwtSecret := os.Getenv("CCTV_JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = fmt.Sprintf("secret-%d", time.Now().Unix())
	}

	_, err := c.db.ExecContext(ctx, `INSERT INTO app_settings (
        id, server_port, metrics_port, hls_output_root, segment_duration, playlist_size, auth_username, password_hash, jwt_secret)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		settingsRowID, serverPort, metricsPort, hlsOutputRoot,
		segmentDuration, playlistSize, username, passwordHash, jwtSecret,
	)
	if err != nil {
		return err
	}
	if usedDefault {
		log.Println("Initialized default admin credentials (admin/admin). Please change them via the admin console.")
	}
	return nil
}

func (c *Config) ensureUsers(ctx context.Context) error {
	var count int
	if err := c.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	var username, passwordHash string
	err := c.db.QueryRowContext(ctx, `SELECT auth_username, password_hash FROM app_settings WHERE id = ?`, settingsRowID).Scan(&username, &passwordHash)
	if err != nil {
		username = "admin"
		passwordHash = ""
	}
	username = strings.TrimSpace(username)
	if username == "" {
		username = "admin"
	}
	if passwordHash == "" {
		hash, err := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		passwordHash = string(hash)
		log.Println("Initialized default admin credentials (admin/admin). Please change them via the admin console.")
	}

	_, err = c.db.ExecContext(ctx, `INSERT INTO users (username, password_hash, role) VALUES (?, ?, 'admin')`, username, passwordHash)
	return err
}

func (c *Config) refresh(ctx context.Context) error {
	row := c.db.QueryRowContext(ctx, `SELECT server_port, metrics_port, hls_output_root, segment_duration, playlist_size, auth_username, password_hash, jwt_secret FROM app_settings WHERE id = ?`, settingsRowID)
	var cfg Config
	var auth AuthConfig
	var legacyUsername, legacyHash string
	if err := row.Scan(&cfg.ServerPort, &cfg.MetricsPort, &cfg.HLSOutputRoot, &cfg.SegmentDuration, &cfg.PlaylistSize, &legacyUsername, &legacyHash, &auth.JWTSecret); err != nil {
		return err
	}
	if err := c.loadAuthFromUsers(ctx, &auth); err != nil {
		return err
	}
	if auth.Username == "" {
		auth.Username = legacyUsername
	}
	if auth.PasswordHash == "" {
		auth.PasswordHash = legacyHash
	}
	cfg.Auth = auth

	rows, err := c.db.QueryContext(ctx, `SELECT id, name, COALESCE(description,''), enabled, COALESCE(rtsp_env,''), COALESCE(rtsp_url,''), COALESCE(fps,0), COALESCE(bitrate,''), COALESCE(gop,0) FROM cameras ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	camMap := make(map[string]Camera)
	for rows.Next() {
		var cam Camera
		var enabled int
		if err := rows.Scan(&cam.ID, &cam.Name, &cam.Description, &enabled, &cam.RTSPURLEnv, &cam.RTSPURL, &cam.FPS, &cam.Bitrate, &cam.GOP); err != nil {
			return err
		}
		cam.Enabled = enabled == 1
		camMap[cam.ID] = cam
	}
	if err := rows.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	c.ServerPort = cfg.ServerPort
	c.MetricsPort = cfg.MetricsPort
	c.HLSOutputRoot = cfg.HLSOutputRoot
	c.SegmentDuration = cfg.SegmentDuration
	c.PlaylistSize = cfg.PlaylistSize
	c.Auth = cfg.Auth
	c.Cameras = camMap
	c.mu.Unlock()
	return nil
}

func (c *Config) loadAuthFromUsers(ctx context.Context, auth *AuthConfig) error {
	row := c.db.QueryRowContext(ctx, `SELECT username, password_hash FROM users ORDER BY id LIMIT 1`)
	switch err := row.Scan(&auth.Username, &auth.PasswordHash); err {
	case nil:
		return nil
	case sql.ErrNoRows:
		return nil
	default:
		return err
	}
}

func (c *Config) SettingsSnapshot() AppSettings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return AppSettings{
		ServerPort:      c.ServerPort,
		HLSOutputRoot:   c.HLSOutputRoot,
		SegmentDuration: c.SegmentDuration,
		PlaylistSize:    c.PlaylistSize,
		AuthUsername:    c.Auth.Username,
	}
}

func (c *Config) UpdateSettings(s AppSettings) error {
	if err := validateSettings(s); err != nil {
		return err
	}
	currentUser, currentHash, _ := c.Credentials()
	username := strings.TrimSpace(s.AuthUsername)
	if username == "" {
		username = currentUser
	}
	if username == "" {
		return errors.New("auth username required")
	}
	passwordHash := currentHash
	if s.NewPassword != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(s.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		passwordHash = string(hash)
	}
	if err := os.MkdirAll(s.HLSOutputRoot, 0o755); err != nil {
		return fmt.Errorf("ensure hls root: %w", err)
	}
	tx, err := c.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(context.Background(), `UPDATE app_settings SET
		server_port = ?,
		auth_username = ?,
		password_hash = ?,
		hls_output_root = ?,
		segment_duration = ?,
		playlist_size = ?
	WHERE id = ?`,
		s.ServerPort, username, passwordHash, s.HLSOutputRoot, s.SegmentDuration, s.PlaylistSize, settingsRowID,
	); err != nil {
		return err
	}
	var userID int
	switch err = tx.QueryRowContext(context.Background(), `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&userID); err {
	case nil:
		if _, err = tx.ExecContext(context.Background(), `UPDATE users SET username = ?, password_hash = ? WHERE id = ?`, username, passwordHash, userID); err != nil {
			return err
		}
	case sql.ErrNoRows:
		if _, err = tx.ExecContext(context.Background(), `INSERT INTO users (username, password_hash, role) VALUES (?, ?, 'admin')`, username, passwordHash); err != nil {
			return err
		}
	default:
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return c.refresh(context.Background())
}

func validateSettings(s AppSettings) error {
	if s.ServerPort < 80 || s.ServerPort > 65535 {
		return fmt.Errorf("invalid server port")
	}
	if s.HLSOutputRoot == "" {
		return errors.New("hls output root required")
	}
	if s.SegmentDuration <= 0 {
		return errors.New("segment duration must be positive")
	}
	if s.PlaylistSize <= 0 {
		return errors.New("playlist size must be positive")
	}
	return nil
}

func (c *Config) upsertCamera(ctx context.Context, cam Camera) error {
	if cam.ID == "" {
		return errors.New("camera id required")
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO cameras (id, name, description, enabled, rtsp_env, rtsp_url, fps, bitrate, gop)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
        name=excluded.name,
        description=excluded.description,
        enabled=excluded.enabled,
        rtsp_env=excluded.rtsp_env,
        rtsp_url=excluded.rtsp_url,
        fps=excluded.fps,
        bitrate=excluded.bitrate,
        gop=excluded.gop`,
		cam.ID, cam.Name, cam.Description, boolToInt(cam.Enabled), nullableString(cam.RTSPURLEnv), nullableString(cam.RTSPURL), nullIfZero(cam.FPS), nullableString(cam.Bitrate), nullIfZero(cam.GOP))
	return err
}

func (c *Config) UpsertCamera(cam Camera) error {
	if err := c.upsertCamera(context.Background(), cam); err != nil {
		return err
	}
	c.mu.Lock()
	c.Cameras[cam.ID] = cam
	c.mu.Unlock()
	return nil
}

func (c *Config) DeleteCamera(id string) error {
	if _, err := c.db.Exec(`DELETE FROM cameras WHERE id = ?`, id); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.Cameras, id)
	c.mu.Unlock()
	return nil
}

func (c *Config) Credentials() (string, string, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Auth.Username, c.Auth.PasswordHash, c.Auth.JWTSecret
}

func (c *Config) Close() error {
	return c.db.Close()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullIfZero(v int) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func (c Camera) EffectiveRTSP() (string, error) {
	if c.RTSPURLEnv != "" {
		if val := os.Getenv(c.RTSPURLEnv); val != "" {
			return val, nil
		}
		return "", fmt.Errorf("rtsp env %s is not set", c.RTSPURLEnv)
	}
	if c.RTSPURL != "" {
		return c.RTSPURL, nil
	}
	return "", fmt.Errorf("camera %s missing rtsp url", c.ID)
}

func (c Camera) EffectiveFPS(defaultFPS int) int {
	if c.FPS > 0 {
		return c.FPS
	}
	return defaultFPS
}

func (c Camera) EffectiveGOP(defaultGOP int) int {
	if c.GOP > 0 {
		return c.GOP
	}
	return defaultGOP
}

func (c Camera) SanitizedCopy() Camera {
	return c
}
