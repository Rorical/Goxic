package node

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Rorical/Goxic/server/model"
	"github.com/Rorical/Goxic/server/utils"
	dbstore "github.com/ipfs/go-ds-leveldb"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/metrics"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/routing"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	"github.com/libp2p/go-libp2p/p2p/host/peerstore/pstoreds"
	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
)

func Run(ctx context.Context, config *model.Config) (*model.Node, error) {
	privKey, err := utils.FetchPrivKey(config)
	if err != nil {
		return nil, err
	}

	connMgr, err := connmgr.NewConnManager(
		config.Network.MinConnections,
		config.Network.MaxConnections,
	)

	store, err := dbstore.NewDatastore(config.GetDataPath("p2p"), nil)

	if err != nil {

		return nil, err
	}

	peerStore, err := pstoreds.NewPeerstore(ctx, store, pstoreds.Options{
		CacheSize:    100,
		MaxProtocols: 5,
	})
	if err != nil {
		return nil, err
	}

	bwReport := metrics.NewBandwidthCounter()

	var idht *dht.IpfsDHT

	bootNodesAddrs := utils.Str2Peers(config.Network.BoostrapNodes)

	// Build listen addresses based on config
	listenAddrs := make([]string, 0, 4)
	listenAddrs = append(listenAddrs, fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", config.Network.Port))
	listenAddrs = append(listenAddrs, fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", config.Network.Port))

	if config.Network.EnableIPv6 {
		listenAddrs = append(listenAddrs, fmt.Sprintf("/ip6/::/tcp/%d", config.Network.Port))
		listenAddrs = append(listenAddrs, fmt.Sprintf("/ip6/::/udp/%d/quic-v1", config.Network.Port))
	}

	libp2pOpts := []libp2p.Option{
		libp2p.Peerstore(peerStore),
		libp2p.ListenAddrStrings(listenAddrs...),
		libp2p.Security(noise.ID, noise.New),
		libp2p.Identity(privKey),
		libp2p.DefaultTransports,
		libp2p.Muxer(yamux.ID, yamux.DefaultTransport),
		libp2p.ConnectionManager(connMgr),
		libp2p.DefaultResourceManager,
		libp2p.Ping(false),
		libp2p.BandwidthReporter(bwReport),
		libp2p.Routing(func(h host.Host) (routing.PeerRouting, error) {
			idht, err = dht.New(ctx, h,
				dht.Mode(dht.ModeServer),
				dht.BootstrapPeers(bootNodesAddrs...),
				dht.Datastore(store),
				dht.ProtocolPrefix(protocol.ID("/"+config.Network.Name)),
				dht.BucketSize(8),
			)
			if err != nil {
				panic(err)
			}
			return idht, err
		}),
		libp2p.WithDialTimeout(time.Duration(config.Network.DialTimeout) * time.Second),
	}

	// Auto-relay configuration will be added after DHT is created

	if config.Network.EnableHolePunching {
		libp2pOpts = append(libp2pOpts, libp2p.EnableHolePunching())
	} else {
		libp2pOpts = append(libp2pOpts, libp2p.EnableRelay())
	}

	h, err := libp2p.New(libp2pOpts...)
	if err != nil {
		return nil, err
	}

	// print the node's listening addresses
	log.Println("Listen addresses:", h.Addrs())

	// print the node's ID
	log.Println("Node ID:", h.ID())

	// print the node's public key
	log.Println("Public Key:", h.Peerstore().PubKey(h.ID()))

	// Configure auto-relay with DHT-based peer source if enabled
	if config.Network.EnableAutoRelay {
		log.Println("Configuring auto-relay with DHT peer source...")

		// Create a peer source function that uses DHT to find relay nodes
		peerSource := func(ctx context.Context, num int) <-chan peer.AddrInfo {
			ch := make(chan peer.AddrInfo, num)
			go func() {
				defer close(ch)

				// Use DHT to find peers that support relay protocol
				peerChan, err := drouting.NewRoutingDiscovery(idht).FindPeers(ctx, "libp2p-relay")
				if err != nil {
					log.Printf("Failed to discover relay peers: %v", err)
					return
				}

				count := 0
				for peer := range peerChan {
					if count >= num {
						break
					}

					// Only include peers that are not ourselves
					if peer.ID != h.ID() {
						select {
						case ch <- peer:
							count++
						case <-ctx.Done():
							return
						}
					}
				}
			}()
			return ch
		}

		// Enable auto-relay with the peer source and minimum interval
		_, err = autorelay.NewAutoRelay(h,
			autorelay.WithPeerSource(peerSource),
			autorelay.WithMinInterval(1*time.Minute),
		)
		if err != nil {
			log.Printf("Failed to enable auto-relay: %v", err)
		} else {
			log.Println("Auto-relay enabled with DHT peer source")
		}
	}

	idSer, err := identify.NewIDService(h, identify.WithMetricsTracer(identify.NewMetricsTracer()))
	if err != nil {
		return nil, err
	}
	idSer.Start()

	var wg sync.WaitGroup
	for _, addr := range bootNodesAddrs {
		wg.Add(1)
		go func(addr peer.AddrInfo) {
			defer wg.Done()
			if err = h.Connect(ctx, addr); err != nil {
				fmt.Println(err)
			}
			if _, err = h.Network().DialPeer(ctx, addr.ID); err != nil {
				fmt.Println(err)
			}
			h.Peerstore().AddAddrs(addr.ID, addr.Addrs, peerstore.PermanentAddrTTL)
			if _, err = idht.RoutingTable().TryAddPeer(addr.ID, true, false); err != nil {
				fmt.Println(err)
			}
		}(addr)
	}
	wg.Wait()

	node := &model.Node{
		Host:   h,
		Router: idht,
		CTX:    ctx,
		Store:  store,
		Config: config,
	}

	routingDiscovery := drouting.NewRoutingDiscovery(node.Router)

	// Advertisement goroutine - server nodes advertise their availability
	go func(serviceName string) {
		ticker := time.NewTicker(time.Duration(config.Discovery.AdvertiseIntervalSec) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				dutil.Advertise(node.CTX, routingDiscovery, serviceName)
				break
			case <-ctx.Done():
				return
			}
		}
	}(config.Network.Name)

	err = idht.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}

	ps, err := pubsub.NewGossipSub(ctx, h,
		pubsub.WithDiscovery(drouting.NewRoutingDiscovery(idht)),
		pubsub.WithFloodPublish(true),
		pubsub.WithMessageSignaturePolicy(pubsub.StrictSign),
		pubsub.WithPeerExchange(true),
	)

	if err != nil {
		return nil, err
	}
	node.PubSub = ps

	return node, nil
}
