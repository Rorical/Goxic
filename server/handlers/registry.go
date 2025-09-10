package handlers

import (
	"context"
	"log"
	"regexp"

	"github.com/Rorical/Goxic/server/model"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// Handler interface for different types of handlers
type Handler interface {
	// Protocol returns the protocol ID pattern this handler supports
	Protocol() string
	// Handle processes the incoming stream
	Handle(stream network.Stream, node *model.Node) error
}

// NodeHandler interface for node-level handlers (like SOCKS5 server)
type NodeHandler interface {
	// Start initializes and starts the handler
	Start(ctx context.Context, node *model.Node) error
	// Stop gracefully stops the handler
	Stop() error
}

// Registry manages protocol handlers and node handlers
type Registry struct {
	streamHandlers map[string]Handler
	nodeHandlers   []NodeHandler
	patterns       map[string]*regexp.Regexp
}

// NewRegistry creates a new handler registry
func NewRegistry() *Registry {
	return &Registry{
		streamHandlers: make(map[string]Handler),
		nodeHandlers:   make([]NodeHandler, 0),
		patterns:       make(map[string]*regexp.Regexp),
	}
}

// RegisterStreamHandler registers a libp2p stream handler
func (r *Registry) RegisterStreamHandler(handler Handler) error {
	protocolPattern := handler.Protocol()
	r.streamHandlers[protocolPattern] = handler

	// Compile regex pattern for protocol matching
	pattern, err := regexp.Compile(protocolPattern)
	if err != nil {
		return err
	}
	r.patterns[protocolPattern] = pattern

	return nil
}

// RegisterNodeHandler registers a node-level handler
func (r *Registry) RegisterNodeHandler(handler NodeHandler) {
	r.nodeHandlers = append(r.nodeHandlers, handler)
}

// GetStreamHandler returns the appropriate handler for a protocol
func (r *Registry) GetStreamHandler(protocolID protocol.ID) (Handler, bool) {
	protocolStr := string(protocolID)

	// Try exact match first
	if handler, exists := r.streamHandlers[protocolStr]; exists {
		return handler, true
	}

	// Try pattern matching
	for pattern, regex := range r.patterns {
		if regex.MatchString(protocolStr) {
			if handler, exists := r.streamHandlers[pattern]; exists {
				return handler, true
			}
		}
	}

	return nil, false
}

// StartNodeHandlers starts all registered node handlers
func (r *Registry) StartNodeHandlers(ctx context.Context, node *model.Node) error {
	for _, handler := range r.nodeHandlers {
		if err := handler.Start(ctx, node); err != nil {
			return err
		}
	}
	return nil
}

// StopNodeHandlers stops all registered node handlers
func (r *Registry) StopNodeHandlers() error {
	for _, handler := range r.nodeHandlers {
		if err := handler.Stop(); err != nil {
			return err
		}
	}
	return nil
}

// SetupStreamHandlers registers all stream handlers with the libp2p host
func (r *Registry) SetupStreamHandlers(node *model.Node) {
	// Register each protocol with libp2p directly
	for protocolPattern, handler := range r.streamHandlers {
		streamHandler := func(stream network.Stream) {
			go func() {
				defer stream.Close()
				if err := handler.Handle(stream, node); err != nil {
					stream.Reset()
				}
			}()
		}

		// Check if this is a relay handler that needs pattern matching
		if relayHandler, ok := handler.(*RelayHandler); ok {
			// Use SetStreamHandlerMatch for pattern-based protocols
			matchFunc := relayHandler.ProtocolMatch()
			node.Host.SetStreamHandlerMatch(protocol.ID(protocolPattern), matchFunc, streamHandler)
			log.Printf("Registered relay protocol with pattern matching: %s", protocolPattern)
		} else {
			// For exact protocols, register directly
			node.Host.SetStreamHandler(protocol.ID(protocolPattern), streamHandler)
			log.Printf("Registered exact protocol: %s", protocolPattern)
		}
	}
}
