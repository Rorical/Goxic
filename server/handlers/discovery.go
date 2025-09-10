package handlers

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sort"
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

// ServerDiscovery manages discovery of available server nodes
type ServerDiscovery struct {
	node              *model.Node
	routingDiscovery  *drouting.RoutingDiscovery
	serverPeers       []peer.AddrInfo
	lastUpdate        time.Time
	updateInterval    time.Duration
	capabilityManager *CapabilityManager
	// Load balancing
	serverLoads map[peer.ID]*ServerLoadInfo
	loadMutex   sync.RWMutex
}

// NewServerDiscovery creates a new server discovery instance
func NewServerDiscovery(node *model.Node, capabilityManager *CapabilityManager) *ServerDiscovery {
	updateInterval := time.Duration(node.Config.Discovery.UpdateIntervalSec) * time.Second
	return &ServerDiscovery{
		node:              node,
		routingDiscovery:  drouting.NewRoutingDiscovery(node.Router),
		serverPeers:       make([]peer.AddrInfo, 0),
		updateInterval:    updateInterval,
		capabilityManager: capabilityManager,
		serverLoads:       make(map[peer.ID]*ServerLoadInfo),
		loadMutex:         sync.RWMutex{},
	}
}

// GetAvailableServers returns list of available server nodes
func (sd *ServerDiscovery) GetAvailableServers(ctx context.Context) ([]peer.AddrInfo, error) {
	// Refresh if cache is stale
	if time.Since(sd.lastUpdate) > sd.updateInterval {
		if err := sd.refreshServerList(ctx); err != nil {
			log.Printf("Failed to refresh server list: %v", err)
			// Return cached list even if refresh failed
		}
	}

	// Filter out disconnected peers and non-server nodes
	availableServers := make([]peer.AddrInfo, 0)
	for _, server := range sd.serverPeers {
		// Skip self
		if server.ID == sd.node.Host.ID() {
			continue
		}

		// Check if we're still connected
		if sd.node.Host.Network().Connectedness(server.ID) != 1 { // Not connected
			continue
		}

		// Check if peer has exit node capability
		hasExitCapability := false
		if sd.capabilityManager != nil {
			capabilities, err := sd.capabilityManager.GetPeerCapabilities(server.ID)
			if err == nil && capabilities != nil {
				// We have explicit capability information
				hasExitCapability = sd.capabilityManager.HasCapability(server.ID, CapabilityExitNode)
				log.Printf("Server %s has known capabilities: %v (exit_node: %t)",
					server.ID, capabilities.Capabilities, hasExitCapability)
			} else {
				// No capability information available, try to get it
				log.Printf("Server %s capabilities unknown, attempting capability exchange", server.ID)

				// Try capability query for newly discovered peers
				go func(peerID peer.ID) {
					if caps, err := sd.capabilityManager.queryPeerCapabilities(peerID); err == nil && caps != nil {
						log.Printf("Successfully retrieved capabilities for %s: %v", peerID, caps.Capabilities)
					}
				}(server.ID)

				// For newly discovered peers, be optimistic but mark as uncertain
				log.Printf("Server %s capabilities unknown, temporarily allowing for capability discovery", server.ID)
				hasExitCapability = true // Temporarily allow - will be refined after capability exchange
			}
		} else {
			// No capability manager, assume all connected peers could be servers
			hasExitCapability = true
		}

		if !hasExitCapability {
			continue
		}

		availableServers = append(availableServers, server)
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
	sd.loadMutex.RLock()
	defer sd.loadMutex.RUnlock()

	var bestServer *peer.AddrInfo
	bestLoad := int(^uint(0) >> 1) // Max int

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

	for peerInfo := range peerChan {
		if peerInfo.ID == sd.node.Host.ID() {
			continue // Skip self
		}

		if peerCount >= maxServers {
			break // Stop collecting after reaching limit
		}

		// Try to connect if not already connected
		if sd.node.Host.Network().Connectedness(peerInfo.ID) == 0 { // Not connected
			connectTimeout := time.Duration(sd.node.Config.Network.ConnectionTimeout) * time.Second
			connectCtx, connectCancel := context.WithTimeout(ctx, connectTimeout)
			if err := sd.node.Host.Connect(connectCtx, peerInfo); err != nil {
				log.Printf("Failed to connect to discovered peer %s: %v", peerInfo.ID, err)
				connectCancel()
				continue
			}
			connectCancel()
		}

		newServers = append(newServers, peerInfo)
		log.Printf("Discovered server node: %s", peerInfo.ID)
		peerCount++
	}

	sd.serverPeers = newServers
	sd.lastUpdate = time.Now()

	log.Printf("Discovered %d server nodes", len(newServers))
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
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}
