package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	DeviceID       string `yaml:"device_id"`
	NATSURL        string `yaml:"nats_url"`
	ChunkSize      int    `yaml:"chunk_size"`
	TokenTTL       int    `yaml:"token_ttl"`
	PollIntervalMS int    `yaml:"poll_interval_ms"`
	CacheDir       string `yaml:"cache_dir"`
	DownloadDir    string `yaml:"download_dir"`
	MountDir       string `yaml:"mount_dir"`
	LogLevel       string `yaml:"log_level"`
}

func Load(path string) (Config, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(payload, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config yaml: %w", err)
	}
	applyDefaults(&cfg)
	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.NATSURL == "" {
		cfg.NATSURL = "nats://localhost:4222"
	}
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = 8 * 1024 * 1024
	}
	if cfg.TokenTTL <= 0 {
		cfg.TokenTTL = 60
	}
	if cfg.PollIntervalMS <= 0 {
		cfg.PollIntervalMS = 500
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = filepath.Join(os.TempDir(), "clipboard-sync", "cache")
	}
	if cfg.DownloadDir == "" {
		cfg.DownloadDir = filepath.Join(os.TempDir(), "clipboard-sync", "downloads")
	}
	if cfg.MountDir == "" {
		cfg.MountDir = filepath.Join(os.TempDir(), "clipboard-sync", "mount")
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
}

func validate(cfg Config) error {
	if cfg.ChunkSize < 64*1024 {
		return fmt.Errorf("chunk_size must be >= 65536")
	}
	if cfg.TokenTTL <= 0 {
		return fmt.Errorf("token_ttl must be > 0")
	}
	if cfg.PollIntervalMS < 100 {
		return fmt.Errorf("poll_interval_ms must be >= 100")
	}
	return nil
}

func (c Config) TokenTTLDuration() time.Duration {
	return time.Duration(c.TokenTTL) * time.Second
}

func (c Config) PollInterval() time.Duration {
	return time.Duration(c.PollIntervalMS) * time.Millisecond
}
