package handlers

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Rorical/Goxic/server/model"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// SOCKS5Handler manages the local SOCKS5 proxy server for client nodes
type SOCKS5Handler struct {
	listener          net.Listener
	ctx               context.Context
	cancel            context.CancelFunc
	port              int
	discovery         *ServerDiscovery
	capabilityManager *CapabilityManager
	activeConnections int32 // Current number of active connections
	config            *model.SOCKS5Config
}

// NewSOCKS5Handler creates a new SOCKS5 handler with config
func NewSOCKS5Handler(config *model.SOCKS5Config) *SOCKS5Handler {
	return &SOCKS5Handler{
		port:   config.Port,
		config: config,
	}
}

// NewSOCKS5HandlerWithCapabilities creates a new SOCKS5 handler with config and capability manager
func NewSOCKS5HandlerWithCapabilities(config *model.SOCKS5Config, capabilityManager *CapabilityManager) *SOCKS5Handler {
	return &SOCKS5Handler{
		port:              config.Port,
		config:            config,
		capabilityManager: capabilityManager,
	}
}

// Start initializes and starts the SOCKS5 server
func (s *SOCKS5Handler) Start(ctx context.Context, node *model.Node) error {
	s.ctx, s.cancel = context.WithCancel(ctx)

	// Initialize capability manager for client node if not provided
	if s.capabilityManager == nil {
		s.capabilityManager = NewCapabilityManager(node, "client")
		if err := s.capabilityManager.AdvertiseCapabilities(); err != nil {
			return fmt.Errorf("failed to advertise capabilities: %w", err)
		}
	}

	// Initialize server discovery with capability manager
	s.discovery = NewServerDiscovery(node, s.capabilityManager)
	s.discovery.StartPeriodicDiscovery(ctx)

	addr := fmt.Sprintf("%s:%d", node.Config.SOCKS5.BindAddress, s.port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to start SOCKS5 server on %s: %w", addr, err)
	}

	s.listener = listener
	log.Printf("SOCKS5 proxy listening on %s", addr)

	go s.acceptConnections(node)
	return nil
}

// Stop gracefully stops the SOCKS5 server
func (s *SOCKS5Handler) Stop() error {
	if s.cancel != nil {
		s.cancel()
	}
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// acceptConnections handles incoming SOCKS5 connections
func (s *SOCKS5Handler) acceptConnections(node *model.Node) {
	for {
		select {
		case <-s.ctx.Done():
			return
		default:
			conn, err := s.listener.Accept()
			if err != nil {
				if s.ctx.Err() != nil {
					return // Context cancelled
				}
				log.Printf("Failed to accept SOCKS5 connection: %v", err)
				continue
			}

			go s.handleConnection(conn, node)
		}
	}
}

// handleConnection processes a single SOCKS5 connection
func (s *SOCKS5Handler) handleConnection(conn net.Conn, node *model.Node) {
	defer conn.Close()
	defer atomic.AddInt32(&s.activeConnections, -1)

	// Check connection limits
	if atomic.LoadInt32(&s.activeConnections) > int32(s.config.MaxConnections) {
		log.Printf("SOCKS5 connection limit reached, rejecting connection from %s",
			conn.RemoteAddr().String())
		return
	}

	atomic.AddInt32(&s.activeConnections, 1)

	// Set connection timeout only for the initial handshake phase
	handshakeTimeout := time.Duration(s.config.ConnectionTimeout) * time.Second
	conn.SetDeadline(time.Now().Add(handshakeTimeout))

	log.Printf("New SOCKS5 connection from %s (active: %d/%d)",
		conn.RemoteAddr().String(),
		atomic.LoadInt32(&s.activeConnections),
		s.config.MaxConnections)

	// SOCKS5 authentication negotiation
	if err := s.handleAuthentication(conn); err != nil {
		log.Printf("SOCKS5 authentication failed: %v", err)
		return
	}

	// SOCKS5 request handling
	targetAddr, err := s.handleRequest(conn)
	if err != nil {
		log.Printf("SOCKS5 request failed: %v", err)
		return
	}

	log.Printf("SOCKS5 request for: %s", targetAddr)

	// Select an available server node
	serverPeer, err := s.discovery.SelectServer(s.ctx)
	if err != nil {
		log.Printf("Failed to select server: %v", err)
		s.sendConnectResponse(conn, 0x01) // General failure
		return
	}

	log.Printf("Selected server: %s for target: %s", serverPeer.ID, targetAddr)

	// Generate unique proxy ID for this connection
	proxyID := fmt.Sprintf("proxy-%d", time.Now().UnixNano())

	// Create libp2p stream to server
	libp2pStream, err := s.createProxyStream(node, *serverPeer, proxyID)
	if err != nil {
		log.Printf("Failed to create proxy stream: %v", err)
		s.sendConnectResponse(conn, 0x01) // General failure
		return
	}
	defer libp2pStream.Close()

	// Send SOCKS5 success response
	s.sendConnectResponse(conn, 0x00) // Success

	// Send target address to server through stream
	if err := s.sendTargetAddr(libp2pStream, targetAddr); err != nil {
		log.Printf("Failed to send target address: %v", err)
		return
	}

	// Remove handshake timeout for long-lived connections like SSH
	conn.SetDeadline(time.Time{})
	log.Printf("Handshake completed, removing connection timeout for long-lived session")

	// Bridge traffic between SOCKS5 client and libp2p stream
	s.bridgeTraffic(conn, libp2pStream)

	// Release connection tracking when done
	s.discovery.ReleaseConnection(serverPeer.ID)
	log.Printf("Released connection tracking for server: %s", serverPeer.ID)
}

// handleAuthentication handles SOCKS5 authentication negotiation
func (s *SOCKS5Handler) handleAuthentication(conn net.Conn) error {
	// Read version and method count
	buf := make([]byte, 2)
	if _, err := conn.Read(buf); err != nil {
		return err
	}

	version := buf[0]
	nmethods := buf[1]

	if version != 0x05 {
		return fmt.Errorf("unsupported SOCKS version: %d", version)
	}

	// Read methods
	methods := make([]byte, nmethods)
	if _, err := conn.Read(methods); err != nil {
		return err
	}

	// Check for "no authentication" method (0x00)
	noAuthSupported := false
	for _, method := range methods {
		if method == 0x00 {
			noAuthSupported = true
			break
		}
	}

	if !noAuthSupported {
		// Send "no acceptable methods"
		conn.Write([]byte{0x05, 0xFF})
		return fmt.Errorf("no acceptable authentication methods")
	}

	// Send "no authentication required"
	_, err := conn.Write([]byte{0x05, 0x00})
	return err
}

// handleRequest handles SOCKS5 connection request
func (s *SOCKS5Handler) handleRequest(conn net.Conn) (string, error) {
	// Read request header
	buf := make([]byte, 4)
	if _, err := conn.Read(buf); err != nil {
		return "", err
	}

	version := buf[0]
	cmd := buf[1]
	// rsv := buf[2] // Reserved, ignore
	atyp := buf[3]

	if version != 0x05 {
		return "", fmt.Errorf("unsupported SOCKS version: %d", version)
	}

	if cmd != 0x01 { // Only support CONNECT
		s.sendConnectResponse(conn, 0x07) // Command not supported
		return "", fmt.Errorf("unsupported command: %d", cmd)
	}

	// Read address
	var addr string
	var err error

	switch atyp {
	case 0x01: // IPv4
		ipBuf := make([]byte, 4)
		if _, err := conn.Read(ipBuf); err != nil {
			return "", err
		}
		addr = net.IP(ipBuf).String()

	case 0x03: // Domain name
		lenBuf := make([]byte, 1)
		if _, err := conn.Read(lenBuf); err != nil {
			return "", err
		}
		domainBuf := make([]byte, lenBuf[0])
		if _, err := conn.Read(domainBuf); err != nil {
			return "", err
		}
		addr = string(domainBuf)

	case 0x04: // IPv6
		ipBuf := make([]byte, 16)
		if _, err := conn.Read(ipBuf); err != nil {
			return "", err
		}
		addr = net.IP(ipBuf).String()

	default:
		s.sendConnectResponse(conn, 0x08) // Address type not supported
		return "", fmt.Errorf("unsupported address type: %d", atyp)
	}

	// Read port
	portBuf := make([]byte, 2)
	if _, err = conn.Read(portBuf); err != nil {
		return "", err
	}
	port := (uint16(portBuf[0]) << 8) | uint16(portBuf[1])

	targetAddr := net.JoinHostPort(addr, strconv.Itoa(int(port)))
	return targetAddr, nil
}

// sendConnectResponse sends SOCKS5 connect response
func (s *SOCKS5Handler) sendConnectResponse(conn net.Conn, status byte) {
	// Simple response with all zeros for bound address and port
	response := []byte{
		0x05,                   // Version
		status,                 // Status
		0x00,                   // Reserved
		0x01,                   // Address type: IPv4
		0x00, 0x00, 0x00, 0x00, // Bound address: 0.0.0.0
		0x00, 0x00, // Bound port: 0
	}
	conn.Write(response)
}

// createProxyStream creates a libp2p stream for proxy traffic
func (s *SOCKS5Handler) createProxyStream(node *model.Node, serverPeer peer.AddrInfo, proxyID string) (network.Stream, error) {
	// For direct connection (no relay hops)
	relayPeers := []peer.ID{serverPeer.ID}

	log.Printf("Creating proxy stream to %s for proxy ID: %s", serverPeer.ID, proxyID)

	// Use the relay handler to create stream
	stream, err := OpenRelay(node, proxyID, relayPeers)
	if err != nil {
		log.Printf("Failed to open relay stream to %s: %v", serverPeer.ID, err)
		return nil, fmt.Errorf("failed to open relay stream: %w", err)
	}

	log.Printf("Successfully created proxy stream to %s", serverPeer.ID)

	return stream, nil
}

// sendTargetAddr sends the target address to the server through the stream
func (s *SOCKS5Handler) sendTargetAddr(stream network.Stream, targetAddr string) error {
	// Send target address length
	addrBytes := []byte(targetAddr)
	lengthBytes := []byte{byte(len(addrBytes))}

	if _, err := stream.Write(lengthBytes); err != nil {
		return fmt.Errorf("failed to write address length: %w", err)
	}

	// Send target address
	if _, err := stream.Write(addrBytes); err != nil {
		return fmt.Errorf("failed to write target address: %w", err)
	}

	return nil
}

// bridgeTraffic bridges data between SOCKS5 connection and libp2p stream
func (s *SOCKS5Handler) bridgeTraffic(socksConn net.Conn, libp2pStream network.Stream) {
	done := make(chan error, 2)

	// Configure connection for long-lived sessions like SSH
	if tcpConn, ok := socksConn.(*net.TCPConn); ok {
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(time.Second * 30)
		tcpConn.SetNoDelay(true) // Disable Nagle's algorithm for interactive sessions
	}

	// SOCKS5 -> libp2p
	go func() {
		defer func() {
			libp2pStream.CloseWrite()
			log.Printf("SOCKS5 -> libp2p direction closed")
		}()
		_, err := io.Copy(libp2pStream, socksConn)
		if err != nil {
			log.Printf("SOCKS5 -> libp2p copy error: %v", err)
		}
		done <- err
	}()

	// libp2p -> SOCKS5
	go func() {
		defer func() {
			if tcpConn, ok := socksConn.(*net.TCPConn); ok {
				tcpConn.CloseWrite()
			}
			log.Printf("libp2p -> SOCKS5 direction closed")
		}()
		_, err := io.Copy(socksConn, libp2pStream)
		if err != nil {
			log.Printf("libp2p -> SOCKS5 copy error: %v", err)
		}
		done <- err
	}()

	// Wait for either direction to complete
	err := <-done
	if err != nil {
		log.Printf("Bridge completed with error: %v", err)
	}

	// Ensure cleanup
	socksConn.Close()
	libp2pStream.Close()

	log.Printf("Proxy connection closed")
}
