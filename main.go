package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Rorical/Goxic/server/model"
	"github.com/Rorical/Goxic/server/node"
)

const (
	DefaultConfigFile = "./config.json"
	AppName           = "Goxic"
	AppVersion        = "1.0.0"
)

func main() {
	// Parse command line flags
	var (
		configPath     = flag.String("config", DefaultConfigFile, "Path to configuration file")
		mode           = flag.String("mode", "", "Node mode: client or server (overrides config)")
		generateConfig = flag.Bool("generate-config", false, "Generate default configuration file and exit")
		version        = flag.Bool("version", false, "Show version information")
		help           = flag.Bool("help", false, "Show help information")
	)
	flag.Parse()

	// Handle special flags
	if *version {
		fmt.Printf("%s v%s\n", AppName, AppVersion)
		fmt.Println("Distributed anti-censorship proxy network built on libp2p")
		os.Exit(0)
	}

	if *help {
		printHelp()
		os.Exit(0)
	}

	if *generateConfig {
		if err := generateDefaultConfig(*configPath); err != nil {
			log.Fatalf("Failed to generate config: %v", err)
		}
		fmt.Printf("Generated default configuration at: %s\n", *configPath)
		fmt.Println("Please edit the configuration file and set required values:")
		fmt.Println("  - network.secret: Shared secret for authentication")
		fmt.Println("  - network.boostrapNodes: Bootstrap node addresses")
		os.Exit(0)
	}

	// Load configuration
	config, err := model.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration from %s: %v", *configPath, err)
	}

	// Override mode from command line if provided
	if *mode != "" {
		if *mode != "client" && *mode != "server" {
			log.Fatalf("Invalid mode '%s', must be 'client' or 'server'", *mode)
		}
		config.Mode = *mode
		log.Printf("Mode overridden from command line: %s", *mode)
	}

	// Initialize logging
	if err := initializeLogging(config); err != nil {
		log.Fatalf("Failed to initialize logging: %v", err)
	}

	log.Printf("Starting %s v%s in %s mode", AppName, AppVersion, config.Mode)
	log.Printf("Configuration loaded from: %s", *configPath)

	// Create application context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start the node
	nodeType := node.NodeTypeClient
	if config.IsServerMode() {
		nodeType = node.NodeTypeServer
	}

	log.Printf("Initializing %s node...", nodeType)
	goxicNode, err := node.SetupNode(ctx, config, nodeType)
	if err != nil {
		log.Fatalf("Failed to setup node: %v", err)
	}

	log.Printf("Node started successfully!")
	log.Printf("Peer ID: %s", goxicNode.Host.ID())
	log.Printf("Listening on: %v", goxicNode.Host.Addrs())

	// Print mode-specific information
	if config.IsClientMode() {
		if config.SOCKS5.Enabled {
			log.Printf("SOCKS5 proxy listening on %s:%d",
				config.SOCKS5.BindAddress, config.SOCKS5.Port)
			log.Printf("Configure your applications to use SOCKS5 proxy: %s:%d",
				config.SOCKS5.BindAddress, config.SOCKS5.Port)
		}
	} else {
		log.Printf("Server node ready to handle proxy traffic")
	}

	// Wait for shutdown signal
	<-sigChan
	log.Println("Received shutdown signal, gracefully stopping...")

	// Graceful shutdown
	if err := node.ShutdownNode(goxicNode); err != nil {
		log.Printf("Error during shutdown: %v", err)
	}

	log.Println("Shutdown complete")
}

// printHelp displays usage information
func printHelp() {
	fmt.Printf("%s v%s - Distributed anti-censorship proxy network\n\n", AppName, AppVersion)
	fmt.Println("Usage:")
	fmt.Printf("  %s [options]\n\n", os.Args[0])
	fmt.Println("Options:")
	flag.PrintDefaults()
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Printf("  # Generate default configuration\n")
	fmt.Printf("  %s -generate-config\n\n", os.Args[0])
	fmt.Printf("  # Run as client node\n")
	fmt.Printf("  %s -mode=client\n\n", os.Args[0])
	fmt.Printf("  # Run as server node with custom config\n")
	fmt.Printf("  %s -config=/path/to/config.json -mode=server\n\n", os.Args[0])
	fmt.Println("Configuration:")
	fmt.Println("  The configuration file is required and must contain:")
	fmt.Println("  - network.secret: Shared authentication secret")
	fmt.Println("  - network.boostrapNodes: List of bootstrap node addresses")
	fmt.Println("  - network.name: Network service name (default: goxic-proxy)")
	fmt.Println()
	fmt.Println("Node Modes:")
	fmt.Println("  client: Runs SOCKS5 proxy locally, routes traffic through servers")
	fmt.Println("  server: Handles traffic egress, can relay for other nodes")
}

// generateDefaultConfig creates a default configuration file
func generateDefaultConfig(configPath string) error {
	// Create directory if it doesn't exist
	if dir := filepath.Dir(configPath); dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create config directory: %w", err)
		}
	}

	// Generate default config
	config := model.NewDefaultConfig()

	// Add example bootstrap nodes (user should replace these)
	config.Network.BoostrapNodes = []string{
		"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWReplace WithActualBootstrapNode1",
		"/ip4/127.0.0.1/tcp/4002/p2p/12D3KooWReplace WithActualBootstrapNode2",
	}

	// Set placeholder secret
	config.Network.Secret = "CHANGE-THIS-TO-YOUR-SHARED-SECRET"

	// Save configuration
	return config.SaveConfig(configPath)
}

// initializeLogging sets up logging based on configuration
func initializeLogging(config *model.Config) error {
	// For now, use standard Go logging
	// TODO: Implement structured logging with levels and file output

	// Set log flags
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Set output file if specified
	if config.Logging.OutputFile != "" {
		// Create log directory if needed
		if dir := filepath.Dir(config.Logging.OutputFile); dir != "." {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("failed to create log directory: %w", err)
			}
		}

		logFile, err := os.OpenFile(config.Logging.OutputFile,
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("failed to open log file: %w", err)
		}

		log.SetOutput(logFile)
		log.Printf("Logging to file: %s", config.Logging.OutputFile)
	}

	return nil
}
