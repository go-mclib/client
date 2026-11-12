package client

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-mclib/data/packets"
)

// testServer accepts TCP connections; closeConns decides whether each accepted
// connection is closed immediately (ending the client's session) or held open.
type testServer struct {
	listener net.Listener
	mu       sync.Mutex
	conns    []net.Conn
}

func newTestServer(t *testing.T, closeConns bool) *testServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &testServer{listener: listener}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			if closeConns {
				_ = conn.Close()
				continue
			}
			s.mu.Lock()
			s.conns = append(s.conns, conn)
			s.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, conn := range s.conns {
			_ = conn.Close()
		}
	})
	return s
}

func newTestClient(address string, maxAttempts int) (*Client, *bytes.Buffer) {
	c := New(address, "tester", false)
	var logs bytes.Buffer
	c.Logger = log.New(&lockedWriter{w: &logs}, "", 0)
	c.MaxReconnectAttempts = maxAttempts
	c.ReconnectDelay = 10 * time.Millisecond
	return c, &logs
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func runClient(t *testing.T, ctx context.Context, c *Client) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- c.ConnectAndStart(ctx) }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("ConnectAndStart did not return")
		return nil
	}
}

func TestSendPacketNotConnected(t *testing.T) {
	c := New("127.0.0.1:1", "tester", false)
	if err := c.SendPacket(&packets.C2SPunch{}); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("SendPacket before connect = %v, want ErrNotConnected", err)
	}
}

func TestFirstDialFailureIsFinal(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	c, logs := newTestClient(address, -1)
	if err := runClient(t, t.Context(), c); !errors.Is(err, errDial) {
		t.Fatalf("ConnectAndStart = %v, want dial error", err)
	}
	if strings.Contains(logs.String(), "reconnecting in") {
		t.Fatalf("first dial failure was retried:\n%s", logs)
	}
}

func TestReconnectRetriesDialFailures(t *testing.T) {
	server := newTestServer(t, true)
	c, logs := newTestClient(server.listener.Addr().String(), 2)
	// After the first session the server goes away: reconnect dials must be
	// retried until MaxReconnectAttempts is exhausted.
	var sessions atomic.Int32
	c.OnConnect(func() {
		sessions.Add(1)
		_ = server.listener.Close()
	})

	if err := runClient(t, t.Context(), c); !errors.Is(err, errDial) {
		t.Fatalf("ConnectAndStart = %v, want dial error", err)
	}
	if got := strings.Count(logs.String(), "reconnecting in"); got != 2 {
		t.Fatalf("reconnect attempts = %d, want 2:\n%s", got, logs)
	}
	if got := sessions.Load(); got != 1 {
		t.Fatalf("sessions = %d, want 1", got)
	}
}

func TestAttemptsResetAfterPlay(t *testing.T) {
	server := newTestServer(t, true)
	c, _ := newTestClient(server.listener.Addr().String(), 1)
	var sessions atomic.Int32
	c.OnConnect(func() {
		c.firePlay() // every session reaches play before the server closes it
		if sessions.Add(1) == 3 {
			_ = c.Disconnect(true)
		}
	})

	if err := runClient(t, t.Context(), c); err != nil {
		t.Fatalf("ConnectAndStart = %v, want nil after Disconnect(true)", err)
	}
	if got := sessions.Load(); got != 3 {
		t.Fatalf("sessions = %d, want 3 (attempts must reset after reaching play)", got)
	}
}

func TestForcedDisconnectDoesNotReconnect(t *testing.T) {
	server := newTestServer(t, false)
	c, _ := newTestClient(server.listener.Addr().String(), -1)
	var sessions atomic.Int32
	c.OnConnect(func() {
		sessions.Add(1)
		go func() { _ = c.Disconnect(true) }()
	})

	if err := runClient(t, t.Context(), c); err != nil {
		t.Fatalf("ConnectAndStart = %v, want nil", err)
	}
	if got := sessions.Load(); got != 1 {
		t.Fatalf("sessions = %d, want 1", got)
	}
}

func TestContextCancelStopsClient(t *testing.T) {
	server := newTestServer(t, false)
	c, _ := newTestClient(server.listener.Addr().String(), -1)
	ctx, cancel := context.WithCancel(t.Context())
	var sessions atomic.Int32
	c.OnConnect(func() {
		sessions.Add(1)
		cancel()
	})

	if err := runClient(t, ctx, c); !errors.Is(err, context.Canceled) {
		t.Fatalf("ConnectAndStart = %v, want context.Canceled", err)
	}
	if got := sessions.Load(); got != 1 {
		t.Fatalf("sessions = %d, want 1", got)
	}
}

func TestSwarmStartCancelsOthersOnError(t *testing.T) {
	server := newTestServer(t, false)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unreachable := listener.Addr().String()
	_ = listener.Close()

	s := NewSwarm()
	healthy, _ := newTestClient(server.listener.Addr().String(), -1)
	failing, _ := newTestClient(unreachable, 0)
	s.Add(healthy)
	s.Add(failing)

	done := make(chan error, 1)
	go func() { done <- s.Start(t.Context()) }()
	select {
	case err := <-done:
		if !errors.Is(err, errDial) {
			t.Fatalf("Swarm.Start = %v, want dial error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Swarm.Start did not stop the remaining clients")
	}
}
