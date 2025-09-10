package handlers

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/Rorical/Goxic/server/model"
	"github.com/libp2p/go-libp2p/core/peer"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
)

// ServerLoadInfo tracks load information for a server
type ServerLoadInfo struct {
	PeerID       peer.ID       `json:"peer_id"`
	ActiveConns  int           `json:"active_connections"`
	TotalConns   int           `json:"total_connections"`
	LastUsed     time.Time     `json:"last_used"`
	ResponseTime time.Duration `json:"response_time"` // Future: track response times
}

// PeerFailureInfo tracks connection failure history for peers
type PeerFailureInfo struct {
	PeerID       peer.ID   `json:"peer_id"`
	FailureCount int       `json:"failure_count"`
	LastFailure  time.Time `json:"last_failure"`
	LastSuccess  time.Time `json:"last_success"`
}

// ServerDiscovery manages discovery of available server nodes
type ServerDiscovery struct {
	node             *model.Node
	routingDiscovery *drouting.RoutingDiscovery
	serverPeers      []peer.AddrInfo
	lastUpdate       time.Time
	updateInterval   time.Duration
	// Load balancing
	serverLoads map[peer.ID]*ServerLoadInfo
	loadMutex   sync.RWMutex
	// Connection failure tracking
	peerFailures   map[peer.ID]*PeerFailureInfo
	failureMutex   sync.RWMutex
	maxFailures    int           // Max failures before blacklisting
	blacklistTime  time.Duration // How long to avoid failed peers
	// NodeHandler interface support
	ctx       context.Context
	ctxCancel context.CancelFunc
	started   bool
}

// NewServerDiscovery creates a new server discovery instance
func NewServerDiscovery(node *model.Node) *ServerDiscovery {
	updateInterval := time.Duration(node.Config.Discovery.UpdateIntervalSec) * time.Second
	return &ServerDiscovery{
		node:             node,
		routingDiscovery: drouting.NewRoutingDiscovery(node.Router),
		serverPeers:      make([]peer.AddrInfo, 0),
		updateInterval:   updateInterval,
		serverLoads:      make(map[peer.ID]*ServerLoadInfo),
		loadMutex:        sync.RWMutex{},
		peerFailures:     make(map[peer.ID]*PeerFailureInfo),
		failureMutex:     sync.RWMutex{},
		maxFailures:      3,                // Avoid peers after 3 consecutive failures
		blacklistTime:    10 * time.Minute, // Avoid failed peers for 10 minutes
	}
}

// GetAvailableServers returns list of available server nodes
func (sd *ServerDiscovery) GetAvailableServers(ctx context.Context) ([]peer.AddrInfo, error) {
	// Refresh if cache is stale
	if time.Since(sd.lastUpdate) > sd.updateInterval {
		// Determine if this is being called by a client (via SOCKS5) or server (via mesh)
		// If ServerDiscovery was not started via Start() method, it's client-side usage
		var refreshErr error
		if !sd.started {
			// Client-side discovery: don't filter out "client" nodes
			refreshErr = sd.refreshServerListForClient(ctx)
		} else {
			// Server-side discovery: filter to maintain server mesh
			refreshErr = sd.refreshServerList(ctx)
		}

		if refreshErr != nil {
			log.Printf("Failed to refresh server list: %v", refreshErr)
			// Return cached list even if refresh failed
		}
	}

	// Filter peers based on usage context (client vs server)
	availableServers := make([]peer.AddrInfo, 0)
	for _, server := range sd.serverPeers {
		// Skip self
		if server.ID == sd.node.Host.ID() {
			continue
		}

		// Check if we're still connected
		if sd.node.Host.Network().Connectedness(server.ID) != 1 { // Not connected
			log.Printf("Peer %s no longer connected, skipping", server.ID)
			continue
		}

		// For server-to-server mesh, filter to only exit-capable servers
		// For client usage, include all connected peers and let protocol negotiation handle the rest
		if sd.started {
			// Server mesh mode: only include exit-capable servers
			if !sd.isExitCapableServer(server.ID) {
				log.Printf("Server %s no longer supports relay protocol, filtering out", server.ID)
				continue
			}
			log.Printf("Server mesh: including exit-capable server %s", server.ID)
		} else {
			// Client mode: include all connected peers
			log.Printf("Client mode: including peer %s (protocol verification during connection)", server.ID)
		}

		availableServers = append(availableServers, server)
	}

	if sd.started {
		log.Printf("GetAvailableServers (server mesh): returning %d exit-capable servers from %d cached servers",
			len(availableServers), len(sd.serverPeers))
	} else {
		log.Printf("GetAvailableServers (client mode): returning %d peers from %d cached peers",
			len(availableServers), len(sd.serverPeers))
	}

	return availableServers, nil
}

// SelectServer selects a server from available servers using configured strategy
func (sd *ServerDiscovery) SelectServer(ctx context.Context) (*peer.AddrInfo, error) {
	servers, err := sd.GetAvailableServers(ctx)
	if err != nil {
		return nil, err
	}

	if len(servers) == 0 {
		return nil, fmt.Errorf("no available server nodes found")
	}

	strategy := sd.node.Config.Discovery.SelectionStrategy
	log.Printf("Selecting server using strategy: %s from %d available servers", strategy, len(servers))

	switch strategy {
	case model.SelectionFirst:
		return sd.selectFirstServer(servers)

	case model.SelectionRandom:
		return sd.selectRandomServer(servers)

	case model.SelectionLoadBalance:
		return sd.selectLoadBalancedServer(servers)

	case model.SelectionPreferred:
		return sd.selectPreferredServer(servers)

	default:
		log.Printf("Unknown selection strategy '%s', falling back to first", strategy)
		return sd.selectFirstServer(servers)
	}
}

// selectFirstServer always returns the first server (deterministic)
func (sd *ServerDiscovery) selectFirstServer(servers []peer.AddrInfo) (*peer.AddrInfo, error) {
	selected := &servers[0]
	sd.TrackConnection(selected.ID)
	log.Printf("Selected first available server: %s", selected.ID)
	return selected, nil
}

// selectRandomServer returns a random server
func (sd *ServerDiscovery) selectRandomServer(servers []peer.AddrInfo) (*peer.AddrInfo, error) {
	index := rand.Intn(len(servers))
	selected := &servers[index]
	sd.TrackConnection(selected.ID)
	log.Printf("Selected random server: %s", selected.ID)
	return selected, nil
}

// selectLoadBalancedServer selects server with lowest load
func (sd *ServerDiscovery) selectLoadBalancedServer(servers []peer.AddrInfo) (*peer.AddrInfo, error) {
	var bestServer *peer.AddrInfo
	bestLoad := int(^uint(0) >> 1)
	func() {
		sd.loadMutex.RLock()
		defer sd.loadMutex.RUnlock()

		for i := range servers {
			server := &servers[i]
			loadInfo, exists := sd.serverLoads[server.ID]

			currentLoad := 0
			if exists {
				currentLoad = loadInfo.ActiveConns
			}

			if currentLoad < bestLoad {
				bestLoad = currentLoad
				bestServer = server
			}

			log.Printf("Server %s load: %d active connections", server.ID, currentLoad)
		}

		if bestServer == nil {
			bestServer = &servers[0] // Fallback
		}
	}()
	sd.TrackConnection(bestServer.ID)
	log.Printf("Selected load-balanced server: %s (load: %d)", bestServer.ID, bestLoad)
	return bestServer, nil
}

// selectPreferredServer selects from preferred list first, then falls back
func (sd *ServerDiscovery) selectPreferredServer(servers []peer.AddrInfo) (*peer.AddrInfo, error) {
	preferredIDs := sd.node.Config.Discovery.PreferredExitNodes

	// First try to find preferred servers that are available
	preferredServers := make([]peer.AddrInfo, 0)
	for _, server := range servers {
		peerIDStr := server.ID.String()
		for _, preferredID := range preferredIDs {
			if peerIDStr == preferredID {
				preferredServers = append(preferredServers, server)
				break
			}
		}
	}

	// If preferred servers are available, use load balancing among them
	if len(preferredServers) > 0 {
		log.Printf("Found %d preferred servers available, selecting with load balancing", len(preferredServers))
		return sd.selectLoadBalancedServer(preferredServers)
	}

	// No preferred servers available, fall back to load balancing on all servers
	log.Printf("No preferred servers available, falling back to load balancing")
	return sd.selectLoadBalancedServer(servers)
}

// TrackConnection tracks a new connection to a server
func (sd *ServerDiscovery) TrackConnection(peerID peer.ID) {
	sd.loadMutex.Lock()
	defer sd.loadMutex.Unlock()

	if loadInfo, exists := sd.serverLoads[peerID]; exists {
		loadInfo.ActiveConns++
		loadInfo.TotalConns++
		loadInfo.LastUsed = time.Now()
	} else {
		sd.serverLoads[peerID] = &ServerLoadInfo{
			PeerID:      peerID,
			ActiveConns: 1,
			TotalConns:  1,
			LastUsed:    time.Now(),
		}
	}
}

// ReleaseConnection tracks when a connection to a server is closed
func (sd *ServerDiscovery) ReleaseConnection(peerID peer.ID) {
	sd.loadMutex.Lock()
	defer sd.loadMutex.Unlock()

	if loadInfo, exists := sd.serverLoads[peerID]; exists {
		loadInfo.ActiveConns--
		if loadInfo.ActiveConns < 0 {
			loadInfo.ActiveConns = 0 // Safety check
		}
	}
}

// GetServerLoad returns current load info for a server
func (sd *ServerDiscovery) GetServerLoad(peerID peer.ID) *ServerLoadInfo {
	sd.loadMutex.RLock()
	defer sd.loadMutex.RUnlock()

	if loadInfo, exists := sd.serverLoads[peerID]; exists {
		// Return a copy to avoid race conditions
		return &ServerLoadInfo{
			PeerID:      loadInfo.PeerID,
			ActiveConns: loadInfo.ActiveConns,
			TotalConns:  loadInfo.TotalConns,
			LastUsed:    loadInfo.LastUsed,
		}
	}
	return nil
}

// refreshServerList discovers server nodes from DHT
func (sd *ServerDiscovery) refreshServerList(ctx context.Context) error {
	// Create a context with timeout for discovery
	queryTimeout := time.Duration(sd.node.Config.Discovery.QueryTimeoutSec) * time.Second
	discoveryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	// Discover peers advertising the service
	peerChan, err := sd.routingDiscovery.FindPeers(discoveryCtx, sd.node.Config.Network.Name)
	if err != nil {
		return fmt.Errorf("failed to find peers: %w", err)
	}

	newServers := make([]peer.AddrInfo, 0)

	// Collect discovered peers (limit by config)
	maxServers := sd.node.Config.Discovery.MaxServers
	peerCount := 0
	connectedServers := 0
	filteredOutClients := 0
	skippedBlacklisted := 0

	for peerInfo := range peerChan {
		if peerInfo.ID == sd.node.Host.ID() {
			continue // Skip self
		}

		if peerCount >= maxServers {
			break // Stop collecting after reaching limit
		}

		// Skip peers that have failed repeatedly
		if sd.isBlacklisted(peerInfo.ID) {
			skippedBlacklisted++
			continue
		}

		// Try to connect with shorter timeout to avoid hanging on NAT/firewall issues
		connectedness := sd.node.Host.Network().Connectedness(peerInfo.ID)
		if connectedness == 0 { // Not connected
			// Use shorter timeout for server discovery to avoid blocking on client nodes behind NAT
			shortTimeout := 5 * time.Second
			connectCtx, connectCancel := context.WithTimeout(ctx, shortTimeout)
			
			log.Printf("Attempting to connect to discovered peer %s...", peerInfo.ID)
			if err := sd.node.Host.Connect(connectCtx, peerInfo); err != nil {
				log.Printf("Server discovery: Failed to connect to peer %s (likely client behind NAT): %v", peerInfo.ID, err)
				connectCancel()
				// Record the failure and skip this peer
				sd.recordConnectionFailure(peerInfo.ID)
				continue
			}
			connectCancel()
			log.Printf("Server discovery: Successfully connected to peer %s", peerInfo.ID)
			sd.recordConnectionSuccess(peerInfo.ID)
			connectedServers++
		}

		// Give peer some time to register protocols after connection
		time.Sleep(100 * time.Millisecond)

		// Check if this peer is an exit-capable server
		if !sd.isExitCapableServer(peerInfo.ID) {
			log.Printf("Server discovery: Peer %s is not an exit-capable server, disconnecting", peerInfo.ID)
			filteredOutClients++
			// Disconnect from client nodes to save resources
			if connectedness == 0 { // We just connected
				sd.node.Host.Network().ClosePeer(peerInfo.ID)
			}
			continue
		}

		log.Printf("Server discovery: Verified exit-capable server %s, adding to mesh", peerInfo.ID)
		newServers = append(newServers, peerInfo)
		peerCount++
	}

	sd.serverPeers = newServers
	sd.lastUpdate = time.Now()

	log.Printf("Server discovery complete: %d exit-capable servers found, %d non-servers filtered out, %d successful connections, %d blacklisted peers skipped",
		len(newServers), filteredOutClients, connectedServers, skippedBlacklisted)
	return nil
}

// Start implements NodeHandler interface - starts periodic server discovery
func (sd *ServerDiscovery) Start(ctx context.Context, node *model.Node) error {
	if sd.started {
		return fmt.Errorf("ServerDiscovery already started")
	}

	sd.ctx, sd.ctxCancel = context.WithCancel(ctx)
	sd.started = true

	log.Printf("Starting server-to-server discovery with interval: %s, max servers: %d",
		sd.updateInterval, sd.node.Config.Discovery.MaxServers)

	// Start periodic discovery
	sd.StartPeriodicDiscovery(sd.ctx)

	return nil
}

// Stop implements NodeHandler interface - stops periodic server discovery
func (sd *ServerDiscovery) Stop() error {
	if !sd.started {
		return nil
	}

	if sd.ctxCancel != nil {
		sd.ctxCancel()
	}
	sd.started = false

	log.Printf("Stopped server-to-server discovery")
	return nil
}

// StartPeriodicDiscovery starts background discovery of server nodes
func (sd *ServerDiscovery) StartPeriodicDiscovery(ctx context.Context) {
	ticker := time.NewTicker(sd.updateInterval)
	go func() {
		defer ticker.Stop()

		// Initial discovery
		if err := sd.refreshServerList(ctx); err != nil {
			log.Printf("Initial server discovery failed: %v", err)
		}

		for {
			select {
			case <-ticker.C:
				if err := sd.refreshServerList(ctx); err != nil {
					log.Printf("Periodic server discovery failed: %v", err)
				} else {
					connectedCount := sd.GetConnectedServerCount()
					log.Printf("Server discovery update: %d server connections active", connectedCount)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

// GetConnectedServerCount returns the number of currently connected server nodes
func (sd *ServerDiscovery) GetConnectedServerCount() int {
	count := 0
	for _, server := range sd.serverPeers {
		if server.ID != sd.node.Host.ID() && sd.node.Host.Network().Connectedness(server.ID) == 1 {
			count++
		}
	}
	return count
}

// isExitCapableServer checks if a peer supports relay protocol (indicating it's an exit-capable server)
func (sd *ServerDiscovery) isExitCapableServer(peerID peer.ID) bool {
	protocols, err := sd.node.Host.Peerstore().GetProtocols(peerID)
	if err != nil {
		log.Printf("Failed to get protocols for peer %s: %v", peerID, err)
		return false
	}

	log.Printf("DEBUG: Peer %s advertises protocols: %v", peerID, protocols)

	// Check if peer supports the relay protocol
	relayProtocolPrefix := "/goxic-proxy/relay/"
	for _, proto := range protocols {
		protoStr := string(proto)
		if len(protoStr) >= len(relayProtocolPrefix) && protoStr[:len(relayProtocolPrefix)] == relayProtocolPrefix {
			log.Printf("Peer %s supports relay protocol: %s", peerID, protoStr)
			return true
		}
	}

	log.Printf("Peer %s does not support relay protocol (client-only node)", peerID)
	return false
}

// isBlacklisted checks if a peer should be avoided due to recent failures
func (sd *ServerDiscovery) isBlacklisted(peerID peer.ID) bool {
	sd.failureMutex.RLock()
	defer sd.failureMutex.RUnlock()
	
	failureInfo, exists := sd.peerFailures[peerID]
	if !exists {
		return false
	}
	
	// Check if peer has exceeded max failures and is within blacklist time
	if failureInfo.FailureCount >= sd.maxFailures {
		timeSinceLastFailure := time.Since(failureInfo.LastFailure)
		if timeSinceLastFailure < sd.blacklistTime {
			log.Printf("Peer %s is blacklisted: %d failures, last failure %s ago",
				peerID, failureInfo.FailureCount, timeSinceLastFailure)
			return true
		}
		// Blacklist time has expired, reset failure count
		log.Printf("Blacklist expired for peer %s, resetting failure count", peerID)
		failureInfo.FailureCount = 0
	}
	
	return false
}

// recordConnectionFailure tracks a failed connection attempt
func (sd *ServerDiscovery) recordConnectionFailure(peerID peer.ID) {
	sd.failureMutex.Lock()
	defer sd.failureMutex.Unlock()
	
	failureInfo, exists := sd.peerFailures[peerID]
	if !exists {
		failureInfo = &PeerFailureInfo{PeerID: peerID}
		sd.peerFailures[peerID] = failureInfo
	}
	
	failureInfo.FailureCount++
	failureInfo.LastFailure = time.Now()
	
	log.Printf("Recorded connection failure for peer %s (total failures: %d)",
		peerID, failureInfo.FailureCount)
}

// recordConnectionSuccess tracks a successful connection
func (sd *ServerDiscovery) recordConnectionSuccess(peerID peer.ID) {
	sd.failureMutex.Lock()
	defer sd.failureMutex.Unlock()
	
	failureInfo, exists := sd.peerFailures[peerID]
	if !exists {
		failureInfo = &PeerFailureInfo{PeerID: peerID}
		sd.peerFailures[peerID] = failureInfo
	}
	
	failureInfo.FailureCount = 0 // Reset failures on successful connection
	failureInfo.LastSuccess = time.Now()
}

// refreshServerListForClient discovers server nodes from DHT without server-to-server filtering
// This is used by client nodes to find any available exit servers on-demand
func (sd *ServerDiscovery) refreshServerListForClient(ctx context.Context) error {
	// Create a context with timeout for discovery
	queryTimeout := time.Duration(sd.node.Config.Discovery.QueryTimeoutSec) * time.Second
	discoveryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	// Discover peers advertising the service
	peerChan, err := sd.routingDiscovery.FindPeers(discoveryCtx, sd.node.Config.Network.Name)
	if err != nil {
		return fmt.Errorf("failed to find peers: %w", err)
	}

	newServers := make([]peer.AddrInfo, 0)

	// Collect discovered peers (limit by config)
	maxServers := sd.node.Config.Discovery.MaxServers
	peerCount := 0

	for peerInfo := range peerChan {
		if peerInfo.ID == sd.node.Host.ID() {
			continue // Skip self
		}

		if peerCount >= maxServers {
			break // Stop collecting after reaching limit
		}

		// For client nodes, connect to all discovered peers and let protocol selection happen later
		connectedness := sd.node.Host.Network().Connectedness(peerInfo.ID)
		if connectedness == 0 { // Not connected
			connectTimeout := time.Duration(sd.node.Config.Network.ConnectionTimeout) * time.Second
			connectCtx, connectCancel := context.WithTimeout(ctx, connectTimeout)
			if err := sd.node.Host.Connect(connectCtx, peerInfo); err != nil {
				log.Printf("Client: Failed to connect to discovered peer %s: %v", peerInfo.ID, err)
				connectCancel()
				continue
			}
			connectCancel()
			log.Printf("Client: Connected to peer %s", peerInfo.ID)
		}

		newServers = append(newServers, peerInfo)
		peerCount++
	}

	sd.serverPeers = newServers
	sd.lastUpdate = time.Now()

	log.Printf("Client discovery complete: %d peers found", len(newServers))
	return nil
}
