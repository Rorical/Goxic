package handlers

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/Rorical/Goxic/server/model"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
)

// ConnectionGuard manages authenticated connections and intercepts stream requests
type ConnectionGuard struct {
	authManager     *AuthenticatedPeerManager
	authHandler     *AuthProtocolHandler
	node            *model.Node
	originalHandler network.StreamHandler
	mu              sync.RWMutex
	pendingAuth     map[peer.ID]bool // Track peers currently being authenticated
}

// NewConnectionGuard creates a new connection guard
func NewConnectionGuard(node *model.Node, secret string) *ConnectionGuard {
	authManager := NewAuthenticatedPeerManager(node, secret)
	authHandler := NewAuthProtocolHandler(authManager)

	return &ConnectionGuard{
		authManager: authManager,
		authHandler: authHandler,
		node:        node,
		pendingAuth: make(map[peer.ID]bool),
	}
}

// SetOriginalHandler sets the original stream handler to call after authentication
func (cg *ConnectionGuard) SetOriginalHandler(handler network.StreamHandler) {
	cg.originalHandler = handler
}

// GuardedStreamHandler is the intercepting stream handler
func (cg *ConnectionGuard) GuardedStreamHandler(stream network.Stream) {
	remotePeer := stream.Conn().RemotePeer()
	streamProtocol := stream.Protocol()

	// Always allow auth protocol through
	if streamProtocol == protocol.ID(AuthProtocol) {
		cg.authHandler.Handle(stream, cg.node)
		return
	}

	// Check if peer is authenticated
	if !cg.authManager.IsAuthenticated(remotePeer) {
		log.Printf("Rejecting stream from unauthenticated peer %s (protocol: %s)", remotePeer, streamProtocol)
		stream.Reset()

		// Trigger authentication for this peer
		go cg.authenticatePeerAsync(remotePeer)
		return
	}

	// Peer is authenticated, allow the stream through to original handler
	if cg.originalHandler != nil {
		cg.originalHandler(stream)
	} else {
		log.Printf("No original handler set, closing authenticated stream")
		stream.Close()
	}
}

// authenticatePeerAsync attempts to authenticate a peer asynchronously
func (cg *ConnectionGuard) authenticatePeerAsync(peerID peer.ID) {
	cg.mu.Lock()
	// Check if authentication is already in progress
	if cg.pendingAuth[peerID] {
		cg.mu.Unlock()
		return
	}
	cg.pendingAuth[peerID] = true
	cg.mu.Unlock()

	// Clean up pending status when done
	defer func() {
		cg.mu.Lock()
		delete(cg.pendingAuth, peerID)
		cg.mu.Unlock()
	}()

	authTimeout := time.Duration(cg.node.Config.Auth.TimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), authTimeout)
	defer cancel()

	log.Printf("Attempting to authenticate peer %s", peerID)

	if err := cg.authHandler.AuthenticatePeer(ctx, cg.node, peerID); err != nil {
		log.Printf("Failed to authenticate peer %s: %v", peerID, err)

		// Disconnect from unauthenticated peer
		if err := cg.node.Host.Network().ClosePeer(peerID); err != nil {
			log.Printf("Failed to disconnect from unauthenticated peer %s: %v", peerID, err)
		}
	} else {
		log.Printf("Successfully authenticated peer %s", peerID)
	}
}

// AuthenticateConnectedPeers authenticates all currently connected peers
func (cg *ConnectionGuard) AuthenticateConnectedPeers(ctx context.Context) {
	connectedPeers := cg.node.Host.Network().Peers()

	log.Printf("Authenticating %d connected peers", len(connectedPeers))

	for _, peerID := range connectedPeers {
		// Skip if already authenticated
		if cg.authManager.IsAuthenticated(peerID) {
			continue
		}

		// Authenticate asynchronously
		go cg.authenticatePeerAsync(peerID)
	}
}

// StartPeriodicCleanup starts periodic cleanup of expired authentications and disconnected peers
func (cg *ConnectionGuard) StartPeriodicCleanup(ctx context.Context) {
	cleanupInterval := time.Duration(cg.node.Config.Auth.CleanupIntervalMin) * time.Minute
	ticker := time.NewTicker(cleanupInterval)

	go func() {
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				cg.cleanupExpiredAuth()
			case <-ctx.Done():
				return
			}
		}
	}()
}

// cleanupExpiredAuth removes expired authentications and disconnected peers
func (cg *ConnectionGuard) cleanupExpiredAuth() {
	connectedPeers := make(map[peer.ID]bool)
	for _, peerID := range cg.node.Host.Network().Peers() {
		connectedPeers[peerID] = true
	}

	authenticatedPeers := cg.authManager.GetAuthenticatedPeers()
	removedCount := 0

	for _, peerID := range authenticatedPeers {
		// Remove authentication for disconnected peers
		if !connectedPeers[peerID] {
			cg.authManager.RemoveAuthentication(peerID)
			removedCount++
		}
	}

	if removedCount > 0 {
		log.Printf("Cleaned up authentication for %d disconnected peers", removedCount)
	}
}

// GetAuthenticatedPeers returns list of authenticated peers
func (cg *ConnectionGuard) GetAuthenticatedPeers() []peer.ID {
	return cg.authManager.GetAuthenticatedPeers()
}

// IsAuthenticated checks if a peer is authenticated
func (cg *ConnectionGuard) IsAuthenticated(peerID peer.ID) bool {
	return cg.authManager.IsAuthenticated(peerID)
}

// ForceAuthentication forces authentication attempt with a specific peer
func (cg *ConnectionGuard) ForceAuthentication(ctx context.Context, peerID peer.ID) error {
	return cg.authHandler.AuthenticatePeer(ctx, cg.node, peerID)
}

// NetworkNotifiee handles network events for authentication
type AuthNetworkNotifiee struct {
	guard *ConnectionGuard
}

// NewAuthNetworkNotifiee creates a new network notifiee for authentication
func NewAuthNetworkNotifiee(guard *ConnectionGuard) *AuthNetworkNotifiee {
	return &AuthNetworkNotifiee{
		guard: guard,
	}
}

// Connected is called when a new peer connects
func (n *AuthNetworkNotifiee) Connected(net network.Network, conn network.Conn) {
	peerID := conn.RemotePeer()
	log.Printf("New peer connected: %s, initiating authentication", peerID)

	// Authenticate the new peer
	go n.guard.authenticatePeerAsync(peerID)
}

// Disconnected is called when a peer disconnects
func (n *AuthNetworkNotifiee) Disconnected(net network.Network, conn network.Conn) {
	peerID := conn.RemotePeer()
	log.Printf("Peer disconnected: %s, removing authentication", peerID)

	// Remove authentication for disconnected peer
	n.guard.authManager.RemoveAuthentication(peerID)
}

// Listen is called when we start listening on an address
func (n *AuthNetworkNotifiee) Listen(net network.Network, addr ma.Multiaddr) {}

// ListenClose is called when we stop listening on an address
func (n *AuthNetworkNotifiee) ListenClose(net network.Network, addr ma.Multiaddr) {}
