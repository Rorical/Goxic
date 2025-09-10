package model

import (
	"context"

	dbstore "github.com/ipfs/go-ds-leveldb"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
)

type Node struct {
	Host     host.Host
	Router   *dht.IpfsDHT
	CTX      context.Context
	Store    *dbstore.Datastore
	PubSub   *pubsub.PubSub
	Config   *Config
	Registry HandlerRegistry
}

// HandlerRegistry interface to avoid circular imports
type HandlerRegistry interface {
	StartNodeHandlers(ctx context.Context, node *Node) error
	StopNodeHandlers() error
	SetupStreamHandlers(node *Node)
	GetConnectionGuardInterface() interface{} // Return as interface{} to avoid circular import
}
