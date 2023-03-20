package cassette

import (
	"context"
	"net"
	"net/http"

	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-libipfs/bitswap/network"
	"github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multihash"
)

var (
	logger = log.Logger("cassette")
)

type Cassette struct {
	*options
	s *http.Server
	// Context and cancellation used to terminate streaming responses on shutdown.
	ctx         context.Context
	cancel      context.CancelFunc
	bsn         network.BitSwapNetwork
	r           *receiver
	broadcaster *broadcaster
}

func New(o ...Option) (*Cassette, error) {
	opts, err := newOptions(o...)
	if err != nil {
		return nil, err
	}
	c := Cassette{
		options: opts,
	}
	c.s = &http.Server{
		Addr:    opts.httpListenAddr,
		Handler: c.serveMux(),
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.s.RegisterOnShutdown(c.cancel)
	return &c, nil
}

func (c *Cassette) Start(ctx context.Context) error {
	c.bsn = network.NewFromIpfsHost(c.h, nil)
	var err error
	c.r, err = newReceiver(c.findByMultihash)
	if err != nil {
		return err
	}
	c.bsn.Start(c.r)
	c.broadcaster = newBroadcaster(c)
	c.broadcaster.start(ctx)
	ln, err := net.Listen("tcp", c.s.Addr)
	if err != nil {
		return err
	}
	go func() { _ = c.s.Serve(ln) }()
	logger.Infow("Server started", "id", c.h.ID(), "libp2pAddrs", c.h.Addrs(), "httpAddr", ln.Addr(), "protocols", c.h.Mux().Protocols())
	return nil
}

func (c *Cassette) Find(ctx context.Context, k cid.Cid) chan peer.AddrInfo {
	rch := make(chan peer.AddrInfo, 1)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, c.maxWaitTimeout)
		unregister := c.r.registerFoundHook(ctx, k, func(id peer.ID) {
			addrs := c.h.Peerstore().Addrs(id)
			if len(addrs) > 0 {
				select {
				case <-ctx.Done():
					return
				case rch <- peer.AddrInfo{ID: id, Addrs: addrs}:
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
	return c.s.Shutdown(ctx)
}
