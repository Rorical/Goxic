package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/Rorical/Goxic/server/model"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const (
	AuthProtocol      = "/goxic/auth/1.0.0"
	AuthChallengeSize = 32
)

// AuthMessage represents authentication protocol messages
type AuthMessage struct {
	Type      string `json:"type"`      // "challenge", "response", "success", "error"
	Challenge []byte `json:"challenge"` // Random challenge bytes
	Response  []byte `json:"response"`  // HMAC response
	Timestamp int64  `json:"timestamp"` // Unix timestamp
	Error     string `json:"error"`     // Error message if any
}

// AuthenticatedPeerManager manages authentication state for peers
type AuthenticatedPeerManager struct {
	mu                 sync.RWMutex
	authenticatedPeers map[peer.ID]time.Time // peer -> authentication time
	secret             string
	node               *model.Node
}

// NewAuthenticatedPeerManager creates a new authentication manager
func NewAuthenticatedPeerManager(node *model.Node, secret string) *AuthenticatedPeerManager {
	return &AuthenticatedPeerManager{
		authenticatedPeers: make(map[peer.ID]time.Time),
		secret:             secret,
		node:               node,
	}
}

// IsAuthenticated checks if a peer is authenticated
func (apm *AuthenticatedPeerManager) IsAuthenticated(peerID peer.ID) bool {
	apm.mu.RLock()
	defer apm.mu.RUnlock()

	authTime, exists := apm.authenticatedPeers[peerID]
	if !exists {
		return false
	}

	// Check if authentication is still valid using config validity hours
	validityDuration := time.Duration(apm.node.Config.Auth.ValidityHours) * time.Hour
	if time.Since(authTime) > validityDuration {
		// Remove expired authentication
		delete(apm.authenticatedPeers, peerID)
		return false
	}

	return true
}

// MarkAuthenticated marks a peer as authenticated
func (apm *AuthenticatedPeerManager) MarkAuthenticated(peerID peer.ID) {
	apm.mu.Lock()
	defer apm.mu.Unlock()

	apm.authenticatedPeers[peerID] = time.Now()
	log.Printf("Peer %s authenticated successfully", peerID)
}

// RemoveAuthentication removes authentication for a peer
func (apm *AuthenticatedPeerManager) RemoveAuthentication(peerID peer.ID) {
	apm.mu.Lock()
	defer apm.mu.Unlock()

	delete(apm.authenticatedPeers, peerID)
	log.Printf("Authentication removed for peer %s", peerID)
}

// GetAuthenticatedPeers returns list of currently authenticated peers
func (apm *AuthenticatedPeerManager) GetAuthenticatedPeers() []peer.ID {
	apm.mu.RLock()
	defer apm.mu.RUnlock()

	peers := make([]peer.ID, 0, len(apm.authenticatedPeers))
	now := time.Now()

	validityDuration := time.Duration(apm.node.Config.Auth.ValidityHours) * time.Hour
	for peerID, authTime := range apm.authenticatedPeers {
		// Only include non-expired peers
		if now.Sub(authTime) <= validityDuration {
			peers = append(peers, peerID)
		} else {
			// Clean up expired entries
			delete(apm.authenticatedPeers, peerID)
		}
	}

	return peers
}

// AuthProtocolHandler handles authentication protocol
type AuthProtocolHandler struct {
	authManager *AuthenticatedPeerManager
}

// NewAuthProtocolHandler creates a new authentication protocol handler
func NewAuthProtocolHandler(authManager *AuthenticatedPeerManager) *AuthProtocolHandler {
	return &AuthProtocolHandler{
		authManager: authManager,
	}
}

// Protocol returns the protocol ID
func (h *AuthProtocolHandler) Protocol() string {
	return AuthProtocol
}

// Handle processes authentication streams
func (h *AuthProtocolHandler) Handle(stream network.Stream, node *model.Node) error {
	defer stream.Close()

	remotePeer := stream.Conn().RemotePeer()
	log.Printf("Authentication request from peer: %s", remotePeer)

	// Set read timeout using config
	authTimeout := time.Duration(node.Config.Auth.TimeoutSeconds) * time.Second
	stream.SetReadDeadline(time.Now().Add(authTimeout))

	// Read incoming message
	data, err := io.ReadAll(stream)
	if err != nil {
		return fmt.Errorf("failed to read auth message: %w", err)
	}

	var message AuthMessage
	if err := json.Unmarshal(data, &message); err != nil {
		return fmt.Errorf("failed to unmarshal auth message: %w", err)
	}

	switch message.Type {
	case "challenge":
		// Peer is starting authentication, send challenge
		return h.handleAuthChallenge(stream, remotePeer)

	case "response":
		// Peer is responding to our challenge
		return h.handleAuthResponse(stream, remotePeer, &message)

	default:
		return fmt.Errorf("unknown auth message type: %s", message.Type)
	}
}

// handleAuthChallenge responds with an authentication challenge
func (h *AuthProtocolHandler) handleAuthChallenge(stream network.Stream, remotePeer peer.ID) error {
	// Generate random challenge
	challenge := make([]byte, AuthChallengeSize)
	if _, err := rand.Read(challenge); err != nil {
		return fmt.Errorf("failed to generate challenge: %w", err)
	}

	challengeMsg := AuthMessage{
		Type:      "challenge",
		Challenge: challenge,
		Timestamp: time.Now().Unix(),
	}

	responseData, err := json.Marshal(challengeMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal challenge: %w", err)
	}

	if _, err := stream.Write(responseData); err != nil {
		return fmt.Errorf("failed to send challenge: %w", err)
	}

	// Wait for response
	authTimeout := time.Duration(h.authManager.node.Config.Auth.TimeoutSeconds) * time.Second
	stream.SetReadDeadline(time.Now().Add(authTimeout))
	responseBytes, err := io.ReadAll(stream)
	if err != nil {
		return fmt.Errorf("failed to read auth response: %w", err)
	}

	var response AuthMessage
	if err := json.Unmarshal(responseBytes, &response); err != nil {
		return fmt.Errorf("failed to unmarshal auth response: %w", err)
	}

	if response.Type != "response" {
		return fmt.Errorf("expected response, got: %s", response.Type)
	}

	// Verify the response
	return h.verifyAuthResponse(stream, remotePeer, challenge, response.Response)
}

// handleAuthResponse processes authentication response from initiator
func (h *AuthProtocolHandler) handleAuthResponse(stream network.Stream, remotePeer peer.ID, message *AuthMessage) error {
	// This handles the case where we're responding to someone else's challenge
	// For now, we expect challenges to be initiated by the connecting peer
	return fmt.Errorf("unexpected auth response from %s", remotePeer)
}

// verifyAuthResponse verifies the HMAC response against the challenge
func (h *AuthProtocolHandler) verifyAuthResponse(stream network.Stream, remotePeer peer.ID, challenge, response []byte) error {
	// Calculate expected HMAC
	hash := hmac.New(sha256.New, []byte(h.authManager.secret))

	// Include challenge and timestamp in HMAC
	hash.Write(challenge)

	// Add peer ID to prevent replay attacks
	hash.Write([]byte(remotePeer.String()))

	expectedHMAC := hash.Sum(nil)

	// Verify HMAC
	if !hmac.Equal(expectedHMAC, response) {
		// Send error response
		errorMsg := AuthMessage{
			Type:  "error",
			Error: "authentication failed",
		}

		if errorData, err := json.Marshal(errorMsg); err == nil {
			stream.Write(errorData)
		}

		log.Printf("Authentication failed for peer %s: invalid secret", remotePeer)
		return fmt.Errorf("authentication failed: invalid secret")
	}

	// Authentication successful
	h.authManager.MarkAuthenticated(remotePeer)

	// Send success response
	successMsg := AuthMessage{
		Type:      "success",
		Timestamp: time.Now().Unix(),
	}

	successData, err := json.Marshal(successMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal success message: %w", err)
	}

	if _, err := stream.Write(successData); err != nil {
		return fmt.Errorf("failed to send success message: %w", err)
	}

	log.Printf("Authentication successful for peer %s", remotePeer)
	return nil
}

// AuthenticatePeer initiates authentication with a peer
func (h *AuthProtocolHandler) AuthenticatePeer(ctx context.Context, node *model.Node, peerID peer.ID) error {
	// Check if already authenticated
	if h.authManager.IsAuthenticated(peerID) {
		return nil // Already authenticated
	}

	// Create stream to peer
	stream, err := node.Host.NewStream(ctx, peerID, protocol.ID(AuthProtocol))
	if err != nil {
		return fmt.Errorf("failed to create auth stream: %w", err)
	}
	defer stream.Close()

	// Set timeout using config
	authTimeout := time.Duration(h.authManager.node.Config.Auth.TimeoutSeconds) * time.Second
	stream.SetDeadline(time.Now().Add(authTimeout))

	// Send initial challenge request
	challengeReq := AuthMessage{
		Type:      "challenge",
		Timestamp: time.Now().Unix(),
	}

	reqData, err := json.Marshal(challengeReq)
	if err != nil {
		return fmt.Errorf("failed to marshal challenge request: %w", err)
	}

	if _, err := stream.Write(reqData); err != nil {
		return fmt.Errorf("failed to send challenge request: %w", err)
	}

	// Read challenge response
	challengeData, err := io.ReadAll(stream)
	if err != nil {
		return fmt.Errorf("failed to read challenge: %w", err)
	}

	var challengeMsg AuthMessage
	if err := json.Unmarshal(challengeData, &challengeMsg); err != nil {
		return fmt.Errorf("failed to unmarshal challenge: %w", err)
	}

	if challengeMsg.Type != "challenge" {
		return fmt.Errorf("expected challenge, got: %s", challengeMsg.Type)
	}

	// Calculate response HMAC
	hmacHash := hmac.New(sha256.New, []byte(h.authManager.secret))
	hmacHash.Write(challengeMsg.Challenge)
	hmacHash.Write([]byte(node.Host.ID().String())) // Our own peer ID

	response := AuthMessage{
		Type:      "response",
		Response:  hmacHash.Sum(nil),
		Timestamp: time.Now().Unix(),
	}

	responseData, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("failed to marshal auth response: %w", err)
	}

	if _, err := stream.Write(responseData); err != nil {
		return fmt.Errorf("failed to send auth response: %w", err)
	}

	// Read final result
	resultData, err := io.ReadAll(stream)
	if err != nil {
		return fmt.Errorf("failed to read auth result: %w", err)
	}

	var resultMsg AuthMessage
	if err := json.Unmarshal(resultData, &resultMsg); err != nil {
		return fmt.Errorf("failed to unmarshal auth result: %w", err)
	}

	if resultMsg.Type == "success" {
		h.authManager.MarkAuthenticated(peerID)
		return nil
	} else if resultMsg.Type == "error" {
		return fmt.Errorf("authentication rejected: %s", resultMsg.Error)
	}

	return fmt.Errorf("unexpected auth result: %s", resultMsg.Type)
}
