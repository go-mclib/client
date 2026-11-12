package inventory

import (
	"context"
	"errors"
	"sync"
	"time"
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
	done := sync.OnceFunc(func() { <-m.transactions; cancel() })
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
