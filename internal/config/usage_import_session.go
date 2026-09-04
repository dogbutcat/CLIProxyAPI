package config

import "fmt"

type UsageImportSessionConfig struct {
	Dir             string `yaml:"dir,omitempty" json:"dir,omitempty"`
	ChunkSizeBytes  int64  `yaml:"chunk-size-bytes" json:"chunk-size-bytes"`
	MaxSessionBytes int64  `yaml:"max-session-bytes" json:"max-session-bytes"`
	MaxActive       int    `yaml:"max-active" json:"max-active"`
	TTLMinutes      int    `yaml:"ttl-minutes" json:"ttl-minutes"`
}

func DefaultUsageImportSessionConfig() UsageImportSessionConfig {
	return UsageImportSessionConfig{
		ChunkSizeBytes:  DefaultUsageImportSessionChunkSizeBytes,
		MaxSessionBytes: DefaultUsageImportSessionMaxBytes,
		MaxActive:       DefaultUsageImportSessionMaxActive,
		TTLMinutes:      DefaultUsageImportSessionTTLMinutes,
	}
}

func (c UsageImportSessionConfig) WithDefaults() UsageImportSessionConfig {
	defaults := DefaultUsageImportSessionConfig()
	if c.ChunkSizeBytes == 0 {
		c.ChunkSizeBytes = defaults.ChunkSizeBytes
	}
	if c.MaxSessionBytes == 0 {
		c.MaxSessionBytes = defaults.MaxSessionBytes
	}
	if c.MaxActive == 0 {
		c.MaxActive = defaults.MaxActive
	}
	if c.TTLMinutes == 0 {
		c.TTLMinutes = defaults.TTLMinutes
	}
	return c
}

func (c UsageImportSessionConfig) Validate() error {
	c = c.WithDefaults()
	if c.ChunkSizeBytes <= 0 || c.ChunkSizeBytes > c.MaxSessionBytes {
		return fmt.Errorf("usage-import-session.chunk-size-bytes: must be greater than 0 and less than or equal to max-session-bytes")
	}
	if c.MaxSessionBytes <= 0 || c.MaxSessionBytes > DefaultUsageImportSessionMaxBytes {
		return fmt.Errorf("usage-import-session.max-session-bytes: must be between 1 and %d", DefaultUsageImportSessionMaxBytes)
	}
	if c.MaxActive <= 0 {
		return fmt.Errorf("usage-import-session.max-active: must be greater than 0")
	}
	if c.TTLMinutes <= 0 || c.TTLMinutes > DefaultUsageImportSessionTTLMinutes {
		return fmt.Errorf("usage-import-session.ttl-minutes: must be between 1 and %d", DefaultUsageImportSessionTTLMinutes)
	}
	return nil
}
