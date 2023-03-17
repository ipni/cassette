package cassette

import (
	"context"
	"math"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-libipfs/bitswap/message"
	bitswap_message_pb "github.com/ipfs/go-libipfs/bitswap/message/pb"
	"github.com/ipfs/go-libipfs/bitswap/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
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
		cids []cid.Cid
	}
	channeledSender struct {
		ctx        context.Context
		cancel     context.CancelFunc
		id         peer.ID
		outgoing   chan message.BitSwapMessage
		sender     network.MessageSender
		senderOpts network.MessageSenderOpts
		b          *broadcaster
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
					}
				}
			}
			logger.Infow("Broadcast list refreshed", "size", len(peers))
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
					// Construct both possible flavours once to avoid generation per recipient to
					// to reduce GC.
					wantHave := message.New(false)
					wantBlock := message.New(false)
					for _, k := range c.cids {
						wantHave.AddEntry(k, math.MaxInt32, bitswap_message_pb.Message_Wantlist_Have, false)
						wantBlock.AddEntry(k, math.MaxInt32, bitswap_message_pb.Message_Wantlist_Block, false)
					}
					for _, recipient := range recipients {
						var msg message.BitSwapMessage
						if recipient.supportsHaves() {
							msg = wantHave
						} else {
							msg = wantBlock
						}
						select {
						case <-ctx.Done():
							return
						case recipient.outgoing <- msg:
						}
					}
				case addRecipient:
					if _, exists := recipients[c.id]; exists {
						continue
					}
					cs := b.newChanneledSender(c.id)
					go cs.start()
					recipients[c.id] = cs
				case removeRecipient:
					if cs, exists := recipients[c.id]; exists {
						cs.shutdown()
						delete(recipients, c.id)
					}
				}
			}
		}
	}()
}

func (cs *channeledSender) start() {
	for {
		select {
		case <-cs.ctx.Done():
			return
		case msg, ok := <-cs.outgoing:
			if !ok {
				return
			}
			if cs.sender == nil {
				var err error
				cs.sender, err = cs.b.c.bsn.NewMessageSender(cs.ctx, cs.id, &cs.senderOpts)
				if err != nil {
					// TODO hook up to circuit breaker
					logger.Errorw("Failed to instantiate sender", "to", cs.id, "err", err)
					continue
				}
			}
			if err := cs.sender.SendMsg(cs.ctx, msg); err != nil {
				logger.Errorw("Failed to send message", "to", cs.id, "err", err)
			}
			// TODO: basic tests show there is no point reusing sender; investigate further at load
			//       for now, reset per send.
			_ = cs.sender.Reset()
			cs.sender = nil
		}
	}
}

func (cs *channeledSender) supportsHaves() bool {
	// TODO open stream to detect this as that is more reliable
	// TODO cache supported protocol IDs for the recipient
	protocols, err := cs.b.c.h.Peerstore().GetProtocols(cs.id)
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
	close(cs.outgoing)
}

func (b *broadcaster) newChanneledSender(id peer.ID) *channeledSender {
	cs := channeledSender{
		id:         id,
		senderOpts: b.c.messageSenderOpts,
		outgoing:   make(chan message.BitSwapMessage, b.c.messageSenderBuffer),
		b:          b,
	}
	cs.ctx, cs.cancel = context.WithCancel(context.Background())
	return &cs
}

func (b *broadcaster) broadcastWant(c []cid.Cid) {
	b.mailbox <- findCids{cids: c}
}
