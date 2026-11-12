package client

import (
	"context"
	"errors"
	"fmt"
	"sync"

	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// ErrQueueFull is returned by Session.Send when the outgoing queue is full.
var ErrQueueFull = errors.New("outgoing packet queue is full")

// errSessionEnded is returned for packets of a session that ended before they
// were written. The session's context was canceled, so it matches
// context.Canceled.
var errSessionEnded = fmt.Errorf("client session ended: %w", context.Canceled)

const outgoingQueueSize = 100

// Session is one phase of a server connection. It starts when the client
// connects and ends when the connection closes; a server transfer (play back
// to configuration) ends the session and starts a new one on the same
// connection. Context is canceled when the session ends, so work bound to a
// session can never act on a replacement connection.
//
// All methods are safe for concurrent use.
type Session struct {
	ctx    context.Context
	cancel context.CancelFunc
	link   *link
}

// link is one transport connection with its ordered writer queue.
type link struct {
	transport  *jp.TCPClient
	host, port string
	logger     func(format string, args ...any)

	queue chan queuedPacket
	mu    sync.Mutex // guards closed and enqueueing
	// closed is set when the writer stops; later packets are rejected.
	closed bool
	done   chan struct{}
}

type queuedPacket struct {
	packet jp.Packet
	result chan error // buffered; nil when nobody waits
}

// boundPacket fails at serialization once its contexts are done: the write
// lock may be held by another writer while the session or caller gives up.
type boundPacket struct {
	jp.Packet
	ctx, session context.Context
}

func (p boundPacket) Write(buf *ns.PacketBuffer) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if err := p.session.Err(); err != nil {
		return errSessionEnded
	}
	return p.Packet.Write(buf)
}

func newLink(transport *jp.TCPClient, host, port string, logf func(string, ...any)) *link {
	l := &link{
		transport: transport,
		host:      host,
		port:      port,
		logger:    logf,
		queue:     make(chan queuedPacket, outgoingQueueSize),
		done:      make(chan struct{}),
	}
	go l.writeLoop()
	return l
}

// writeLoop writes queued packets in order until the link closes, then
// reports every packet still queued as not written.
func (l *link) writeLoop() {
	for {
		select {
		case q := <-l.queue:
			err := l.transport.WritePacket(q.packet)
			if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				l.logger("error writing queued packet: %v", err)
			}
			if q.result != nil {
				q.result <- err
			}
		case <-l.done:
			l.mu.Lock()
			l.closed = true
			l.mu.Unlock()
			for {
				select {
				case q := <-l.queue:
					if q.result != nil {
						q.result <- ErrNotConnected
					}
				default:
					return
				}
			}
		}
	}
}

// close stops the writer and closes the transport. Safe to call repeatedly.
func (l *link) close() {
	l.mu.Lock()
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	l.mu.Unlock()
	_ = l.transport.Close()
}

func newSession(parent context.Context, l *link) *Session {
	ctx, cancel := context.WithCancel(parent)
	return &Session{ctx: ctx, cancel: cancel, link: l}
}

// Context is canceled when the session ends.
func (s *Session) Context() context.Context { return s.ctx }

// Conn returns the session's connection, or nil for a session without one
// (see Simulator).
func (s *Session) Conn() *jp.Conn { return s.link.transport.Conn() }

// ResolvedAddr returns the resolved host and port of the session's connection.
func (s *Session) ResolvedAddr() (host, port string) { return s.link.host, s.link.port }

// Write writes p to the session's connection immediately, bypassing the
// queue. It fails without writing once ctx is done or the session has ended,
// including while waiting for another writer. If ctx is canceled while the
// write is blocked on the network, the connection is closed: a partially
// written frame cannot be recovered.
func (s *Session) Write(ctx context.Context, p jp.Packet) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.ctx.Err() != nil {
		return errSessionEnded
	}
	stop := context.AfterFunc(ctx, func() {
		// a session that already ended is not this caller's to close
		if s.ctx.Err() == nil {
			_ = s.link.transport.Close()
		}
	})
	err := s.link.transport.WritePacket(boundPacket{Packet: p, ctx: ctx, session: s.ctx})
	stop()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if s.ctx.Err() != nil {
		return errSessionEnded
	}
	return err
}

// Send queues p on the connection's ordered writer without blocking. Packets
// sent earlier (through any session of the connection) are written first.
// It returns ErrQueueFull when the queue is full and an error once ctx is
// done or the session has ended. A packet still queued when ctx is done or
// the session ends is dropped. The returned channel receives the write result
// exactly once: nil after the packet was written to the transport.
func (s *Session) Send(ctx context.Context, p jp.Packet) (<-chan error, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.ctx.Err() != nil {
		return nil, errSessionEnded
	}
	result := make(chan error, 1)
	l := s.link
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, ErrNotConnected
	}
	select {
	case l.queue <- queuedPacket{packet: boundPacket{Packet: p, ctx: ctx, session: s.ctx}, result: result}:
		return result, nil
	default:
		return nil, ErrQueueFull
	}
}
