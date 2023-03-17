package cassette

import (
	"context"
	"sync/atomic"

	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-libipfs/bitswap/message"
	bitswap_message_pb "github.com/ipfs/go-libipfs/bitswap/message/pb"
	"github.com/ipfs/go-libipfs/bitswap/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

var _ network.Receiver = (*receiver)(nil)

type (
	receiver struct {
		ctx             context.Context
		cancel          context.CancelFunc
		mailbox         chan any
		nextID          atomic.Int64
		findByMultihash bool
	}
	receivedMessageEvent struct {
		k  string
		id peer.ID
	}
	registerHook struct {
		id   int64
		k    string
		hook func(peer.ID)
	}
	unregisterHook struct {
		k  string
		id int64
	}
)

func newReceiver(findByMultihash bool) (*receiver, error) {
	var r receiver
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.findByMultihash = findByMultihash
	r.mailbox = make(chan any)
	go func() {
		type registeredHook struct {
			id   int64
			hook func(id peer.ID)
		}
		registry := make(map[string][]*registeredHook)
		for {
			select {
			case <-r.ctx.Done():
				return
			case e, ok := <-r.mailbox:
				if !ok {
					return
				}
				switch ee := e.(type) {
				case receivedMessageEvent:

					hooks, ok := registry[ee.k]
					if ok && hooks != nil {
						for _, hook := range hooks {
							select {
							case <-r.ctx.Done():
								return
							default:
								if hook != nil {
									hook.hook(ee.id)
								}
							}
						}
					}
				case registerHook:
					registry[ee.k] = append(registry[ee.k], &registeredHook{id: ee.id, hook: ee.hook})
				case unregisterHook:
					hooks, ok := registry[ee.k]
					if ok {
						hooksLen := len(hooks)
						switch hooksLen {
						case 0, 1:
							delete(registry, ee.k)
						default:
						SearchLoop:
							for i := 0; i < hooksLen; i++ {
								select {
								case <-r.ctx.Done():
									return
								default:
									if hooks[i].id == ee.id {
										// Remove without preserving order
										hooks[i] = hooks[hooksLen-1]
										hooks = hooks[:hooksLen-1]
										// We expect to find only one hook for a given ID.
										break SearchLoop
									}
								}
							}
							registry[ee.k] = hooks
						}
					}
				}
			}
		}
	}()
	return &r, nil
}

func (r *receiver) keyFromCid(c cid.Cid) string {
	if r.findByMultihash {
		return string(c.Hash())
	}
	return c.String()
}

func (r *receiver) ReceiveMessage(ctx context.Context, sender peer.ID, in message.BitSwapMessage) {
	if len(in.Haves()) > 0 {
		for _, c := range in.Haves() {
			select {
			case <-ctx.Done():
				return
			case r.mailbox <- receivedMessageEvent{
				k:  r.keyFromCid(c),
				id: sender,
			}:
			}
		}
	}
	if len(in.BlockPresences()) > 0 {
		for _, c := range in.BlockPresences() {
			if c.Type == bitswap_message_pb.Message_Have {
				select {
				case <-ctx.Done():
					return
				case r.mailbox <- receivedMessageEvent{
					k:  r.keyFromCid(c.Cid),
					id: sender,
				}:
				}
			}
		}
	}
	if len(in.Blocks()) > 0 {
		for _, c := range in.Blocks() {
			select {
			case <-ctx.Done():
				return
			case r.mailbox <- receivedMessageEvent{
				k:  r.keyFromCid(c.Cid()),
				id: sender,
			}:
			}
		}
	}
}

func (r *receiver) ReceiveError(err error) {
	// TODO hook this up to circuit breakers?
	logger.Errorw("Received Error", "err", err)
}

func (r *receiver) PeerConnected(id peer.ID) {
	logger.Debugw("peer connected", "id", id)
}

func (r *receiver) PeerDisconnected(id peer.ID) {
	logger.Debugw("peer disconnected", "id", id)
}

func (r *receiver) registerFoundHook(ctx context.Context, k cid.Cid, f func(id peer.ID)) func() {
	id := r.nextHookID()
	kk := r.keyFromCid(k)
	select {
	case <-ctx.Done():
	case r.mailbox <- registerHook{k: kk, hook: f, id: id}:
	}
	return func() {
		r.mailbox <- unregisterHook{k: kk, id: id}
	}
}

func (r *receiver) nextHookID() int64 {
	return r.nextID.Add(1)
}
