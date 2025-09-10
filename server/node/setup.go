package node

import (
	"context"
	"fmt"
	"log"

	"github.com/Rorical/Goxic/server/handlers"
	"github.com/Rorical/Goxic/server/model"
)

// NodeType represents the type of node (client or server)
type NodeType string

const (
	NodeTypeClient NodeType = "client"
	NodeTypeServer NodeType = "server"
)

// SetupNode initializes a node with appropriate handlers based on its type
func SetupNode(ctx context.Context, config *model.Config, nodeType NodeType) (*model.Node, error) {
	// Create the base node
	node, err := Run(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create node: %w", err)
	}

	// Create handler registry
	registry := handlers.NewRegistry()
	node.Registry = registry

	if config.IsBootstrapMode() {
		log.Printf("Bootstrap mode: only DHT functionality enabled")
	}

	// Setup handlers based on node type (skip for bootstrap mode)
	if config.IsBootstrapMode() {
		log.Printf("Bootstrap mode: skipping protocol handlers, node will only participate in DHT")
		// Start the registry without custom handlers (just basic DHT functionality)
		registry.SetupStreamHandlers(node)
		return node, nil
	}

	// Setup handlers for client/server modes
	switch nodeType {
	case NodeTypeClient:
		if err := setupClientHandlers(ctx, node, registry); err != nil {
			return nil, fmt.Errorf("failed to setup client handlers: %w", err)
		}
	case NodeTypeServer:
		if err := setupServerHandlers(ctx, node, registry); err != nil {
			return nil, fmt.Errorf("failed to setup server handlers: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown node type: %s", nodeType)
	}

	// Setup stream handlers with libp2p
	registry.SetupStreamHandlers(node)

	// Start node-level handlers
	if err := registry.StartNodeHandlers(ctx, node); err != nil {
		return nil, fmt.Errorf("failed to start node handlers: %w", err)
	}

	return node, nil
}

// setupClientHandlers configures handlers for client nodes
func setupClientHandlers(ctx context.Context, node *model.Node, registry *handlers.Registry) error {
	// Client nodes run SOCKS5 proxy locally if enabled
	if node.Config.SOCKS5.Enabled {
		socks5Handler := handlers.NewSOCKS5HandlerWithCapabilities(&node.Config.SOCKS5)
		registry.RegisterNodeHandler(socks5Handler)
	}

	// Client nodes also handle relay protocol for routing through network
	if node.Config.Relay.Enabled {
		relayHandler := handlers.NewRelayHandler()
		registry.RegisterStreamHandler(relayHandler)
	}

	return nil
}

// setupServerHandlers configures handlers for server nodes
func setupServerHandlers(ctx context.Context, node *model.Node, registry *handlers.Registry) error {
	// Server nodes handle relay protocol for traffic routing and exit if enabled
	if node.Config.Relay.Enabled {
		relayHandler := handlers.NewRelayHandler()
		if err := registry.RegisterStreamHandler(relayHandler); err != nil {
			log.Printf("WARNING: Failed to register relay handler: %v", err)
		} else {
			log.Printf("Relay handler registered for protocol: %s", relayHandler.Protocol())
		}
	} else {
		log.Printf("Relay functionality disabled in config")
	}

	// Server nodes need server discovery to actively find and connect to other servers
	if node.Config.Discovery.UpdateIntervalSec > 0 {
		serverDiscovery := handlers.NewServerDiscovery(node)
		registry.RegisterNodeHandler(serverDiscovery)
		log.Printf("Server-to-server discovery enabled with interval: %ds", node.Config.Discovery.UpdateIntervalSec)
	} else {
		log.Printf("Server discovery disabled (updateIntervalSec = 0)")
	}

	return nil
}

// ShutdownNode gracefully shuts down a node and its handlers
func ShutdownNode(node *model.Node) error {
	if node.Registry != nil {
		if err := node.Registry.StopNodeHandlers(); err != nil {
			return fmt.Errorf("failed to stop node handlers: %w", err)
		}
	}

	if node.Host != nil {
		if err := node.Host.Close(); err != nil {
			return fmt.Errorf("failed to close host: %w", err)
		}
	}

	if node.Store != nil {
		if err := node.Store.Close(); err != nil {
			return fmt.Errorf("failed to close datastore: %w", err)
		}
	}

	return nil
}
