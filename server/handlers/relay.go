package handlers

import (
	"fmt"
	"io"
	"log"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/Rorical/Goxic/server/model"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const (
	ServiceName = "goxic-relay"
	Version     = "1.0.0"
)

// RelayHandler handles traffic relaying between nodes
type RelayHandler struct {
	pattern *regexp.Regexp
}

// NewRelayHandler creates a new relay handler
func NewRelayHandler() *RelayHandler {
	// Pattern: /goxic-proxy/relay/1.0.0/proxy/[proxy-id]/next/[peer-id]/next/[peer-id]...
	pattern := regexp.MustCompile(`^/goxic-proxy/relay/([^/]+)/proxy/([^/]+)(?:/next/(.+))?$`)
	return &RelayHandler{
		pattern: pattern,
	}
}

// Protocol returns the protocol pattern this handler supports
func (h *RelayHandler) Protocol() string {
	return `/goxic-proxy/relay/` + Version
}

// ProtocolMatch returns the protocol matcher function for SetStreamHandlerMatch
func (h *RelayHandler) ProtocolMatch() func(protocol.ID) bool {
	return func(proto protocol.ID) bool {
		return h.pattern.MatchString(string(proto))
	}
}

// Handle processes incoming relay streams
func (h *RelayHandler) Handle(stream network.Stream, node *model.Node) error {
	stream.Scope().SetService(ServiceName)

	log.Printf("Relay stream from peer: %s", stream.Conn().RemotePeer().String())

	protocolStr := string(stream.Protocol())
	subMatches := h.pattern.FindStringSubmatch(protocolStr)

	if len(subMatches) < 3 {
		log.Printf("Invalid relay protocol: %s", protocolStr)
		return fmt.Errorf("invalid relay protocol: %s", protocolStr)
	}

	version := subMatches[1]
	proxyID := subMatches[2]
	nextPeersStr := subMatches[3]

	log.Printf("Relay request - Version: %s, ProxyID: %s", version, proxyID)

	if nextPeersStr != "" {
		// Forward to the next peer in the chain
		return h.forwardToNextPeer(stream, node, version, proxyID, nextPeersStr)
	} else {
		// Final destination - handle proxy traffic
		return h.handleProxyTraffic(stream, node, proxyID)
	}
}

// forwardToNextPeer forwards the stream to the next peer in the relay chain
func (h *RelayHandler) forwardToNextPeer(income network.Stream, node *model.Node, version, proxyID, nextPeersStr string) error {
	nextPeers := strings.Split(nextPeersStr, "/next/")
	if len(nextPeers) < 1 || nextPeers[0] == "" {
		return fmt.Errorf("invalid next peers: %s", nextPeersStr)
	}

	nextPeerID, err := peer.Decode(nextPeers[0])
	if err != nil {
		log.Printf("Invalid peer ID: %s", nextPeers[0])
		return err
	}

	log.Printf("Forwarding to next peer: %s", nextPeerID.String())

	// Rebuild protocol ID for the next hop
	protocolID := fmt.Sprintf("/goxic-proxy/relay/%s/proxy/%s", version, proxyID)
	if len(nextPeers) > 1 {
		for _, peerID := range nextPeers[1:] {
			if peerID != "" {
				protocolID += fmt.Sprintf("/next/%s", peerID)
			}
		}
	}

	// Create outgoing stream to next peer
	outcome, err := node.Host.NewStream(node.CTX, nextPeerID, protocol.ID(protocolID))
	if err != nil {
		log.Printf("Failed to create stream to %s: %v", nextPeerID.String(), err)
		return err
	}
	defer outcome.Close()
	outcome.Scope().SetService(ServiceName)

	// Bidirectional relay with proper cleanup
	done := make(chan error, 2)

	go func() {
		defer outcome.CloseWrite()
		_, err := io.Copy(outcome, income)
		if err != nil {
			log.Printf("income -> outcome relay error: %v", err)
		}
		done <- err
	}()

	go func() {
		defer income.CloseWrite()
		_, err := io.Copy(income, outcome)
		if err != nil {
			log.Printf("outcome -> income relay error: %v", err)
		}
		done <- err
	}()

	// Wait for either direction to complete
	err = <-done
	if err != nil {
		log.Printf("Relay forwarding completed with error: %v", err)
	}

	// Ensure both streams are closed
	outcome.Close()
	income.Close()

	return err
}

// handleProxyTraffic handles the final proxy traffic (exit node functionality)
func (h *RelayHandler) handleProxyTraffic(stream network.Stream, node *model.Node, proxyID string) error {
	log.Printf("Handling proxy traffic for ID: %s", proxyID)

	// Read target address from client
	targetAddr, err := h.readTargetAddr(stream)
	if err != nil {
		log.Printf("Failed to read target address: %v", err)
		return err
	}

	log.Printf("Exit node connecting to: %s", targetAddr)

	// Establish connection to target destination
	targetConn, err := net.DialTimeout("tcp", targetAddr, time.Second*30)
	if err != nil {
		log.Printf("Failed to connect to target %s: %v", targetAddr, err)
		return err
	}
	defer targetConn.Close()

	// Configure connection for long-lived sessions (like SSH)
	if tcpConn, ok := targetConn.(*net.TCPConn); ok {
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(time.Second * 30)
		tcpConn.SetNoDelay(true) // Disable Nagle's algorithm for interactive sessions
	}

	log.Printf("Successfully connected to target: %s", targetAddr)

	// Bridge traffic between libp2p stream and target connection
	return h.bridgeExitTraffic(stream, targetConn)
}

// readTargetAddr reads the target address sent by the client
func (h *RelayHandler) readTargetAddr(stream network.Stream) (string, error) {
	// Read address length
	lengthBytes := make([]byte, 1)
	if _, err := stream.Read(lengthBytes); err != nil {
		return "", fmt.Errorf("failed to read address length: %w", err)
	}

	// Read target address
	addrLength := int(lengthBytes[0])
	if addrLength == 0 || addrLength > 255 {
		return "", fmt.Errorf("invalid address length: %d", addrLength)
	}

	addrBytes := make([]byte, addrLength)
	if _, err := io.ReadFull(stream, addrBytes); err != nil {
		return "", fmt.Errorf("failed to read target address: %w", err)
	}

	return string(addrBytes), nil
}

// bridgeExitTraffic bridges traffic between libp2p stream and target connection
func (h *RelayHandler) bridgeExitTraffic(libp2pStream network.Stream, targetConn net.Conn) error {
	done := make(chan error, 2)

	// Remove any existing deadlines to prevent timeouts on long-lived connections
	libp2pStream.SetDeadline(time.Time{})
	targetConn.SetDeadline(time.Time{})

	log.Printf("Starting traffic bridge for long-lived connection")

	// libp2p -> target
	go func() {
		defer func() {
			if tcpConn, ok := targetConn.(*net.TCPConn); ok {
				tcpConn.CloseWrite() // Signal end of writes
			}
		}()
		_, err := io.Copy(targetConn, libp2pStream)
		if err != nil {
			log.Printf("libp2p -> target copy error: %v", err)
		}
		done <- err
	}()

	// target -> libp2p
	go func() {
		defer func() {
			libp2pStream.CloseWrite() // Signal end of writes
		}()
		_, err := io.Copy(libp2pStream, targetConn)
		if err != nil {
			log.Printf("target -> libp2p copy error: %v", err)
		}
		done <- err
	}()

	// Wait for either direction to complete
	err := <-done

	// Close both connections to ensure cleanup
	targetConn.Close()
	libp2pStream.Close()

	log.Printf("Exit connection closed for target")
	return err
}

// OpenRelay creates a new relay connection through specified peers
func OpenRelay(node *model.Node, proxyID string, relayPeers []peer.ID) (network.Stream, error) {
	var firstPeerID peer.ID

	if len(relayPeers) == 0 {
		return nil, fmt.Errorf("no relay peers specified")
	}

	firstPeerID = relayPeers[0]

	// Build protocol ID
	protocolID := fmt.Sprintf("/goxic-proxy/relay/%s/proxy/%s", Version, proxyID)
	if len(relayPeers) > 1 {
		for _, peerID := range relayPeers[1:] {
			protocolID += fmt.Sprintf("/next/%s", peerID.String())
		}
	}

	log.Printf("Opening relay stream to %s with protocol: %s", firstPeerID.String(), protocolID)

	stream, err := node.Host.NewStream(node.CTX, firstPeerID, protocol.ID(protocolID))
	if err != nil {
		return nil, fmt.Errorf("failed to create relay stream: %w", err)
	}

	stream.Scope().SetService(ServiceName)
	return stream, nil
}
