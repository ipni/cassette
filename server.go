package cassette

import (
	"context"
	"io"
	"net/http"
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
		c.handleLookup(newIPNILookupResponseWriter(w, c.ipniCascadeLabel, c.ipniRequireCascadeQueryParam, c.httpResponsePreferJson), r)
	case http.MethodOptions:
		discardBody(r)
		c.handleLookupOptions(w)
	default:
		discardBody(r)
		http.Error(w, "", http.StatusNotFound)
	}
}

func (c *Cassette) handleLookup(w lookupResponseWriter, r *http.Request) {
	if err := w.Accept(r); err != nil {
		switch e := err.(type) {
		case errHttpResponse:
			e.WriteTo(w)
		default:
			logger.Errorw("Failed to accept lookup request", "err", err)
			http.Error(w, "", http.StatusInternalServerError)
		}
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	pch := c.Find(ctx, w.Key())
	defer cancel()
LOOP:
	for {
		select {
		case <-c.ctx.Done():
			logger.Debugw("Interrupted while responding to lookup", "key", w.Key(), "err", ctx.Err())
			break LOOP
		case provider, ok := <-pch:
			if !ok {
				logger.Debugw("No more provider records", "key", w.Key())
				break LOOP
			}
			if err := w.WriteProviderRecord(providerRecord{AddrInfo: provider}); err != nil {
				logger.Errorw("Failed to encode provider record", "err", err)
				break LOOP
			}
		}
	}
	if err := w.Close(); err != nil {
		switch e := err.(type) {
		case errHttpResponse:
			e.WriteTo(w)
		default:
			logger.Errorw("Failed to finalize lookup results", "err", err)
			http.Error(w, "", http.StatusInternalServerError)
		}
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
