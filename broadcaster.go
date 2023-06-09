package cassette

import (
	"context"
	"math"
	"time"

	"github.com/ipfs/boxo/bitswap/message"
	bitswap_message_pb "github.com/ipfs/boxo/bitswap/message/pb"
	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/mercari/go-circuitbreaker"
)

var bitswapOneTwo protocol.ID = "/ipfs/bitswap/1.2.0"

type (
	broadcaster struct {
		mailbox       chan any
		refreshTicker *time.Ticker
		c             *Cassette
	}
	addRecipient struct {
		id peer.ID
	}
	removeRecipient struct {
		id peer.ID
	}
	findCids struct {
		cids      []cid.Cid
		timestamp time.Time
	}
	channeledSender struct {
		ctx             context.Context
		cancel          context.CancelFunc
		id              peer.ID
		mailbox         chan findCids
		c               *Cassette
		unsentTimestamp time.Time
		// unsentCids maps the CIDs that are not sent yet to whether they should be cancelled or not.
		unsentCids   map[cid.Cid]bool
		maxBatchSize int
		maxBatchWait *time.Ticker
		cancelAfter  *time.Ticker
		cb           *circuitbreaker.CircuitBreaker
	}
)

func newBroadcaster(c *Cassette) *broadcaster {
	return &broadcaster{
		c:             c,
		mailbox:       make(chan any, 1),
		refreshTicker: time.NewTicker(c.recipientsRefreshInterval),
	}
}

func (b *broadcaster) start(ctx context.Context) {
	go func() {
		refresh := func() {
			logger.Info("Refreshing broadcast list...")
			peers := b.c.h.Peerstore().Peers()
			var count int
			for _, id := range peers {
				select {
				case <-ctx.Done():
					logger.Info("Refresh disrupted")
					return
				default:
					if id != b.c.h.ID() {
						b.mailbox <- addRecipient{
							id: id,
						}
						count++
					}
				}
			}
			logger.Infow("Broadcast list refreshed", "size", count)
		}
		refresh()
		for {
			select {
			case <-ctx.Done():
				logger.Info("Stopping broadcast recipient refresh")
				return
			case <-b.refreshTicker.C:
				refresh()
			}
		}
	}()

	go func() {
		recipients := make(map[peer.ID]*channeledSender)
		defer func() {
			logger.Infow("Stopping broadcast...")
			for id, sender := range recipients {
				sender.shutdown()
				delete(recipients, id)
				logger.Infow("Stopped broadcast to peer", "peer", id)
			}
			logger.Infow("Broadcasting stopped.")
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case cmd := <-b.mailbox:
				switch c := cmd.(type) {
				case findCids:
					for _, recipient := range recipients {
						select {
						case <-ctx.Done():
							return
						case recipient.mailbox <- c:
							b.c.metrics.notifyBroadcastRequested(ctx, int64(len(c.cids)))
						}
					}
				case addRecipient:
					if _, exists := recipients[c.id]; exists {
						continue
					}
					cs := b.newChanneledSender(c.id)
					go cs.start()
					recipients[c.id] = cs
					b.c.metrics.notifyBroadcastRecipientAdded(ctx)
				case removeRecipient:
					if cs, exists := recipients[c.id]; exists {
						cs.shutdown()
						delete(recipients, c.id)
						b.c.metrics.notifyBroadcastRecipientRemoved(ctx)
					}
				}
			}
		}
	}()
}

func (cs *channeledSender) start() {
	cancellations := make(map[cid.Cid]struct{})
	for {
		select {
		case <-cs.ctx.Done():
			return
		case fc, ok := <-cs.mailbox:
			if !ok {
				return
			}
			if cs.unsentTimestamp.IsZero() || cs.unsentTimestamp.After(fc.timestamp) {
				cs.unsentTimestamp = fc.timestamp
			}
			for _, c := range fc.cids {
				if _, exists := cs.unsentCids[c]; !exists {
					// Add as unsent with cancel set to false.
					cs.unsentCids[c] = false
					// Add to cancellations to be marked for cancellation later.
					cancellations[c] = struct{}{}
				}
			}
			if len(cs.unsentCids) >= cs.maxBatchSize {
				cs.sendUnsent()
			}
		case <-cs.maxBatchWait.C:
			if len(cs.unsentCids) != 0 {
				cs.sendUnsent()
			}
		case <-cs.cancelAfter.C:
			if len(cancellations) != 0 {
				for c := range cancellations {
					// When the CID has been broadcasted it gets removed from the unsent CIDs.
					// Therefore, only cancel CIDs that are no longer present in the unsent CIDs.
					// This won't work for CIDs that are repeatedly looked up in a short period of
					// time. However, Cassette typically sits behind a caching layer that should
					// cover this edge case.
					// TODO: add caching inside Cassette to make sure this is covered regardless of
					//       deployment topology.
					if _, ok := cs.unsentCids[c]; !ok {
						cs.unsentCids[c] = true
					}
					delete(cancellations, c)
				}
			}
		}
	}
}

func (cs *channeledSender) supportsHaves() bool {
	// Assure connection to peer before checking protocols list. Otherwise, GetProtocols
	// silently returns empty protocols list.
	if addrs := cs.c.h.Peerstore().Addrs(cs.id); len(addrs) == 0 {
		return false
	} else if err := cs.c.h.Connect(cs.ctx, peer.AddrInfo{ID: cs.id, Addrs: addrs}); err != nil {
		logger.Errorw("Failed to connect to peer in order to determine Want-Haves support", "peer", cs.id, "err", err)
		return false
	}
	protocols, err := cs.c.h.Peerstore().GetProtocols(cs.id)
	if err != nil {
		return false
	}
	for _, p := range protocols {
		if p == bitswapOneTwo {
			return true
		}
	}
	return false
}

func (cs *channeledSender) shutdown() {
	cs.cancel()
	cs.maxBatchWait.Stop()
	close(cs.mailbox)
}

func (cs *channeledSender) sendUnsent() {
	defer func() {
		cs.unsentTimestamp = time.Now()
	}()
	totalCidCount := int64(len(cs.unsentCids))
	if !cs.cb.Ready() {
		// Clear unsent CIDs since it will most likely take long enough for the node to become ready that
		// lookup requests have already timed out.
		cs.unsentCids = make(map[cid.Cid]bool)
		cs.c.metrics.notifyBroadcastFailed(cs.ctx, totalCidCount, circuitbreaker.ErrOpen, time.Since(cs.unsentTimestamp))
		return
	}
	var wantHave bool
	var wlt bitswap_message_pb.Message_Wantlist_WantType
	if cs.supportsHaves() {
		wlt = bitswap_message_pb.Message_Wantlist_Have
		wantHave = true
	} else if cs.c.fallbackOnWantBlock {
		wlt = bitswap_message_pb.Message_Wantlist_Block
	} else {
		logger.Warnw("Peer does not support Want-Haves and fallback on Want-Blocks is disabled. Skipping broadcast.", "peer", cs.id, "skipped", len(cs.unsentCids))
		// Clear unsent CIDs.
		cs.unsentCids = make(map[cid.Cid]bool)
		cs.c.metrics.notifyBroadcastSkipped(cs.ctx, totalCidCount, time.Since(cs.unsentTimestamp))

		// Fail the Circuit Breaker to avoid checking whether the recipient supports Want-Haves on every send, since
		// it is a characteristic that does not change often.
		cs.cb.Fail()
		return
	}

	var cancelCount int64
	msg := message.New(false)
	for c, cancel := range cs.unsentCids {
		if cancel {
			msg.Cancel(c)
			cancelCount++
		} else {
			msg.AddEntry(c, math.MaxInt32, wlt, false)
		}
		delete(cs.unsentCids, c)
	}
	if err := cs.c.bsn.SendMessage(cs.ctx, cs.id, msg); err != nil {
		logger.Errorw("Failed to send message", "to", cs.id, "err", err)
		cs.c.metrics.notifyBroadcastFailed(cs.ctx, totalCidCount, err, time.Since(cs.unsentTimestamp))
		// SendMessage wraps the context internally with configured bitswap timeouts.
		// Therefore, calling FailWithContext on Circuit Breaker will have no effect if deadline exceeds and would fail
		// the Circuit Breaker regardless of what the WithFailOnContextDeadline option is set to.
		// This is fine since we do want it to fail.
		// Regardless, do call FailWithContext so that the WithFailOnContextCancel is respected.
		cs.cb.FailWithContext(cs.ctx)
	} else {
		cs.c.metrics.notifyBroadcastSucceeded(cs.ctx, totalCidCount, cancelCount, wantHave, time.Since(cs.unsentTimestamp))
		cs.cb.Success()
	}
}

func (b *broadcaster) newChanneledSender(id peer.ID) *channeledSender {
	cs := channeledSender{
		id:           id,
		mailbox:      make(chan findCids, b.c.broadcastSendChannelBuffer),
		c:            b.c,
		unsentCids:   make(map[cid.Cid]bool),
		maxBatchSize: b.c.maxBroadcastBatchSize,
		maxBatchWait: time.NewTicker(b.c.maxBroadcastBatchWait),
		cancelAfter:  time.NewTicker(b.c.broadcastCancelAfter),
	}
	cs.ctx, cs.cancel = context.WithCancel(context.Background())

	cs.cb = circuitbreaker.New(
		circuitbreaker.WithFailOnContextCancel(b.c.recipientCBFailOnContextCancel),
		circuitbreaker.WithFailOnContextDeadline(b.c.recipientCBFailOnContextDeadline),
		circuitbreaker.WithHalfOpenMaxSuccesses(b.c.recipientCBHalfOpenMaxSuccesses),
		circuitbreaker.WithOpenTimeoutBackOff(b.c.recipientCBOpenTimeoutBackOff),
		circuitbreaker.WithOpenTimeout(b.c.recipientCBOpenTimeout),
		circuitbreaker.WithCounterResetInterval(b.c.recipientCBCounterResetInterval),
		circuitbreaker.WithTripFunc(b.c.recipientCBTripFunc),
		circuitbreaker.WithOnStateChangeHookFn(func(from, to circuitbreaker.State) {
			b.c.metrics.notifyBroadcastRecipientCBStateChanged(cs.ctx, id, from, to)
			logger.Infow("Broadcast recipient circuit breaker state changed", "recipient", id, "from", from, "to", to)
			switch to {
			case circuitbreaker.StateOpen:
				b.mailbox <- removeRecipient{id: id}
				logger.Warnw("Removing recipient with opened circuit breaker", "recipient", id, "from", from, "to", to)
			}
		}),
	)
	return &cs
}

func (b *broadcaster) broadcastWant(c []cid.Cid) {
	b.mailbox <- findCids{cids: c, timestamp: time.Now()}
}
