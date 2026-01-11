package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config represents the application configuration
type Config struct {
	Immich      *ImmichConfig `yaml:"immich,omitempty"`
	DefaultUser string        `yaml:"default_user,omitempty"`
	Sync        *SyncConfig   `yaml:"sync,omitempty"`
}

// ImmichConfig holds Immich server connection details
type ImmichConfig struct {
	URL    string `yaml:"url"`
	APIKey string `yaml:"api_key"`
}

// SyncConfig holds continuous sync settings
type SyncConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"`
}

// DefaultConfigPath returns the default config file path following XDG spec
func DefaultConfigPath() string {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "whence", "config.yaml")
}

// LoadConfig loads configuration from the specified path
// Returns nil config (not error) if file doesn't exist
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Config file is optional
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return &cfg, nil
}

// ImmichConfigured returns true if Immich is configured
func (c *Config) ImmichConfigured() bool {
	return c != nil && c.Immich != nil && c.Immich.URL != "" && c.Immich.APIKey != ""
}
