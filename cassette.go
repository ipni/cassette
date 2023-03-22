package cassette

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-libipfs/bitswap/network"
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

	metrics *metrics
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
	c.metrics, err = newMetrics(&c)
	if err != nil {
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
	c.broadcaster = newBroadcaster(c)
	c.broadcaster.start(ctx)
	c.server.RegisterOnShutdown(c.cancel)
	go func() { _ = c.server.ListenAndServe() }()
	logger.Infow("Lookup server started", "id", c.h.ID(), "libp2pAddrs", c.h.Addrs(), "httpAddr", c.server.Addr, "protocols", c.h.Mux().Protocols())
	return nil
}

func (c *Cassette) Find(ctx context.Context, k cid.Cid) chan peer.AddrInfo {
	start := time.Now()
	c.metrics.notifyLookupRequested(ctx)
	var timeToFirstProvider time.Duration
	rch := make(chan peer.AddrInfo, 1)
	go func() {
		var resultCount atomic.Int64
		ctx, cancel := context.WithTimeout(ctx, c.responseTimeout)
		providersSoFar := make(map[peer.ID]struct{})
		unregister := c.r.registerFoundHook(ctx, k, func(id peer.ID) {
			if _, seen := providersSoFar[id]; seen {
				return
			}
			providersSoFar[id] = struct{}{}
			addrs := c.h.Peerstore().Addrs(id)
			if !c.addrFilterDisabled {
				addrs = multiaddr.FilterAddrs(addrs, IsPubliclyDialableAddr)
			}
			if len(addrs) > 0 {
				select {
				case <-ctx.Done():
					return
				case rch <- peer.AddrInfo{ID: id, Addrs: addrs}:
					if resultCount.Add(1) == 1 {
						timeToFirstProvider = time.Since(start)
					}
				}
			}
		})
		defer func() {
			cancel()
			unregister()
			close(rch)
		}()
		targets := c.toFindTargets(k)
		c.broadcaster.broadcastWant(targets)
		<-ctx.Done()
		c.metrics.notifyLookupResponded(context.Background(), resultCount.Load(), timeToFirstProvider, time.Since(start))
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
