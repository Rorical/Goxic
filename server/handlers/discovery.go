package handlers

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/Rorical/Goxic/server/model"
	"github.com/libp2p/go-libp2p/core/peer"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
)

// ServerDiscovery manages discovery of available server nodes
type ServerDiscovery struct {
	node              *model.Node
	routingDiscovery  *drouting.RoutingDiscovery
	serverPeers       []peer.AddrInfo
	lastUpdate        time.Time
	updateInterval    time.Duration
	capabilityManager *CapabilityManager
	connectionGuard   *ConnectionGuard
}

// NewServerDiscovery creates a new server discovery instance
func NewServerDiscovery(node *model.Node, capabilityManager *CapabilityManager, connectionGuard *ConnectionGuard) *ServerDiscovery {
	updateInterval := time.Duration(node.Config.Discovery.UpdateIntervalSec) * time.Second
	return &ServerDiscovery{
		node:              node,
		routingDiscovery:  drouting.NewRoutingDiscovery(node.Router),
		serverPeers:       make([]peer.AddrInfo, 0),
		updateInterval:    updateInterval,
		capabilityManager: capabilityManager,
		connectionGuard:   connectionGuard,
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
		if sd.capabilityManager != nil && !sd.capabilityManager.HasCapability(server.ID, CapabilityExitNode) {
			continue
		}

		// Check if peer is authenticated (if authentication is enabled)
		if sd.connectionGuard != nil && !sd.connectionGuard.IsAuthenticated(server.ID) {
			continue
		}

		availableServers = append(availableServers, server)
	}

	return availableServers, nil
}

// SelectServer selects a server from available servers
func (sd *ServerDiscovery) SelectServer(ctx context.Context) (*peer.AddrInfo, error) {
	servers, err := sd.GetAvailableServers(ctx)
	if err != nil {
		return nil, err
	}

	if len(servers) == 0 {
		return nil, fmt.Errorf("no available server nodes found")
	}

	// Select based on configuration
	if sd.node.Config.Discovery.EnableRandomSelection {
		// Random selection
		index := rand.Intn(len(servers))
		return &servers[index], nil
	} else {
		// First available server (deterministic)
		return &servers[0], nil
	}
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
