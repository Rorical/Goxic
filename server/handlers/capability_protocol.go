package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"

	"github.com/Rorical/Goxic/server/model"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const (
	CapabilityProtocol = "/goxic/capability-exchange/1.0.0"
)

// CapabilityExchangeMessage represents a capability exchange message
type CapabilityExchangeMessage struct {
	Type         string            `json:"type"`         // "request" or "response"
	Capabilities *NodeCapabilities `json:"capabilities"` // null for requests
}

// CapabilityProtocolHandler handles capability exchange protocol
type CapabilityProtocolHandler struct {
	capabilityManager *CapabilityManager
}

// NewCapabilityProtocolHandler creates a new capability protocol handler
func NewCapabilityProtocolHandler(capabilityManager *CapabilityManager) *CapabilityProtocolHandler {
	return &CapabilityProtocolHandler{
		capabilityManager: capabilityManager,
	}
}

// Protocol returns the protocol ID
func (h *CapabilityProtocolHandler) Protocol() string {
	return CapabilityProtocol
}

// Handle processes capability exchange streams
func (h *CapabilityProtocolHandler) Handle(stream network.Stream, node *model.Node) error {
	defer stream.Close()

	remotePeer := stream.Conn().RemotePeer()
	log.Printf("Capability exchange from peer: %s", remotePeer)

	// Read incoming message
	data, err := io.ReadAll(stream)
	if err != nil {
		return fmt.Errorf("failed to read capability message: %w", err)
	}

	var message CapabilityExchangeMessage
	if err := json.Unmarshal(data, &message); err != nil {
		return fmt.Errorf("failed to unmarshal capability message: %w", err)
	}

	switch message.Type {
	case "request":
		// Peer is requesting our capabilities, send response
		return h.handleCapabilityRequest(stream, remotePeer)

	case "response":
		// Peer is sharing their capabilities with us
		return h.handleCapabilityResponse(stream, remotePeer, message.Capabilities)

	default:
		return fmt.Errorf("unknown capability message type: %s", message.Type)
	}
}

// handleCapabilityRequest responds with our capabilities
func (h *CapabilityProtocolHandler) handleCapabilityRequest(stream network.Stream, remotePeer peer.ID) error {
	ourCapabilities := h.capabilityManager.GetOurCapabilities()

	response := CapabilityExchangeMessage{
		Type:         "response",
		Capabilities: &ourCapabilities,
	}

	responseData, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("failed to marshal capability response: %w", err)
	}

	_, err = stream.Write(responseData)
	return err
}

// handleCapabilityResponse processes received capabilities from a peer
func (h *CapabilityProtocolHandler) handleCapabilityResponse(stream network.Stream, remotePeer peer.ID, capabilities *NodeCapabilities) error {
	if capabilities == nil {
		return fmt.Errorf("received nil capabilities")
	}

	// Store the peer's capabilities
	if err := h.capabilityManager.UpdatePeerCapabilities(remotePeer, *capabilities); err != nil {
		return fmt.Errorf("failed to update peer capabilities: %w", err)
	}

	log.Printf("Received capabilities from %s: %v", remotePeer, capabilities.Capabilities)
	return nil
}

// RequestCapabilities requests capabilities from a specific peer
func (h *CapabilityProtocolHandler) RequestCapabilities(ctx context.Context, node *model.Node, peerID peer.ID) error {
	// Create stream to peer
	stream, err := node.Host.NewStream(ctx, peerID, protocol.ID(CapabilityProtocol))
	if err != nil {
		return fmt.Errorf("failed to create capability exchange stream: %w", err)
	}
	defer stream.Close()

	// Send capability request
	request := CapabilityExchangeMessage{
		Type:         "request",
		Capabilities: nil,
	}

	requestData, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("failed to marshal capability request: %w", err)
	}

	if _, err := stream.Write(requestData); err != nil {
		return fmt.Errorf("failed to send capability request: %w", err)
	}

	// Read response
	responseData, err := io.ReadAll(stream)
	if err != nil {
		return fmt.Errorf("failed to read capability response: %w", err)
	}

	var response CapabilityExchangeMessage
	if err := json.Unmarshal(responseData, &response); err != nil {
		return fmt.Errorf("failed to unmarshal capability response: %w", err)
	}

	if response.Type == "response" && response.Capabilities != nil {
		peerID := stream.Conn().RemotePeer()
		return h.capabilityManager.UpdatePeerCapabilities(peerID, *response.Capabilities)
	}

	return fmt.Errorf("invalid capability response from peer")
}

// ExchangeCapabilitiesWithPeer initiates capability exchange with a connected peer
func (h *CapabilityProtocolHandler) ExchangeCapabilitiesWithPeer(ctx context.Context, node *model.Node, peerID peer.ID) {
	go func() {
		if err := h.RequestCapabilities(ctx, node, peerID); err != nil {
			log.Printf("Failed to exchange capabilities with peer %s: %v", peerID, err)
		}
	}()
}
