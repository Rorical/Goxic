package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/Rorical/Goxic/server/model"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const (
	// Metadata key for node capabilities
	CapabilityMetadataKey = "goxic-capabilities"
)

// NodeCapability represents what a node can do
type NodeCapability string

const (
	CapabilitySOCKS5Proxy NodeCapability = "socks5-proxy" // Can accept SOCKS5 connections
	CapabilityExitNode    NodeCapability = "exit-node"    // Can route traffic to internet
	CapabilityRelay       NodeCapability = "relay"        // Can relay traffic between peers
)

// NodeCapabilities represents the full set of capabilities for a node
type NodeCapabilities struct {
	Capabilities []NodeCapability `json:"capabilities"`
	Version      string           `json:"version"`
	NodeType     string           `json:"node_type"` // "client" or "server"
}

// CapabilityManager manages node capability metadata
type CapabilityManager struct {
	node         *model.Node
	capabilities NodeCapabilities
}

// NewCapabilityManager creates a new capability manager
func NewCapabilityManager(node *model.Node, nodeType string) *CapabilityManager {
	var caps []NodeCapability

	switch nodeType {
	case "client":
		caps = []NodeCapability{
			CapabilitySOCKS5Proxy,
			CapabilityRelay,
		}
	case "server":
		caps = []NodeCapability{
			CapabilityExitNode,
			CapabilityRelay,
		}
	default:
		// Default to relay only
		caps = []NodeCapability{CapabilityRelay}
	}

	capabilities := NodeCapabilities{
		Capabilities: caps,
		Version:      "1.0.0",
		NodeType:     nodeType,
	}

	return &CapabilityManager{
		node:         node,
		capabilities: capabilities,
	}
}

// AdvertiseCapabilities stores our capabilities in the local peerstore
func (cm *CapabilityManager) AdvertiseCapabilities() error {
	capabilityData, err := json.Marshal(cm.capabilities)
	if err != nil {
		return fmt.Errorf("failed to marshal capabilities: %w", err)
	}

	// Store in our own peerstore
	cm.node.Host.Peerstore().Put(cm.node.Host.ID(), CapabilityMetadataKey, capabilityData)

	log.Printf("Advertised capabilities: %v", cm.capabilities.Capabilities)
	return nil
}

// GetPeerCapabilities retrieves capabilities for a specific peer
func (cm *CapabilityManager) GetPeerCapabilities(peerID peer.ID) (*NodeCapabilities, error) {
	// Try to get from peerstore first
	if data, err := cm.node.Host.Peerstore().Get(peerID, CapabilityMetadataKey); err == nil {
		if dataBytes, ok := data.([]byte); ok {
			var capabilities NodeCapabilities
			if err := json.Unmarshal(dataBytes, &capabilities); err == nil {
				return &capabilities, nil
			}
		}
	}

	// If not in peerstore, try to query the peer directly
	return cm.queryPeerCapabilities(peerID)
}

// queryPeerCapabilities queries a peer for their capabilities
func (cm *CapabilityManager) queryPeerCapabilities(peerID peer.ID) (*NodeCapabilities, error) {
	// Check if peer is connected
	if cm.node.Host.Network().Connectedness(peerID) != 1 {
		return nil, fmt.Errorf("peer %s is not connected", peerID)
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	// Try to create stream for capability exchange
	stream, err := cm.node.Host.NewStream(ctx, peerID, protocol.ID(CapabilityProtocol))
	if err != nil {
		log.Printf("Failed to create capability exchange stream with %s: %v", peerID, err)
		// If capability protocol is not supported, try to infer from other means
		return cm.inferPeerCapabilities(peerID)
	}
	defer stream.Close()

	// Set stream timeout
	stream.SetDeadline(time.Now().Add(time.Second * 10))

	// Send capability request
	request := CapabilityExchangeMessage{
		Type:         "request",
		Capabilities: nil,
	}

	requestData, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal capability request: %w", err)
	}

	if _, err := stream.Write(requestData); err != nil {
		return nil, fmt.Errorf("failed to send capability request: %w", err)
	}

	// Read response
	responseData, err := io.ReadAll(stream)
	if err != nil {
		return nil, fmt.Errorf("failed to read capability response: %w", err)
	}

	var response CapabilityExchangeMessage
	if err := json.Unmarshal(responseData, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal capability response: %w", err)
	}

	if response.Type != "response" || response.Capabilities == nil {
		return nil, fmt.Errorf("invalid capability response from peer %s", peerID)
	}

	// Cache the capabilities
	if err := cm.UpdatePeerCapabilities(peerID, *response.Capabilities); err != nil {
		log.Printf("Failed to cache capabilities for peer %s: %v", peerID, err)
	}

	return response.Capabilities, nil
}

// inferPeerCapabilities attempts to infer peer capabilities from available information
func (cm *CapabilityManager) inferPeerCapabilities(peerID peer.ID) (*NodeCapabilities, error) {
	// Try to probe for specific protocols to infer capabilities
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	capabilities := make([]NodeCapability, 0)
	nodeType := "unknown"

	// Test for relay capability
	hasRelay := cm.testProtocolSupport(ctx, peerID, "/Goxic/relay/.*")
	if hasRelay {
		capabilities = append(capabilities, CapabilityRelay)
	}

	// Try to determine if this is a client or server based on multiple factors
	// Since we can't easily test for SOCKS5 (it's a node handler, not stream protocol),
	// we'll use heuristics:

	// 1. Check if peer has capability exchange protocol - indicates full node
	hasCapabilityExchange := cm.testProtocolSupport(ctx, peerID, "/goxic/capability-exchange/.*")

	// 2. Be optimistic about relay-capable peers since capability exchange might be failing
	if hasCapabilityExchange && hasRelay {
		// Peer supports capability exchange and relay - likely a full server node
		nodeType = "server"
		capabilities = append(capabilities, CapabilityExitNode)
		log.Printf("Peer %s has relay + capability exchange - assuming server with exit capability", peerID)
	} else if hasRelay {
		// Has relay but no capability exchange - could still be a server
		// Be optimistic since exact capability determination is failing
		nodeType = "server"
		capabilities = append(capabilities, CapabilityExitNode)
		log.Printf("Peer %s has relay capability - optimistically assuming exit capability", peerID)
	}

	// If we can't determine capabilities, return minimal safe set
	if len(capabilities) == 0 {
		capabilities = []NodeCapability{CapabilityRelay}
		nodeType = "unknown"
	}

	inferredCapabilities := &NodeCapabilities{
		Capabilities: capabilities,
		Version:      "1.0.0",
		NodeType:     nodeType,
	}

	// Cache inferred capabilities but mark them as uncertain
	if err := cm.UpdatePeerCapabilities(peerID, *inferredCapabilities); err != nil {
		log.Printf("Failed to cache inferred capabilities for peer %s: %v", peerID, err)
	}

	log.Printf("Inferred capabilities for peer %s: %v (node_type: %s)", peerID, capabilities, nodeType)
	return inferredCapabilities, nil
}

// testProtocolSupport tests if a peer supports a specific protocol pattern
func (cm *CapabilityManager) testProtocolSupport(ctx context.Context, peerID peer.ID, protocolPattern string) bool {
	// Get supported protocols from peer
	protocols, err := cm.node.Host.Peerstore().GetProtocols(peerID)
	if err != nil {
		log.Printf("Failed to get protocols for peer %s: %v", peerID, err)
		return false
	}

	// Check if any supported protocol matches our pattern
	for _, proto := range protocols {
		// Simple pattern matching - for regex patterns, compile and test
		if protocolPattern == "/Goxic/relay/.*" {
			if strings.HasPrefix(string(proto), "/Goxic/relay/") {
				return true
			}
		} else if protocolPattern == "/goxic/capability-exchange/.*" {
			if strings.HasPrefix(string(proto), "/goxic/capability-exchange/") {
				return true
			}
		} else if string(proto) == protocolPattern {
			return true
		}
	}

	return false
}

// HasCapability checks if a peer has a specific capability
func (cm *CapabilityManager) HasCapability(peerID peer.ID, capability NodeCapability) bool {
	capabilities, err := cm.GetPeerCapabilities(peerID)
	if err != nil {
		return false
	}

	for _, cap := range capabilities.Capabilities {
		if cap == capability {
			return true
		}
	}

	return false
}

// GetCapableServers returns peers that have exit node capability
func (cm *CapabilityManager) GetCapableServers(peers []peer.AddrInfo) []peer.AddrInfo {
	capableServers := make([]peer.AddrInfo, 0)

	for _, peerInfo := range peers {
		if cm.HasCapability(peerInfo.ID, CapabilityExitNode) {
			capableServers = append(capableServers, peerInfo)
		}
	}

	return capableServers
}

// UpdatePeerCapabilities updates stored capabilities for a peer
func (cm *CapabilityManager) UpdatePeerCapabilities(peerID peer.ID, capabilities NodeCapabilities) error {
	capabilityData, err := json.Marshal(capabilities)
	if err != nil {
		return fmt.Errorf("failed to marshal peer capabilities: %w", err)
	}

	cm.node.Host.Peerstore().Put(peerID, CapabilityMetadataKey, capabilityData)
	log.Printf("Updated capabilities for peer %s: %v", peerID, capabilities.Capabilities)

	return nil
}

// GetOurCapabilities returns our own capabilities
func (cm *CapabilityManager) GetOurCapabilities() NodeCapabilities {
	return cm.capabilities
}

// StartCapabilityExchange starts exchanging capabilities with connected peers
func (cm *CapabilityManager) StartCapabilityExchange(ctx context.Context) {
	log.Printf("Capability exchange started for node type: %s", cm.capabilities.NodeType)

	// Start a goroutine to periodically exchange capabilities with connected peers
	go func() {
		ticker := time.NewTicker(time.Minute * 2) // Exchange capabilities every 2 minutes
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				log.Printf("Capability exchange stopped")
				return
			case <-ticker.C:
				cm.exchangeWithConnectedPeers(ctx)
			}
		}
	}()
}

// ConnectedServers returns currently connected peers that can act as exit nodes
func (cm *CapabilityManager) ConnectedServers() []peer.AddrInfo {
	connectedPeers := cm.node.Host.Network().Peers()
	serverPeers := make([]peer.AddrInfo, 0)

	for _, peerID := range connectedPeers {
		if cm.HasCapability(peerID, CapabilityExitNode) {
			// Get peer info from peerstore
			peerInfo := cm.node.Host.Peerstore().PeerInfo(peerID)
			serverPeers = append(serverPeers, peerInfo)
		}
	}

	return serverPeers
}

// exchangeWithConnectedPeers exchanges capabilities with all connected peers
func (cm *CapabilityManager) exchangeWithConnectedPeers(ctx context.Context) {
	connectedPeers := cm.node.Host.Network().Peers()

	for _, peerID := range connectedPeers {
		// Skip if we already have recent capability information
		if capabilities, err := cm.GetPeerCapabilities(peerID); err == nil && capabilities != nil {
			continue // We already know this peer's capabilities
		}

		// Try to exchange capabilities with this peer
		go func(pid peer.ID) {
			if caps, err := cm.queryPeerCapabilities(pid); err == nil && caps != nil {
				log.Printf("Exchanged capabilities with peer %s: %v", pid, caps.Capabilities)
			} else {
				log.Printf("Failed to exchange capabilities with peer %s: %v", pid, err)
			}
		}(peerID)
	}
}
