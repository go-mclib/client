package inventory

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

// A nonblocking request holds the same transaction gate as Execute until its
// authoritative response arrives. Packet handlers release it without waiting.
type pendingInventoryRequest struct {
	tx              *inventoryTransaction
	contentRevision uint64
	done            func()
	timer           *time.Timer
}

func (m *Module) tryTransaction(window int32, revision uint64) (*inventoryTransaction, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.transactions == nil {
		m.transactions = make(chan struct{}, 1)
	}
	select {
	case m.transactions <- struct{}{}:
	default:
		return nil, nil, errors.New("inventory transaction in progress")
	}
	ctx, cancel := context.WithTimeout(context.Background(), inventoryClickTimeout)
	var once sync.Once
	done := func() { once.Do(func() { <-m.transactions; cancel() }) }
	if m.session == nil || m.session.ctx.Err() != nil {
		done()
		return nil, nil, errors.New("inventory session inactive")
	}
	s := m.snapshotLocked()
	if s.WindowID != window || s.Revision != revision {
		done()
		return nil, nil, errors.New("stale inventory window or revision")
	}
	tx := &inventoryTransaction{m: m, ctx: ctx, session: m.session, epoch: m.epoch, window: window, current: s}
	return tx, done, nil
}

func (m *Module) finishRequestLocked(uncertain bool) {
	pending := m.pendingRequest
	if pending == nil {
		return
	}
	m.pendingRequest = nil
	if pending.timer != nil {
		pending.timer.Stop()
	}
	if uncertain && pending.tx.epoch == m.epoch && pending.tx.session == m.session {
		if m.container == nil {
			m.ready = false
		} else {
			m.container.ready = false
		}
	}
	pending.done()
}

// submitHotbarSwap is deliberately nonblocking: action tick callers must keep
// dispatching packets. It reserves click ordering until full resync or timeout.
func (m *Module) submitHotbarSwap(containerSlot, hotbarIndex int, send func(jp.Packet) error) error {
	snapshot := m.Snapshot()
	tx, done, err := m.tryTransaction(0, snapshot.Revision)
	if err != nil {
		return err
	}
	if !snapshot.Ready || !snapshot.Cursor.IsEmpty() {
		done()
		return errors.New("inventory must be ready with an empty cursor")
	}
	m.mu.Lock()
	if err := tx.checkLocked(); err != nil {
		m.mu.Unlock()
		done()
		return err
	}
	pending := &pendingInventoryRequest{tx: tx, contentRevision: m.contentRevision, done: done}
	m.pendingRequest = pending
	pending.timer = time.AfterFunc(inventoryClickTimeout, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.pendingRequest == pending {
			m.finishRequestLocked(true)
			m.notifyLocked()
		}
	})
	m.mu.Unlock()
	err = send(transactionPacket{Packet: &packets.C2SContainerClick{
		WindowId: 0, StateId: -1, Slot: ns.Int16(containerSlot), Button: ns.Int8(hotbarIndex),
		Mode: 2, CarriedItem: ns.EmptyHashedSlot(),
	}, tx: tx, checkContents: true})
	if err != nil {
		m.mu.Lock()
		if m.pendingRequest == pending {
			m.finishRequestLocked(true)
			m.notifyLocked()
		}
		m.mu.Unlock()
	}
	return err
}
