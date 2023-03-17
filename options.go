package cassette

import (
	"time"

	bsnet "github.com/ipfs/go-libipfs/bitswap/network"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
)

var kuboBootstrapPeers = []string{
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
	"/ip4/104.131.131.82/tcp/4001/p2p/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ",      // mars.i.ipfs.io
	"/ip4/104.131.131.82/udp/4001/quic/p2p/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ", // mars.i.ipfs.io
}

type (
	Option  func(*options) error
	options struct {
		h                            host.Host
		httpListenAddr               string
		httpAllowOrigin              string
		httpResponsePreferJson       bool
		peers                        []peer.AddrInfo
		ipniCascadeLabel             string
		ipniRequireCascadeQueryParam bool
		maxWaitTimeout               time.Duration
		findByMultihash              bool
		messageSenderBuffer          int
		messageSenderOpts            bsnet.MessageSenderOpts
		recipientsRefreshInterval    time.Duration
	}
)

func newOptions(o ...Option) (*options, error) {
	opts := options{
		httpListenAddr:      "0.0.0.0:40080",
		ipniCascadeLabel:    "legacy",
		httpAllowOrigin:     "*",
		maxWaitTimeout:      5 * time.Second,
		messageSenderBuffer: 100,
		findByMultihash:     true,
		messageSenderOpts: bsnet.MessageSenderOpts{
			MaxRetries:       3,
			SendTimeout:      10 * time.Second,
			SendErrorBackoff: 100 * time.Millisecond,
		},
		recipientsRefreshInterval: 10 * time.Second,
	}
	for _, apply := range o {
		if err := apply(&opts); err != nil {
			return nil, err
		}
	}

	// var err error
	if opts.h == nil {
		manager, err := connmgr.NewConnManager(500, 5000)
		if err != nil {
			return nil, err
		}
		opts.h, err = libp2p.New(
			libp2p.ResourceManager(&network.NullResourceManager{}),
			libp2p.ConnectionManager(manager),
		)
		if err != nil {
			return nil, err
		}
	}
	if len(opts.peers) == 0 {
		for _, a := range kuboBootstrapPeers {
			ai, err := peer.AddrInfoFromString(a)
			if err != nil {
				return nil, err
			}
			opts.peers = append(opts.peers, *ai)
		}
		for _, p := range opts.peers {
			opts.h.Peerstore().AddAddrs(p.ID, p.Addrs, peerstore.PermanentAddrTTL)
		}
	}
	return &opts, nil
}

func WithHost(h host.Host) Option {
	return func(o *options) error {
		o.h = h
		return nil
	}
}

func WithHttpListenAddr(a string) Option {
	return func(o *options) error {
		o.httpListenAddr = a
		return nil
	}
}

func WithPeers(p ...peer.AddrInfo) Option {
	return func(o *options) error {
		o.peers = p
		return nil
	}
}

func WithIpniCascadeLabel(l string) Option {
	return func(o *options) error {
		o.ipniCascadeLabel = l
		return nil
	}
}

func WithHttpAllowOrigin(ao string) Option {
	return func(o *options) error {
		o.httpAllowOrigin = ao
		return nil
	}
}

// WithIpniRequireCascadeQueryParam sets whether the server should require IPNI cascade query
// parameter with the matching label in order to respond to HTTP lookup requests.
// See: WithIpniCascadeLabel
func WithIpniRequireCascadeQueryParam(p bool) Option {
	return func(o *options) error {
		o.ipniRequireCascadeQueryParam = p
		return nil
	}
}

// WithHttpResponsePreferJson sets whether to prefer non-streaming json response over streaming
// ndjosn when the Accept header uses `*/*` wildcard. By default, in such case ndjson streaming
// response is preferred.
func WithHttpResponsePreferJson(b bool) Option {
	return func(o *options) error {
		o.httpResponsePreferJson = b
		return nil
	}
}

func WithFindByMultihash(b bool) Option {
	return func(o *options) error {
		o.findByMultihash = b
		return nil
	}
}

func WithMessageSenderBuffer(b int) Option {
	return func(o *options) error {
		o.messageSenderBuffer = b
		return nil
	}
}

func WithMessageSenderOpts(opts bsnet.MessageSenderOpts) Option {
	return func(o *options) error {
		o.messageSenderOpts = opts
		return nil
	}
}

func WithMaxWaitTimeout(d time.Duration) Option {
	return func(o *options) error {
		o.maxWaitTimeout = d
		return nil
	}
}

func WithRecipientsRefreshInterval(d time.Duration) Option {
	return func(o *options) error {
		o.recipientsRefreshInterval = d
		return nil
	}
}
