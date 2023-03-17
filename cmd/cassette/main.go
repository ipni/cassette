package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/ipfs/go-log/v2"
	"github.com/ipni/cassette"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
)

var logger = log.Logger("cassette/cmd")

const libp2pUserAgent = "ipni/cassette"

func main() {
	libp2pIdentityPath := flag.String("libp2pIdentityPath", "", "The path to the marshalled libp2p host identity. If unspecified a random identity is generated.")
	libp2pListenAddrs := flag.String("libp2pListenAddrs", "", "The comma separated libp2p host listen multiaddrs. If unspecified the default listen multiaddrs are used at ephemeral port.")
	libp2pConMgrLow := flag.Int("libp2pConMgrLow", 160, "The low watermark of libp2p connection manager.")
	libp2pConMgrHigh := flag.Int("libp2pConMgrHigh", 192, "The high watermark of libp2p connection manager.")
	httpListenAddr := flag.String("httpListenAddr", "0.0.0.0:40080", "The cassette HTTP server listen address in address:port format.")
	httpResponsePreferJson := flag.Bool("httpResponsePreferJson", false, `Whether to prefer responding with JSON instead of NDJSON when Accept header is set to "*/*".`)
	useResourceManager := flag.Bool("useResourceManager", true, "Weather to use resource manager with built-in increased limits. When disabled Resource Manager is completely disabled.")
	ipniRequireQueryParam := flag.Bool("ipniRequireQueryParam", false, `Weather to require IPNI "cascade" query parameter with matching label in order to respond to HTTP lookup requests. Not required by default.`)
	ipniCascadeLabel := flag.String("ipniCascadeLabel", "legacy", "The IPNI cascade label associated to this instance.")
	logLevel := flag.String("logLevel", "info", "The logging level. Only applied if GOLOG_LOG_LEVEL environment variable is unset.")
	flag.Parse()

	if _, set := os.LookupEnv("GOLOG_LOG_LEVEL"); !set {
		_ = log.SetLogLevel("*", *logLevel)
		_ = log.SetLogLevel("net/identify", "error")
	}

	hOpts := []libp2p.Option{
		libp2p.UserAgent(libp2pUserAgent),
	}
	if *libp2pIdentityPath != "" {
		p := filepath.Clean(*libp2pIdentityPath)
		logger := logger.With("path", p)
		logger.Info("Unmarshalling libp2p host identity")
		mid, err := os.ReadFile(p)
		if err != nil {
			logger.Fatalw("Failed to read libp2p host identity file", "err", err)
		}
		id, err := crypto.UnmarshalPrivateKey(mid)
		if err != nil {
			logger.Fatalw("Failed to unmarshal libp2p host identity file", "err", err)
		}
		hOpts = append(hOpts, libp2p.Identity(id))
	}
	if *libp2pListenAddrs != "" {
		hOpts = append(hOpts, libp2p.ListenAddrStrings(strings.Split(*libp2pListenAddrs, ",")...))
	}
	if !*useResourceManager {
		hOpts = append(hOpts, libp2p.ResourceManager(&network.NullResourceManager{}))
	}
	cmngr, err := connmgr.NewConnManager(*libp2pConMgrLow, *libp2pConMgrHigh)
	if err != nil {
		logger.Fatalw("Failed to instantiate connection manager", "err", err)
	}
	hOpts = append(hOpts, libp2p.ConnectionManager(cmngr))
	h, err := libp2p.New(hOpts...)
	if err != nil {
		logger.Fatalw("Failed to instantiate libp2p host", "err", err)
	}

	c, err := cassette.New(
		cassette.WithHost(h),
		cassette.WithHttpListenAddr(*httpListenAddr),
		cassette.WithHttpResponsePreferJson(*httpResponsePreferJson),
		cassette.WithIpniRequireCascadeQueryParam(*ipniRequireQueryParam),
		cassette.WithIpniCascadeLabel(*ipniCascadeLabel),
	)
	if err != nil {
		logger.Fatalw("Failed to instantiate cassette", "err", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := c.Start(ctx); err != nil {
		logger.Fatalw("Failed to start cassette", "err", err)
	}
	sch := make(chan os.Signal, 1)
	signal.Notify(sch, os.Interrupt)

	<-sch
	cancel()
	logger.Info("Terminating...")
	if err := c.Shutdown(ctx); err != nil {
		logger.Warnw("Failure occurred while shutting down server.", "err", err)
	} else {
		logger.Info("Shut down server successfully.")
	}
}
