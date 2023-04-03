package cassette

import (
	"context"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/multiformats/go-multiaddr"
	"time"
)

type peerDiscoverer struct {
	discoveryTicker *time.Ticker
	c               *Cassette
}

func newPeerDiscoverer(c *Cassette) *peerDiscoverer {
	return &peerDiscoverer{
		c:               c,
		discoveryTicker: time.NewTicker(c.peerDiscoveryInterval),
	}
}

func (pd *peerDiscoverer) start(ctx context.Context) error {
	peerRouter, err := dht.New(context.Background(), pd.c.peerDiscoveryHost,
		dht.Mode(dht.ModeClient),
		dht.BootstrapPeers(dht.GetDefaultBootstrapPeerAddrInfos()...),
	)
	if err != nil {
		return err
	}
	go func() {
		discover := func() {
			var count int
			for _, p := range pd.c.peers {
				if len(p.Addrs) == 0 {
					count++
					pid := p.ID
					found, err := peerRouter.FindPeer(ctx, pid)
					if err != nil {
						logger.Errorw("Failed to discover addrs for peer", "peer", pid, "err", err)
						continue
					}
					addrs := multiaddr.FilterAddrs(found.Addrs, IsPubliclyDialableAddr)
					if len(addrs) == 0 {
						logger.Errorw("No publicly dialable addrs found for peer", "peer", pid, "addrs", found.Addrs)
						continue
					}
					prev := pd.c.h.Peerstore().Addrs(pid)
					pd.c.h.Peerstore().ClearAddrs(pid)
					pd.c.h.Peerstore().SetAddrs(pid, addrs, pd.c.peerDiscoveryAddrTTL)
					logger.Infow("Discovered addrs for peer", "peer", pid, "addrs", addrs, "previous", prev)
				}
			}
			logger.Infow("Finished discovery cycle for peers with no addrs", "count", count)
		}
		discover()
		for {
			select {
			case <-ctx.Done():
				logger.Info("Stopping peer discovery")
				_ = peerRouter.Close()
				pd.discoveryTicker.Stop()
				return
			case <-pd.discoveryTicker.C:
				discover()
			}
		}
	}()
	return nil
}
