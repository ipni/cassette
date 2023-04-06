package cassette

import (
	"time"

	"github.com/cenkalti/backoff/v3"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/mercari/go-circuitbreaker"
)

var kuboBootstrapPeers = []string{
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
	"/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
	"/ip4/104.131.131.82/tcp/4001/p2p/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ",      // mars.i.ipfs.io
	"/ip4/104.131.131.82/udp/4001/quic/p2p/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ", // mars.i.ipfs.io
}

const (
	defaultConnMngrLowWaterMark  = 0xff
	defaultConnMngrHighWaterMark = 0xfff
)

type (
	Option  func(*options) error
	options struct {
		h                            host.Host
		httpListenAddr               string
		metricsHttpListenAddr        string
		metricsEnablePprofDebug      bool
		httpAllowOrigin              string
		httpResponsePreferJson       bool
		peers                        []peer.AddrInfo
		ipniCascadeLabel             string
		ipniRequireCascadeQueryParam bool
		responseTimeout              time.Duration
		findByMultihash              bool
		broadcastSendChannelBuffer   int
		recipientsRefreshInterval    time.Duration
		fallbackOnWantBlock          bool
		addrFilterDisabled           bool

		recipientCBTripFunc              circuitbreaker.TripFunc
		recipientCBCounterResetInterval  time.Duration
		recipientCBFailOnContextCancel   bool
		recipientCBFailOnContextDeadline bool
		recipientCBHalfOpenMaxSuccesses  int64
		recipientCBOpenTimeoutBackOff    backoff.BackOff
		recipientCBOpenTimeout           time.Duration

		maxBroadcastBatchSize int
		maxBroadcastBatchWait time.Duration

		peerDiscoveryHost     host.Host
		peerDiscoveryInterval time.Duration
		peerDiscoveryAddrTTL  time.Duration
	}
)

func newOptions(o ...Option) (*options, error) {
	opts := options{
		httpListenAddr:             "0.0.0.0:40080",
		metricsHttpListenAddr:      "0.0.0.0:40081",
		metricsEnablePprofDebug:    true,
		ipniCascadeLabel:           "legacy",
		httpAllowOrigin:            "*",
		responseTimeout:            5 * time.Second,
		broadcastSendChannelBuffer: 100,
		findByMultihash:            true,
		recipientsRefreshInterval:  10 * time.Second,
		fallbackOnWantBlock:        true,
		maxBroadcastBatchSize:      100,
		maxBroadcastBatchWait:      100 * time.Millisecond,
		peerDiscoveryInterval:      10 * time.Second,
		peerDiscoveryAddrTTL:       10 * time.Minute,

		recipientCBTripFunc:              circuitbreaker.NewTripFuncConsecutiveFailures(3),
		recipientCBCounterResetInterval:  2 * time.Second,
		recipientCBFailOnContextCancel:   false,
		recipientCBFailOnContextDeadline: true,
		recipientCBHalfOpenMaxSuccesses:  5,
		recipientCBOpenTimeoutBackOff:    circuitbreaker.DefaultOpenBackOff(),
		recipientCBOpenTimeout:           5 * time.Second,
	}
	for _, apply := range o {
		if err := apply(&opts); err != nil {
			return nil, err
		}
	}
	var err error
	if opts.h == nil {
		manager, err := connmgr.NewConnManager(defaultConnMngrLowWaterMark, defaultConnMngrHighWaterMark)
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
	}
	for _, p := range opts.peers {
		if len(p.Addrs) != 0 {
			opts.h.Peerstore().AddAddrs(p.ID, p.Addrs, peerstore.PermanentAddrTTL)
		}
	}
	if opts.peerDiscoveryHost == nil {
		// Use a separate host for peer discovery, because it uses DHT client to find addrs.
		// DHT client updates the peerstore with discovered peers as part of routing table
		// maintenance, and broadcaster broadcasts to all peers in the peerstore.
		// Therefore, separating the hosts to keep the subset of peers we broadcast to limited to:
		// 1. ones that are added in the peers list,
		// 2. ones the address for which is discovered via peer discovery, and
		// 3. ones that explicitly peer with the cassette host.
		opts.peerDiscoveryHost, err = libp2p.New()
		if err != nil {
			return nil, err
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

func WithMetricsListenAddr(a string) Option {
	return func(o *options) error {
		o.metricsHttpListenAddr = a
		return nil
	}
}

func WithPeerStrings(p ...string) Option {
	return func(o *options) error {
		for _, a := range p {
			ai, err := peer.AddrInfoFromString(a)
			if err != nil {
				return err
			}
			o.peers = append(o.peers, *ai)
		}
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

func WithDisableAddrFilter(b bool) Option {
	return func(o *options) error {
		o.addrFilterDisabled = b
		return nil
	}
}

func WithMetricsEnablePprofDebug(b bool) Option {
	return func(o *options) error {
		o.metricsEnablePprofDebug = b
		return nil
	}
}

func WithBroadcastSendChannelBuffer(b int) Option {
	return func(o *options) error {
		o.broadcastSendChannelBuffer = b
		return nil
	}
}

func WithFallbackOnWantBlock(b bool) Option {
	return func(o *options) error {
		o.fallbackOnWantBlock = b
		return nil
	}
}

func WithMaxBroadcastBatchSize(b int) Option {
	return func(o *options) error {
		o.maxBroadcastBatchSize = b
		return nil
	}
}

func WithMaxBroadcastBatchWait(d time.Duration) Option {
	return func(o *options) error {
		o.maxBroadcastBatchWait = d
		return nil
	}
}

func WithResponseTimeout(d time.Duration) Option {
	return func(o *options) error {
		o.responseTimeout = d
		return nil
	}
}

func WithRecipientsRefreshInterval(d time.Duration) Option {
	return func(o *options) error {
		o.recipientsRefreshInterval = d
		return nil
	}
}

func WithPeerDiscoveryHost(h host.Host) Option {
	return func(o *options) error {
		o.peerDiscoveryHost = h
		return nil
	}
}

func WithPeerDiscoveryInterval(d time.Duration) Option {
	return func(o *options) error {
		o.peerDiscoveryInterval = d
		return nil
	}
}

func WithPeerDiscoveryAddrTTL(d time.Duration) Option {
	return func(o *options) error {
		o.peerDiscoveryAddrTTL = d
		return nil
	}
}

func WithRecipientCBTripFunc(t circuitbreaker.TripFunc) Option {
	return func(o *options) error {
		o.recipientCBTripFunc = t
		return nil
	}
}

func WithRecipientCBCounterResetInterval(c time.Duration) Option {
	return func(o *options) error {
		o.recipientCBCounterResetInterval = c
		return nil
	}
}

func WithRecipientCBFailOnContextCancel(f bool) Option {
	return func(o *options) error {
		o.recipientCBFailOnContextCancel = f
		return nil
	}
}

func WithRecipientCBFailOnContextDeadline(f bool) Option {
	return func(o *options) error {
		o.recipientCBFailOnContextDeadline = f
		return nil
	}
}

func WithRecipientCBHalfOpenMaxSuccesses(h int64) Option {
	return func(o *options) error {
		o.recipientCBHalfOpenMaxSuccesses = h
		return nil
	}
}

func WithRecipientCBOpenTimeoutBackOff(b backoff.BackOff) Option {
	return func(o *options) error {
		o.recipientCBOpenTimeoutBackOff = b
		return nil
	}
}

func WithRecipientCBOpenTimeout(t time.Duration) Option {
	return func(o *options) error {
		o.recipientCBOpenTimeout = t
		return nil
	}
}
