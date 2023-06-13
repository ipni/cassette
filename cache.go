package cassette

import (
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
)

type (
	cache struct {
		c     *Cassette
		cache *lru.TwoQueueCache[string, cacheRecord]
	}
	cacheRecord struct {
		insertedAt time.Time
		providers  []peer.AddrInfo
		//TODO: optimize cache memory consumption by deduplication of addrinfos.
	}
)

func newCache(c *Cassette) (*cache, error) {
	twoq, err := lru.New2Q[string, cacheRecord](c.cacheSize)
	if err != nil {
		return nil, err
	}
	return &cache{
		c:     c,
		cache: twoq,
	}, nil
}

func (l *cache) getProviders(c cid.Cid) ([]peer.AddrInfo, bool) {
	value, ok := l.cache.Get(string(c.Hash()))
	switch {
	case !ok:
		return nil, false
	case time.Since(value.insertedAt) > l.c.cacheExpiry:
		l.expire(c)
		return nil, false
	default:
		return value.providers, true
	}
}

func (l *cache) putProviders(c cid.Cid, providers []peer.AddrInfo) {
	l.cache.Add(string(c.Hash()), cacheRecord{
		insertedAt: time.Now(),
		providers:  providers,
	})
}

func (l *cache) expire(c cid.Cid) {
	l.cache.Remove(string(c.Hash()))
}

func (l *cache) len() int {
	return l.cache.Len()
}
