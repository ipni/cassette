package cassette

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/ipni/go-libipni/apierror"
	"github.com/ipni/go-libipni/find/model"
	"github.com/ipni/go-libipni/rwriter"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multicodec"
	"github.com/multiformats/go-varint"
)

const ipniCascadeQueryKey = "cascade"

var (
	cascadeContextID = []byte("legacy-cascade")
	cascadeMetadata  = varint.ToUvarint(uint64(multicodec.TransportBitswap))
)

func (c *Cassette) serveMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/cid", c.handleMh)
	mux.HandleFunc("/cid/", c.handleMhSubtree)
	mux.HandleFunc("/multihash", c.handleMh)
	mux.HandleFunc("/multihash/", c.handleMhSubtree)
	mux.HandleFunc("/ready", c.handleReady)
	mux.HandleFunc("/", c.handleCatchAll)
	return mux
}

func (c *Cassette) handleMh(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodOptions:
		discardBody(r)
		c.handleLookupOptions(w)
	default:
		discardBody(r)
		http.Error(w, "", http.StatusNotFound)
	}
}

func (c *Cassette) handleMhSubtree(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		rspWriter, err := rwriter.New(w, r, rwriter.WithPreferJson(c.httpResponsePreferJson))
		if err != nil {
			var apiErr *apierror.Error
			if errors.As(err, &apiErr) {
				http.Error(w, apiErr.Error(), apiErr.Status())
				return
			}
			http.Error(w, "", http.StatusBadRequest)
			return
		}
		if c.ipniRequireCascadeQueryParam {
			present, matched := rwriter.MatchQueryParam(r, ipniCascadeQueryKey, c.ipniCascadeLabel)
			if !present {
				logger.Debugw("Rejected request with unspecified cascade query parameter.")
				http.Error(w, "", http.StatusNotFound)
				return
			}
			if !matched {
				labels := r.URL.Query()[ipniCascadeQueryKey]
				logger.Infow("Rejected request with mismatching cascade label.", "want", c.ipniCascadeLabel, "got", labels)
				http.Error(w, "", http.StatusNotFound)
				return
			}
		}
		c.handleLookup(rwriter.NewProviderResponseWriter(rspWriter), r)
	case http.MethodOptions:
		discardBody(r)
		c.handleLookupOptions(w)
	default:
		discardBody(r)
		http.Error(w, "", http.StatusNotFound)
	}
}

func (c *Cassette) handleLookup(w *rwriter.ProviderResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	pch := c.Find(ctx, w.Cid())
	defer cancel()
LOOP:
	for {
		select {
		case <-c.ctx.Done():
			logger.Debugw("Interrupted while responding to lookup", "key", w.Cid(), "err", ctx.Err())
			break LOOP
		case provider, ok := <-pch:
			if !ok {
				logger.Debugw("No more provider records", "key", w.Cid())
				break LOOP
			}
			err := w.WriteProviderResult(model.ProviderResult{
				ContextID: cascadeContextID,
				Metadata:  cascadeMetadata,
				Provider: &peer.AddrInfo{
					ID:    provider.ID,
					Addrs: provider.Addrs,
				},
			})
			if err != nil {
				logger.Errorw("Failed to encode provider record", "err", err)
				break LOOP
			}
		}
	}
	if err := w.Close(); err != nil {
		var apiErr *apierror.Error
		if errors.As(err, &apiErr) {
			http.Error(w, apiErr.Error(), apiErr.Status())
		} else {
			logger.Errorw("Failed to finalize lookup results", "err", err)
			http.Error(w, "", http.StatusInternalServerError)
		}
		return
	}
}

func (c *Cassette) handleLookupOptions(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", c.httpAllowOrigin)
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("X-IPNI-Allow-Cascade", c.ipniCascadeLabel)
	w.WriteHeader(http.StatusAccepted)
}

func (c *Cassette) handleReady(w http.ResponseWriter, r *http.Request) {
	discardBody(r)
	switch r.Method {
	case http.MethodGet:
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "", http.StatusNotFound)
	}
}

func (c *Cassette) handleCatchAll(w http.ResponseWriter, r *http.Request) {
	discardBody(r)
	http.Error(w, "", http.StatusNotFound)
}

func discardBody(r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
}
