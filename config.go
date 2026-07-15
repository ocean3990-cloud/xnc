package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// AI-assisted reading is optional. When an Anthropic API key is present the app
// sends the passport / table image to a vision model (the same way you get an
// accurate read when you hand the image to Claude directly) and uses the local
// Tesseract / Windows OCR pipeline only as a fallback. The key lives on the
// user's machine (env var or config.json) and is never written into the source.

const aiDefaultModel = "claude-opus-4-8"

type appConfig struct {
	APIKey string `json:"apiKey"`
	Model  string `json:"model"`
}

var (
	cfgMu     sync.Mutex
	cfgLoaded bool
	cfgCache  appConfig
)

// configPath is a per-user file next to the log/diagnostic folder.
func configPath() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = filepath.Join(os.TempDir(), "XNC Ocean")
	} else {
		base = filepath.Join(base, "XNC Ocean")
	}
	return filepath.Join(base, "config.json")
}

func loadConfig() appConfig {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if cfgLoaded {
		return cfgCache
	}
	cfgLoaded = true
	if b, err := os.ReadFile(configPath()); err == nil {
		_ = json.Unmarshal(b, &cfgCache)
	}
	return cfgCache
}

func saveConfig(c appConfig) error {
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.Model = strings.TrimSpace(c.Model)
	p := configPath()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, b, 0600); err != nil {
		return err
	}
	cfgMu.Lock()
	cfgCache = c
	cfgLoaded = true
	cfgMu.Unlock()
	return nil
}

// aiAPIKey resolves the key: the environment variable wins over config.json so a
// machine-wide key can override a stored one.
func aiAPIKey() string {
	if k := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")); k != "" {
		return k
	}
	return strings.TrimSpace(loadConfig().APIKey)
}

func aiModel() string {
	if m := strings.TrimSpace(loadConfig().Model); m != "" {
		return m
	}
	return aiDefaultModel
}

func aiEnabled() bool { return aiAPIKey() != "" }

func apiKeySourceFromEnv() bool {
	return strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY")) != ""
}
