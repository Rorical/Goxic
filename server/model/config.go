package model

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// NetworkConfig contains network-level settings
type NetworkConfig struct {
	Port               int      `json:"port"`               // Main libp2p port (TCP/QUIC)
	BoostrapNodes      []string `json:"boostrapNodes"`      // DHT bootstrap peers
	Name               string   `json:"name"`               // Network/service name for DHT
	Secret             string   `json:"secret"`             // Authentication secret
	MaxConnections     int      `json:"maxConnections"`     // Maximum peer connections
	MinConnections     int      `json:"minConnections"`     // Minimum peer connections
	ConnectionTimeout  int      `json:"connectionTimeout"`  // Connection timeout in seconds
	DialTimeout        int      `json:"dialTimeout"`        // Dial timeout in seconds
	EnableIPv6         bool     `json:"enableIPv6"`         // Enable IPv6 support
	EnableAutoRelay    bool     `json:"enableAutoRelay"`    // Enable auto relay
	EnableHolePunching bool     `json:"enableHolePunching"` // Enable hole punching
}

// AuthConfig contains authentication settings
type AuthConfig struct {
	Enabled            bool `json:"enabled"`            // Enable authentication
	TimeoutSeconds     int  `json:"timeoutSeconds"`     // Auth timeout in seconds
	ValidityHours      int  `json:"validityHours"`      // Auth validity in hours
	CleanupIntervalMin int  `json:"cleanupIntervalMin"` // Cleanup interval in minutes
}

// SOCKS5Config contains SOCKS5 proxy settings
type SOCKS5Config struct {
	Enabled           bool     `json:"enabled"`           // Enable SOCKS5 server
	Port              int      `json:"port"`              // SOCKS5 listen port
	BindAddress       string   `json:"bindAddress"`       // Bind address (default: 127.0.0.1)
	AllowedNetworks   []string `json:"allowedNetworks"`   // Allowed client networks (CIDR)
	MaxConnections    int      `json:"maxConnections"`    // Max SOCKS5 connections
	ConnectionTimeout int      `json:"connectionTimeout"` // Connection timeout in seconds
}

// RelayConfig contains traffic relay settings
type RelayConfig struct {
	Enabled          bool `json:"enabled"`          // Enable relay functionality
	MaxHops          int  `json:"maxHops"`          // Maximum relay hops
	BandwidthLimitMB int  `json:"bandwidthLimitMB"` // Bandwidth limit in MB/s
	BufferSize       int  `json:"bufferSize"`       // Relay buffer size
}

// DiscoveryConfig contains peer discovery settings
type DiscoveryConfig struct {
	UpdateIntervalSec     int  `json:"updateIntervalSec"`     // Server discovery interval
	AdvertiseIntervalSec  int  `json:"advertiseIntervalSec"`  // Advertisement interval
	QueryTimeoutSec       int  `json:"queryTimeoutSec"`       // DHT query timeout
	CacheValidityMin      int  `json:"cacheValidityMin"`      // Peer cache validity
	MaxServers            int  `json:"maxServers"`            // Max cached servers
	EnableRandomSelection bool `json:"enableRandomSelection"` // Random server selection
}

// LoggingConfig contains logging settings
type LoggingConfig struct {
	Level      string `json:"level"`      // Log level (debug, info, warn, error)
	Format     string `json:"format"`     // Log format (text, json)
	OutputFile string `json:"outputFile"` // Log output file (empty for stdout)
	MaxSizeMB  int    `json:"maxSizeMB"`  // Max log file size in MB
	MaxBackups int    `json:"maxBackups"` // Max log backup files
}

// Config represents the complete application configuration
type Config struct {
	// Basic settings
	Mode      string `json:"mode"`      // Node mode: "client" or "server"
	DataDir   string `json:"dataDir"`   // Data directory for persistent storage
	ConfigDir string `json:"configDir"` // Configuration directory

	// Component configurations
	Network   NetworkConfig   `json:"network"`   // Network settings
	Auth      AuthConfig      `json:"auth"`      // Authentication settings
	SOCKS5    SOCKS5Config    `json:"socks5"`    // SOCKS5 proxy settings
	Relay     RelayConfig     `json:"relay"`     // Relay settings
	Discovery DiscoveryConfig `json:"discovery"` // Discovery settings
	Logging   LoggingConfig   `json:"logging"`   // Logging settings
}

// LoadConfig loads configuration from file with validation and defaults
func LoadConfig(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file %s: %w", path, err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err = json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config JSON: %w", err)
	}

	// Apply defaults and validate
	if err = config.ApplyDefaults(); err != nil {
		return nil, fmt.Errorf("failed to apply defaults: %w", err)
	}

	if err = config.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return &config, nil
}

// NewDefaultConfig creates a configuration with sensible defaults
func NewDefaultConfig() *Config {
	config := &Config{
		Mode:      "client",
		DataDir:   "./data",
		ConfigDir: "./config",

		Network: NetworkConfig{
			Port:               4001,
			BoostrapNodes:      []string{}, // Empty by default - must be provided
			Name:               "goxic-proxy",
			Secret:             "", // Must be provided
			MaxConnections:     400,
			MinConnections:     100,
			ConnectionTimeout:  30,
			DialTimeout:        10,
			EnableIPv6:         true,
			EnableAutoRelay:    true,
			EnableHolePunching: true,
		},

		Auth: AuthConfig{
			Enabled:            true,
			TimeoutSeconds:     30,
			ValidityHours:      24,
			CleanupIntervalMin: 60,
		},

		SOCKS5: SOCKS5Config{
			Enabled:           true,
			Port:              1080,
			BindAddress:       "127.0.0.1",
			AllowedNetworks:   []string{"127.0.0.0/8", "::1/128"},
			MaxConnections:    100,
			ConnectionTimeout: 30,
		},

		Relay: RelayConfig{
			Enabled:          true,
			MaxHops:          3,
			BandwidthLimitMB: 0,         // 0 = unlimited
			BufferSize:       64 * 1024, // 64KB
		},

		Discovery: DiscoveryConfig{
			UpdateIntervalSec:     30,
			AdvertiseIntervalSec:  60,
			QueryTimeoutSec:       10,
			CacheValidityMin:      10,
			MaxServers:            50,
			EnableRandomSelection: false,
		},

		Logging: LoggingConfig{
			Level:      "info",
			Format:     "text",
			OutputFile: "", // stdout
			MaxSizeMB:  100,
			MaxBackups: 5,
		},
	}

	return config
}

// ApplyDefaults fills in missing configuration values with defaults
func (c *Config) ApplyDefaults() error {
	defaults := NewDefaultConfig()

	// Apply network defaults
	if c.Network.Port == 0 {
		c.Network.Port = defaults.Network.Port
	}
	if c.Network.Name == "" {
		c.Network.Name = defaults.Network.Name
	}
	if c.Network.MaxConnections == 0 {
		c.Network.MaxConnections = defaults.Network.MaxConnections
	}
	if c.Network.MinConnections == 0 {
		c.Network.MinConnections = defaults.Network.MinConnections
	}
	if c.Network.ConnectionTimeout == 0 {
		c.Network.ConnectionTimeout = defaults.Network.ConnectionTimeout
	}
	if c.Network.DialTimeout == 0 {
		c.Network.DialTimeout = defaults.Network.DialTimeout
	}

	// Apply auth defaults
	if c.Auth.TimeoutSeconds == 0 {
		c.Auth.TimeoutSeconds = defaults.Auth.TimeoutSeconds
	}
	if c.Auth.ValidityHours == 0 {
		c.Auth.ValidityHours = defaults.Auth.ValidityHours
	}
	if c.Auth.CleanupIntervalMin == 0 {
		c.Auth.CleanupIntervalMin = defaults.Auth.CleanupIntervalMin
	}

	// Apply SOCKS5 defaults
	if c.SOCKS5.Port == 0 {
		c.SOCKS5.Port = defaults.SOCKS5.Port
	}
	if c.SOCKS5.BindAddress == "" {
		c.SOCKS5.BindAddress = defaults.SOCKS5.BindAddress
	}
	if len(c.SOCKS5.AllowedNetworks) == 0 {
		c.SOCKS5.AllowedNetworks = defaults.SOCKS5.AllowedNetworks
	}
	if c.SOCKS5.MaxConnections == 0 {
		c.SOCKS5.MaxConnections = defaults.SOCKS5.MaxConnections
	}
	if c.SOCKS5.ConnectionTimeout == 0 {
		c.SOCKS5.ConnectionTimeout = defaults.SOCKS5.ConnectionTimeout
	}

	// Apply relay defaults
	if c.Relay.MaxHops == 0 {
		c.Relay.MaxHops = defaults.Relay.MaxHops
	}
	if c.Relay.BufferSize == 0 {
		c.Relay.BufferSize = defaults.Relay.BufferSize
	}

	// Apply discovery defaults
	if c.Discovery.UpdateIntervalSec == 0 {
		c.Discovery.UpdateIntervalSec = defaults.Discovery.UpdateIntervalSec
	}
	if c.Discovery.AdvertiseIntervalSec == 0 {
		c.Discovery.AdvertiseIntervalSec = defaults.Discovery.AdvertiseIntervalSec
	}
	if c.Discovery.QueryTimeoutSec == 0 {
		c.Discovery.QueryTimeoutSec = defaults.Discovery.QueryTimeoutSec
	}
	if c.Discovery.CacheValidityMin == 0 {
		c.Discovery.CacheValidityMin = defaults.Discovery.CacheValidityMin
	}
	if c.Discovery.MaxServers == 0 {
		c.Discovery.MaxServers = defaults.Discovery.MaxServers
	}

	// Apply logging defaults
	if c.Logging.Level == "" {
		c.Logging.Level = defaults.Logging.Level
	}
	if c.Logging.Format == "" {
		c.Logging.Format = defaults.Logging.Format
	}
	if c.Logging.MaxSizeMB == 0 {
		c.Logging.MaxSizeMB = defaults.Logging.MaxSizeMB
	}
	if c.Logging.MaxBackups == 0 {
		c.Logging.MaxBackups = defaults.Logging.MaxBackups
	}

	// Set mode-specific defaults
	if c.Mode == "" {
		c.Mode = defaults.Mode
	}
	if c.DataDir == "" {
		c.DataDir = defaults.DataDir
	}
	if c.ConfigDir == "" {
		c.ConfigDir = defaults.ConfigDir
	}

	// Adjust settings based on mode
	if c.Mode == "server" {
		c.SOCKS5.Enabled = false // Servers don't run SOCKS5
	} else if c.Mode == "client" {
		c.SOCKS5.Enabled = true // Clients run SOCKS5
	}

	return nil
}

// Validate checks configuration for consistency and correctness
func (c *Config) Validate() error {
	// Validate mode
	if c.Mode != "client" && c.Mode != "server" {
		return fmt.Errorf("invalid mode '%s', must be 'client' or 'server'", c.Mode)
	}

	// Validate required fields
	if c.Network.Secret == "" {
		return fmt.Errorf("network.secret is required for authentication")
	}
	if len(c.Network.BoostrapNodes) == 0 {
		return fmt.Errorf("network.boostrapNodes is required for DHT bootstrap")
	}
	if c.Network.Name == "" {
		return fmt.Errorf("network.name is required for service discovery")
	}

	// Validate port ranges
	if c.Network.Port < 1024 || c.Network.Port > 65535 {
		return fmt.Errorf("network.port must be between 1024 and 65535")
	}
	if c.SOCKS5.Enabled && (c.SOCKS5.Port < 1024 || c.SOCKS5.Port > 65535) {
		return fmt.Errorf("socks5.port must be between 1024 and 65535")
	}

	// Validate network settings
	if c.Network.MinConnections > c.Network.MaxConnections {
		return fmt.Errorf("network.minConnections (%d) cannot be greater than maxConnections (%d)",
			c.Network.MinConnections, c.Network.MaxConnections)
	}

	// Validate directories
	if !filepath.IsAbs(c.DataDir) {
		// Convert relative path to absolute
		abs, err := filepath.Abs(c.DataDir)
		if err != nil {
			return fmt.Errorf("invalid dataDir '%s': %w", c.DataDir, err)
		}
		c.DataDir = abs
	}

	// Validate SOCKS5 bind address
	if c.SOCKS5.Enabled {
		if net.ParseIP(c.SOCKS5.BindAddress) == nil {
			return fmt.Errorf("invalid socks5.bindAddress '%s'", c.SOCKS5.BindAddress)
		}

		// Validate allowed networks
		for _, network := range c.SOCKS5.AllowedNetworks {
			if _, _, err := net.ParseCIDR(network); err != nil {
				return fmt.Errorf("invalid allowed network '%s': %w", network, err)
			}
		}
	}

	// Validate log level
	validLogLevels := []string{"debug", "info", "warn", "error"}
	levelValid := false
	for _, level := range validLogLevels {
		if strings.ToLower(c.Logging.Level) == level {
			c.Logging.Level = level // Normalize case
			levelValid = true
			break
		}
	}
	if !levelValid {
		return fmt.Errorf("invalid logging.level '%s', must be one of: %s",
			c.Logging.Level, strings.Join(validLogLevels, ", "))
	}

	// Validate log format
	if c.Logging.Format != "text" && c.Logging.Format != "json" {
		return fmt.Errorf("invalid logging.format '%s', must be 'text' or 'json'", c.Logging.Format)
	}

	return nil
}

// SaveConfig saves the configuration to a file
func (c *Config) SaveConfig(path string) error {
	// Create directory if it doesn't exist
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create config directory: %w", err)
		}
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// GetDataPath returns the full path for a data file
func (c *Config) GetDataPath(filename string) string {
	return filepath.Join(c.DataDir, filename)
}

// IsClientMode returns true if running in client mode
func (c *Config) IsClientMode() bool {
	return c.Mode == "client"
}

// IsServerMode returns true if running in server mode
func (c *Config) IsServerMode() bool {
	return c.Mode == "server"
}
