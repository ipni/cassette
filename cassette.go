package cassette

import (
	"context"
	"net/http"
	"time"

	"github.com/ipfs/boxo/bitswap/network"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multihash"
)

var (
	logger = log.Logger("cassette")
)

type Cassette struct {
	*options
	server *http.Server
	// Context and cancellation used to terminate streaming responses on shutdown.
	ctx         context.Context
	cancel      context.CancelFunc
	bsn         network.BitSwapNetwork
	r           *receiver
	broadcaster *broadcaster
	discoverer  *peerDiscoverer

	metrics *metrics
	cache   *cache
}

func New(o ...Option) (*Cassette, error) {
	opts, err := newOptions(o...)
	if err != nil {
		return nil, err
	}
	c := Cassette{
		options: opts,
	}
	c.server = &http.Server{
		Addr:    opts.httpListenAddr,
		Handler: c.serveMux(),
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	if c.metrics, err = newMetrics(&c); err != nil {
		return nil, err
	}
	if c.cache, err = newCache(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Cassette) Start(ctx context.Context) error {
	if err := c.metrics.Start(ctx); err != nil {
		return err
	}
	c.bsn = network.NewFromIpfsHost(c.h, nil)
	var err error
	c.r, err = newReceiver(c)
	if err != nil {
		return err
	}
	c.bsn.Start(c.r)
	c.discoverer = newPeerDiscoverer(c)
	if err := c.discoverer.start(ctx); err != nil {
		return err
	}
	c.broadcaster = newBroadcaster(c)
	c.broadcaster.start(ctx)
	c.server.RegisterOnShutdown(c.cancel)
	go func() { _ = c.server.ListenAndServe() }()
	logger.Infow("Lookup server started", "id", c.h.ID(), "libp2pAddrs", c.h.Addrs(), "httpAddr", c.server.Addr, "protocols", c.h.Mux().Protocols())
	return nil
}

func (c *Cassette) Find(ctx context.Context, k cid.Cid) chan peer.AddrInfo {
	start := time.Now()
	c.metrics.notifyLookupRequested(context.Background())
	rch := make(chan peer.AddrInfo, 1)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, c.responseTimeout)
		defer func() {
			cancel()
			close(rch)
		}()

		var resultCount int64
		var timeToFirstProvider time.Duration
		var cacheHit bool
		defer func() {
			c.metrics.notifyLookupResponded(context.Background(), resultCount, timeToFirstProvider, time.Since(start), cacheHit)
		}()

		// Attempt to get provider IDs from cache
		if providers, found := c.cache.getProviders(k); found {
			cacheHit = true
			for _, provider := range providers {
				select {
				case <-ctx.Done():
					return
				case rch <- provider:
					resultCount++
					if resultCount == 1 {
						timeToFirstProvider = time.Since(start)
					}
				}
			}
			return
		}

		providers, unregister, err := c.r.registerFoundHook(ctx, k)
		if err != nil {
			return
		}
		defer unregister()

		targets := c.toFindTargets(k)
		if err := c.broadcaster.broadcastWant(ctx, targets); err != nil {
			return
		}

		providersSoFar := make(map[peer.ID]struct{})
		returnIfUnseen := func(provider peer.ID) *peer.AddrInfo {
			select {
			case <-ctx.Done():
				return nil
			default:
				if _, seen := providersSoFar[provider]; seen {
					return nil
				}
				providersSoFar[provider] = struct{}{}
				addrs := c.h.Peerstore().Addrs(provider)
				if !c.addrFilterDisabled {
					addrs = multiaddr.FilterAddrs(addrs, IsPubliclyDialableAddr)
				}
				if len(addrs) == 0 {
					return nil
				}
				result := peer.AddrInfo{ID: provider, Addrs: addrs}
				select {
				case <-ctx.Done():
					return nil
				case rch <- result:
					resultCount++
					if resultCount == 1 {
						timeToFirstProvider = time.Since(start)
					}
					return &result
				}
			}
		}

		var returnedProviders []peer.AddrInfo
		defer func() {
			if len(returnedProviders) > 0 || c.cacheNoResults {
				c.cache.putProviders(k, returnedProviders)
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case provider, ok := <-providers:
				if !ok {
					return
				}
				if returned := returnIfUnseen(provider); returned != nil {
					returnedProviders = append(returnedProviders, *returned)
				}
			}
		}
		// TODO add option to stop based on provider count limit
	}()
	return rch
}

func (c *Cassette) toFindTargets(k cid.Cid) []cid.Cid {
	// TODO add option for codecs other than cid.Raw
	switch {
	case cid.Undef.Equals(k):
		return nil
	case !c.findByMultihash:
		return []cid.Cid{k}
	case k.Prefix().Version == 0:
		return []cid.Cid{k, cid.NewCidV1(cid.Raw, k.Hash())}
	default:
		hash := k.Hash()
		prefix := k.Prefix()
		targets := make([]cid.Cid, 0, 3)
		targets = append(targets, k)
		// Strictly check multihash code and length; otherwise, cid.NewCidV0 will panic.
		if prefix.MhType == multihash.SHA2_256 && prefix.MhLength == 32 {
			targets = append(targets, cid.NewCidV0(hash))
		}
		if prefix.Codec != cid.Raw {
			targets = append(targets, cid.NewCidV1(cid.Raw, hash))
		}
		return targets
	}
}

func (c *Cassette) Shutdown(ctx context.Context) error {
	c.bsn.Stop()
	_ = c.metrics.Shutdown(ctx)
	return c.server.Shutdown(ctx)
}
