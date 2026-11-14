package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Config holds all server, scanner, scraper, and privacy settings.
type Config struct {
	mu sync.RWMutex `json:"-"`

	ListenAddr           string   `json:"listen_addr"`
	DataDir              string   `json:"data_dir"`
	AuthEnabled          bool     `json:"auth_enabled"`
	DefaultHideNSFW      bool     `json:"default_hide_nsfw"`
	ScraperLocale        string   `json:"scraper_locale"`
	ScraperIntervalSec   float64  `json:"scraper_interval_sec"`
	ScraperProxy         string   `json:"scraper_proxy"`
	ScraperUserAgent     string   `json:"scraper_user_agent"`
	DLsiteBaseURL        string   `json:"dlsite_base_url,omitempty"`
	ScanMaxDepth         int      `json:"scan_max_depth"`
	ScanIgnorePatterns   []string `json:"scan_ignore_patterns"`
	HistoryEnabled       bool     `json:"history_enabled"`
	HistoryMaxDays       int      `json:"history_max_days"`
	HistoryMaxEntries    int      `json:"history_max_entries"`
	HeartbeatIntervalSec int      `json:"heartbeat_interval_sec"`
	SessionMergeSec      int      `json:"session_merge_sec"`
	WebDAVEnabled        bool     `json:"webdav_enabled"`
}

// Default returns conservative, offline-friendly defaults (§3.3, §3.4, §9.5, §9.6).
func Default(dataDir string) *Config {
	if dataDir == "" {
		dataDir = "data"
	}
	return &Config{
		ListenAddr:         "127.0.0.1:8090",
		DataDir:            dataDir,
		AuthEnabled:        false,
		DefaultHideNSFW:    true, // §3.4 & §12: hide R15/R18 by default unless toggled
		ScraperLocale:      "zh_cn",
		ScraperIntervalSec: 3.0,
		ScraperProxy:       "",
		ScraperUserAgent:   "Mozilla/5.0 (compatible; Otoha/1.0; +https://github.com/SiIverAsh/Otoha)",
		DLsiteBaseURL:      "https://www.dlsite.com",
		ScanMaxDepth:       6,
		ScanIgnorePatterns: []string{
			"_*",
			".*",
			"@eaDir",
			"#recycle",
			"$RECYCLE.BIN",
			"System Volume Information",
		},
		HistoryEnabled:       true,
		HistoryMaxDays:       90,
		HistoryMaxEntries:    500,
		HeartbeatIntervalSec: 30,
		SessionMergeSec:      1800, // 30 minutes
		WebDAVEnabled:        true,
	}
}

func (c *Config) ConfigFilePath() string {
	return filepath.Join(c.DataDir, "config.json")
}

func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, "otoha.db")
}

func (c *Config) BlevePath() string {
	return filepath.Join(c.DataDir, "index.bleve")
}

func (c *Config) CoversDir() string {
	return filepath.Join(c.DataDir, "covers")
}

func (c *Config) CacheDir() string {
	return filepath.Join(c.DataDir, "cache", "dlsite")
}

// Load loads config from <dataDir>/config.json if present, merging over defaults and env vars.
func Load(dataDir string) (*Config, error) {
	cfg := Default(dataDir)
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	path := cfg.ConfigFilePath()
	if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, cfg); err != nil {
			return nil, err
		}
		cfg.DataDir = dataDir
	}
	cfg.applyEnv()
	if cfg.DLsiteBaseURL == "" {
		cfg.DLsiteBaseURL = "https://www.dlsite.com"
	}
	if cfg.HeartbeatIntervalSec <= 0 {
		cfg.HeartbeatIntervalSec = 30
	}
	if cfg.SessionMergeSec <= 0 {
		cfg.SessionMergeSec = 1800
	}
	if cfg.ScanMaxDepth <= 0 {
		cfg.ScanMaxDepth = 6
	}
	return cfg, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("OTOHA_ADDR"); v != "" {
		c.ListenAddr = v
	}
	if v := os.Getenv("OTOHA_PROXY"); v != "" {
		c.ScraperProxy = v
	}
	if v := os.Getenv("OTOHA_AUTH"); v != "" {
		c.AuthEnabled = strings.EqualFold(v, "1") || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("OTOHA_LOCALE"); v != "" {
		c.ScraperLocale = v
	}
	if v := os.Getenv("OTOHA_HIDE_NSFW"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			c.DefaultHideNSFW = b
		}
	}
}

// Save atomically persists configuration to <dataDir>/config.json.
func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.ConfigFilePath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.ConfigFilePath())
}

// Snapshot returns a value copy safe for concurrent readers.
func (c *Config) Snapshot() Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cp := *c
	cp.ScanIgnorePatterns = append([]string(nil), c.ScanIgnorePatterns...)
	return cp
}

// Update applies mutator under write lock and persists to disk.
func (c *Config) Update(fn func(*Config)) error {
	c.mu.Lock()
	fn(c)
	c.mu.Unlock()
	return c.Save()
}
