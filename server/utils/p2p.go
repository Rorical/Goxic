package utils

import "github.com/libp2p/go-libp2p/core/peer"

func Str2Peers(peers []string) []peer.AddrInfo {
	addrs := make([]peer.AddrInfo, len(peers))
	var temp *peer.AddrInfo
	var err error
	for i, pstring := range peers {
		temp, err = peer.AddrInfoFromString(pstring)
		if err != nil {
			panic(err)
		}
		addrs[i] = *temp
	}
	return addrs
}
