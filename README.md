# Goxic

A distributed proxy network built on libp2p that creates a decentralized mesh of proxy servers for better tolerance and flexibility.

## Overview

Goxic enables users to connect to proxy servers without direct contact, using distributed peer discovery through DHT (Distributed Hash Table) technology. The system creates a resilient network where traffic can be routed through multiple nodes.

## Architecture

The network consists of two types of nodes:

### Client Nodes
- Expose SOCKS5 proxy interfaces locally (default: 127.0.0.1:1080)
- Route traffic through the distributed network
- Automatically discover and connect to exit servers
- Support multiple routing strategies (direct, multi-hop)

### Server Nodes
- Handle internet egress traffic
- Act as relay nodes for indirect routing
- Form mesh networks with other servers
- Advertise availability through DHT service discovery

## Key Features

- **Decentralized Discovery**: No central authority required - nodes find each other through DHT
- **Multiple Transport Support**: TCP, QUIC with automatic NAT traversal
- **Load Balancing**: Multiple server selection strategies (random, load-based, preferred)
- **Traffic Encryption**: Ed25519 keys with Noise protocol transport encryption
- **Persistent Storage**: LevelDB for peer data and connection state

## Installation

### Prerequisites
- Go 1.19 or higher
- Git

### Build from Source
```bash
git clone https://github.com/Rorical/Goxic.git
cd Goxic
go build -o goxic
```

### Cross-Compilation
```bash
# Linux
GOOS=linux GOARCH=amd64 go build -o goxic-linux

# macOS
GOOS=darwin GOARCH=amd64 go build -o goxic-macos

# Windows
GOOS=windows GOARCH=amd64 go build -o goxic-windows.exe
```

## Configuration

Create a `config.json` file in your working directory:

```json
{
  "mode": "client",
  "dataDir": "./data",
  "network": {
    "name": "goxic-proxy",
    "secret": "your-shared-secret-here",
    "port": 4001,
    "bootstrapNodes": [
      "/ip4/bootstrap.example.com/tcp/4001/p2p/12D3KooW...",
      "/ip4/1.2.3.4/tcp/4001/p2p/12D3KooW..."
    ],
    "connectionTimeout": 30
  },
  "socks5": {
    "enabled": true,
    "bindAddress": "127.0.0.1",
    "port": 1080
  },
  "relay": {
    "enabled": true
  },
  "discovery": {
    "updateIntervalSec": 60,
    "advertiseIntervalSec": 60,
    "queryTimeoutSec": 10,
    "maxServers": 20,
    "selectionStrategy": "load_balance"
  }
}
```

### Configuration Parameters

#### Network Settings
- `name`: Network identifier for DHT service discovery
- `secret`: Shared authentication secret (must be same across all nodes)
- `port`: Network port for libp2p listeners
- `bootstrapNodes`: Initial peers to connect to the network
- `connectionTimeout`: Timeout for peer connections (seconds)

#### SOCKS5 Settings (Client Nodes)
- `enabled`: Enable local SOCKS5 proxy server
- `bindAddress`: Interface to bind the proxy server
- `port`: SOCKS5 proxy port

#### Discovery Settings
- `updateIntervalSec`: How often to refresh server list (client nodes)
- `advertiseIntervalSec`: How often to advertise availability (server nodes)
- `maxServers`: Maximum number of servers to track
- `selectionStrategy`: Server selection algorithm (`first`, `random`, `load_balance`, `preferred`)

## Usage

### Running a Client Node
```bash
./goxic -mode=client
```

Configure your applications to use SOCKS5 proxy: `127.0.0.1:1080`

### Running a Server Node
```bash
./goxic -mode=server
```

### Running a Bootstrap Node
```bash
./goxic -mode=bootstrap
```

### Command Line Options
- `-mode`: Node type (`client`, `server`, `bootstrap`)
- `-config`: Path to configuration file (default: `./config.json`)

## Network Protocol

### Service Discovery
- Uses configurable DHT protocol based on network `name`
- Server nodes advertise availability every 60 seconds (configurable)
- Client nodes discover servers on-demand

### Authentication
- Secret-based handshake during peer connection
- Ed25519 cryptographic keys for node identity
- Noise protocol for transport encryption

### Connection Management
- IPv4/IPv6 support on TCP and QUIC transports
- Automatic connection limits (100-400 concurrent connections)
- NAT traversal through libp2p's built-in mechanisms

## Development

### Project Structure
```
server/
├── handlers/          # Protocol handlers (SOCKS5, relay, discovery)
├── model/            # Data structures and configuration
├── node/             # Node initialization and management
└── utils/            # Utility functions (keys, p2p helpers)
```

### Building and Testing
```bash
# Build
go build

# Run tests
go test ./...

# Run with race detection
go test -race ./...

# Format code
go fmt ./...

# Static analysis
go vet ./...
```

### Key Components

#### Discovery System
- **ServerDiscovery**: Manages peer discovery and server selection
- **Client Mode**: On-demand discovery without persistent connections
- **Server Mode**: Maintains mesh connections to other exit servers

#### Protocol Handlers
- **SOCKS5Handler**: Local proxy server for client applications
- **RelayHandler**: Traffic routing between network nodes
- **Registry**: Manages protocol registration and stream handling

## Security Considerations

- Keep private keys secure (stored in `{dataDir}/priv.key`)
- Consider network isolation for sensitive deployments
- Monitor connection patterns for unusual activity

## License

This project is licensed under the MIT License - see the LICENSE file for details.