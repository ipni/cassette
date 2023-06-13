package internal

import (
	"os"
	"path/filepath"
	"time"

	"github.com/cenkalti/backoff/v3"
	"github.com/ipfs/go-log/v2"
	"github.com/ipni/cassette"
	"github.com/libp2p/go-libp2p"
	core_connmgr "github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	"github.com/mercari/go-circuitbreaker"
	"gopkg.in/yaml.v2"
)

var logger = log.Logger("cassette/cmd/config")

type Config struct {
	Libp2p *struct {
		IdentityPath *string   `yaml:"identityPath"`
		ListenAddrs  *[]string `yaml:"listenAddr"`
		UserAgent    *string   `yaml:"userAgent"`
		ConnManager  *struct {
			HighWater     *int           `yaml:"highWater"`
			LowWater      *int           `yaml:"lowWater"`
			GracePeriod   *time.Duration `yaml:"gracePeriod"`
			SilencePeriod *time.Duration `yaml:"silencePeriod"`
			EmergencyTrim *bool          `yaml:"emergencyTrim"`
		} `yaml:"connManager"`
		ResourceManager *struct {
			// TODO add resource manager config
		} `yaml:"resourceManager"`
	} `yaml:"libp2p"`
	Ipni *struct {
		HttpListenAddr           *string        `yaml:"httpListenAddr"`
		HttpAllowOrigin          *string        `yaml:"httpAllowOrigin"`
		PreferJsonResponse       *bool          `yaml:"preferJsonResponse"`
		CascadeLabel             *string        `yaml:"cascadeLabel"`
		RequireCascadeQueryParam *bool          `yaml:"requireCascadeQueryParam"`
		ResponseTimeout          *time.Duration `yaml:"responseTimeout"`
		FindByMultihash          *bool          `yaml:"findByMultihash"`
		DisableAddrFilter        *bool          `yaml:"disableAddrFilter"`
	} `yaml:"ipni"`
	Metrics *struct {
		ListenAddr       *string `yaml:"listenAddr"`
		EnablePprofDebug *bool   `yaml:"enablePprofDebug"`
	} `yaml:"metrics"`
	Bitswap *struct {
		Peers                      *[]string      `yaml:"peers"`
		MaxBroadcastBatchSize      *int           `yaml:"maxBroadcastBatchSize"`
		MaxBroadcastBatchWait      *time.Duration `yaml:"maxBroadcastBatchWait"`
		FallbackOnWantBlock        *bool          `yaml:"fallbackOnWantBlock"`
		RecipientsRefreshInterval  *time.Duration `yaml:"recipientsRefreshInterval"`
		BroadcastChannelBuffer     *int           `yaml:"broadcastChannelBuffer"`
		BroadcastSendChannelBuffer *int           `yaml:"sendChannelBuffer"`
		PeerDiscoveryInterval      *time.Duration `yaml:"peerDiscoveryInterval"`
		PeerDiscoveryAddrTTL       *time.Duration `yaml:"peerDiscoveryAddrTTL"`

		RecipientCBTripFunc *struct {
			Threshold   *int64 `yaml:"threshold"`
			FailureRate *struct {
				Min  *int64   `json:"min,omitempty"`
				Rate *float64 `json:"rate,omitempty"`
			} `yaml:"failureRate"`
			ConsecutiveFailures *int64 `yaml:"consecutiveFailures"`
		} `yaml:"recipientCBTripFunc"`
		RecipientCBCounterResetInterval  *time.Duration `yaml:"recipientCBCounterResetInterval"`
		RecipientCBFailOnContextCancel   *bool          `yaml:"recipientCBFailOnContextCancel"`
		RecipientCBFailOnContextDeadline *bool          `yaml:"recipientCBFailOnContextDeadline"`
		RecipientCBHalfOpenMaxSuccesses  *int64         `yaml:"recipientCBHalfOpenMaxSuccesses"`
		RecipientCBOpenTimeoutBackOff    *struct {
			Exponential *struct {
				// TODO: make min, max, base, jitter, etc. configurable.
			} `yaml:"exponential"`
			Constant *time.Duration `yaml:"constant"`
		} `yaml:"recipientCBOpenTimeoutBackOff"`
		RecipientCBOpenTimeout *time.Duration `yaml:"recipientCBOpenTimeout"`
		RecipientSendTimeout   *time.Duration `yaml:"recipientSendTimeout"`
		BroadcastCancelAfter   *time.Duration `yaml:"broadcastCancelAfter"`
	} `yaml:"bitswap"`
	Cache *struct {
		Size           *int           `yaml:"size"`
		Expiry         *time.Duration `yaml:"expiry"`
		CacheNoResults *bool          `yaml:"cacheNoResults"`
	} `yaml:"cache"`
}

func NewConfig(path string) (*Config, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var config Config
	if err := yaml.NewDecoder(f).Decode(&config); err != nil {
		return nil, err
	}
	return &config, nil
}

func (c *Config) ToOptions() ([]cassette.Option, error) {
	var opts []cassette.Option
	if c.Libp2p != nil {
		var hOpts []libp2p.Option
		userAgent := "ipni/cassette"
		if c.Libp2p.UserAgent != nil {
			userAgent = *c.Libp2p.UserAgent
		}
		libp2p.UserAgent(userAgent)
		if c.Libp2p.IdentityPath != nil {
			p := filepath.Clean(*c.Libp2p.IdentityPath)
			logger := logger.With("path", p)
			logger.Info("Unmarshalling libp2p host identity")
			mid, err := os.ReadFile(p)
			if err != nil {
				logger.Errorw("Failed to read libp2p host identity file", "err", err)
				return nil, err
			}
			id, err := crypto.UnmarshalPrivateKey(mid)
			if err != nil {
				logger.Errorw("Failed to unmarshal libp2p host identity file", "err", err)
				return nil, err
			}
			hOpts = append(hOpts, libp2p.Identity(id))
		}
		if c.Libp2p.ListenAddrs != nil {
			hOpts = append(hOpts, libp2p.ListenAddrStrings(*c.Libp2p.ListenAddrs...))
		}
		if c.Libp2p.ResourceManager == nil {
			hOpts = append(hOpts, libp2p.ResourceManager(&network.NullResourceManager{}))
		}
		var cm core_connmgr.ConnManager
		if c.Libp2p.ConnManager == nil {
			cm = core_connmgr.NullConnMgr{}
		} else {
			low, high := 0xff, 0xfff
			if c.Libp2p.ConnManager.LowWater != nil {
				low = *c.Libp2p.ConnManager.LowWater
			}
			if c.Libp2p.ConnManager.HighWater != nil {
				high = *c.Libp2p.ConnManager.HighWater
			}
			var cmOpts []connmgr.Option
			if c.Libp2p.ConnManager.GracePeriod != nil {
				cmOpts = append(cmOpts, connmgr.WithGracePeriod(*c.Libp2p.ConnManager.GracePeriod))
			}
			if c.Libp2p.ConnManager.SilencePeriod != nil {
				cmOpts = append(cmOpts, connmgr.WithSilencePeriod(*c.Libp2p.ConnManager.SilencePeriod))
			}
			if c.Libp2p.ConnManager.EmergencyTrim != nil {
				cmOpts = append(cmOpts, connmgr.WithEmergencyTrim(*c.Libp2p.ConnManager.EmergencyTrim))
			}
			var err error
			cm, err = connmgr.NewConnManager(low, high, cmOpts...)
			if err != nil {
				logger.Errorw("Failed to instantiate connection manager", "err", err)
				return nil, err
			}
		}
		hOpts = append(hOpts, libp2p.ConnectionManager(cm))
		h, err := libp2p.New(hOpts...)
		if err != nil {
			logger.Errorw("Failed to instantiate libp2p host", "err", err)
			return nil, err
		}
		opts = append(opts, cassette.WithHost(h))
	}
	if c.Ipni != nil {
		if c.Ipni.HttpListenAddr != nil {
			opts = append(opts, cassette.WithHttpListenAddr(*c.Ipni.HttpListenAddr))
		}
		if c.Ipni.HttpAllowOrigin != nil {
			opts = append(opts, cassette.WithHttpAllowOrigin(*c.Ipni.HttpAllowOrigin))
		}
		if c.Ipni.PreferJsonResponse != nil {
			opts = append(opts, cassette.WithHttpResponsePreferJson(*c.Ipni.PreferJsonResponse))
		}
		if c.Ipni.CascadeLabel != nil {
			opts = append(opts, cassette.WithIpniCascadeLabel(*c.Ipni.CascadeLabel))
		}
		if c.Ipni.RequireCascadeQueryParam != nil {
			opts = append(opts, cassette.WithIpniRequireCascadeQueryParam(*c.Ipni.RequireCascadeQueryParam))
		}
		if c.Ipni.ResponseTimeout != nil {
			opts = append(opts, cassette.WithResponseTimeout(*c.Ipni.ResponseTimeout))
		}
		if c.Ipni.FindByMultihash != nil {
			opts = append(opts, cassette.WithFindByMultihash(*c.Ipni.FindByMultihash))
		}
		if c.Ipni.DisableAddrFilter != nil {
			opts = append(opts, cassette.WithDisableAddrFilter(*c.Ipni.DisableAddrFilter))
		}
	}
	if c.Metrics != nil {
		if c.Metrics.ListenAddr != nil {
			opts = append(opts, cassette.WithMetricsListenAddr(*c.Metrics.ListenAddr))
		}
		if c.Metrics.EnablePprofDebug != nil {
			opts = append(opts, cassette.WithMetricsEnablePprofDebug(*c.Metrics.EnablePprofDebug))
		}
	}
	if c.Bitswap != nil {
		if c.Bitswap.Peers != nil {
			opts = append(opts, cassette.WithPeerStrings(*c.Bitswap.Peers...))
		}
		if c.Bitswap.MaxBroadcastBatchSize != nil {
			opts = append(opts, cassette.WithMaxBroadcastBatchSize(*c.Bitswap.MaxBroadcastBatchSize))
		}
		if c.Bitswap.MaxBroadcastBatchWait != nil {
			opts = append(opts, cassette.WithMaxBroadcastBatchWait(*c.Bitswap.MaxBroadcastBatchWait))
		}
		if c.Bitswap.FallbackOnWantBlock != nil {
			opts = append(opts, cassette.WithFallbackOnWantBlock(*c.Bitswap.FallbackOnWantBlock))
		}
		if c.Bitswap.RecipientsRefreshInterval != nil {
			opts = append(opts, cassette.WithRecipientsRefreshInterval(*c.Bitswap.RecipientsRefreshInterval))
		}
		if c.Bitswap.BroadcastSendChannelBuffer != nil {
			opts = append(opts, cassette.WithBroadcastSendChannelBuffer(*c.Bitswap.BroadcastSendChannelBuffer))
		}
		if c.Bitswap.BroadcastChannelBuffer != nil {
			opts = append(opts, cassette.WithBroadcastChannelBuffer(*c.Bitswap.BroadcastChannelBuffer))
		}
		if c.Bitswap.PeerDiscoveryInterval != nil {
			opts = append(opts, cassette.WithPeerDiscoveryInterval(*c.Bitswap.PeerDiscoveryInterval))
		}
		if c.Bitswap.PeerDiscoveryAddrTTL != nil {
			opts = append(opts, cassette.WithPeerDiscoveryAddrTTL(*c.Bitswap.PeerDiscoveryAddrTTL))
		}
		if c.Bitswap.RecipientCBTripFunc != nil {
			if c.Bitswap.RecipientCBTripFunc.Threshold != nil {
				opts = append(opts, cassette.WithRecipientCBTripFunc(
					circuitbreaker.NewTripFuncThreshold(*c.Bitswap.RecipientCBTripFunc.Threshold)))
			} else if c.Bitswap.RecipientCBTripFunc.ConsecutiveFailures != nil {
				opts = append(opts, cassette.WithRecipientCBTripFunc(
					circuitbreaker.NewTripFuncConsecutiveFailures(*c.Bitswap.RecipientCBTripFunc.ConsecutiveFailures)))
			} else if c.Bitswap.RecipientCBTripFunc.FailureRate != nil &&
				c.Bitswap.RecipientCBTripFunc.FailureRate.Min != nil &&
				c.Bitswap.RecipientCBTripFunc.FailureRate.Rate != nil {
				opts = append(opts, cassette.WithRecipientCBTripFunc(
					circuitbreaker.NewTripFuncFailureRate(
						*c.Bitswap.RecipientCBTripFunc.FailureRate.Min,
						*c.Bitswap.RecipientCBTripFunc.FailureRate.Rate,
					)))
			}
		}
		if c.Bitswap.RecipientCBCounterResetInterval != nil {
			opts = append(opts, cassette.WithRecipientCBCounterResetInterval(*c.Bitswap.RecipientCBCounterResetInterval))
		}
		if c.Bitswap.RecipientCBFailOnContextCancel != nil {
			opts = append(opts, cassette.WithRecipientCBFailOnContextCancel(*c.Bitswap.RecipientCBFailOnContextCancel))
		}
		if c.Bitswap.RecipientCBFailOnContextDeadline != nil {
			opts = append(opts, cassette.WithRecipientCBFailOnContextDeadline(*c.Bitswap.RecipientCBFailOnContextDeadline))
		}
		if c.Bitswap.RecipientCBHalfOpenMaxSuccesses != nil {
			opts = append(opts, cassette.WithRecipientCBHalfOpenMaxSuccesses(*c.Bitswap.RecipientCBHalfOpenMaxSuccesses))
		}
		if c.Bitswap.RecipientCBOpenTimeoutBackOff != nil {
			if c.Bitswap.RecipientCBOpenTimeoutBackOff.Exponential != nil {
				opts = append(opts, cassette.WithRecipientCBOpenTimeoutBackOff(backoff.NewExponentialBackOff()))
			} else if c.Bitswap.RecipientCBOpenTimeoutBackOff.Constant != nil {
				opts = append(opts, cassette.WithRecipientCBOpenTimeoutBackOff(
					backoff.NewConstantBackOff(*c.Bitswap.RecipientCBOpenTimeoutBackOff.Constant)),
				)
			}
		}
		if c.Bitswap.RecipientCBOpenTimeout != nil {
			opts = append(opts, cassette.WithRecipientCBOpenTimeout(*c.Bitswap.RecipientCBOpenTimeout))
		}
		if c.Bitswap.BroadcastCancelAfter != nil {
			opts = append(opts, cassette.WithBroadcastCancelAfter(*c.Bitswap.BroadcastCancelAfter))
		}
		if c.Bitswap.RecipientSendTimeout != nil {
			opts = append(opts, cassette.WithRecipientSendTimeout(*c.Bitswap.RecipientSendTimeout))
		}
	}
	if c.Cache != nil {
		if c.Cache.Expiry != nil {
			opts = append(opts, cassette.WithCacheExpiry(*c.Cache.Expiry))
		}
		if c.Cache.Size != nil {
			opts = append(opts, cassette.WithCacheSize(*c.Cache.Size))
		}
		if c.Cache.CacheNoResults != nil {
			opts = append(opts, cassette.WithCacheNoResults(*c.Cache.CacheNoResults))
		}
	}
	return opts, nil
}
