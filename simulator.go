package client

import (
	"context"
	"net"

	jp "github.com/go-mclib/protocol/javaproto"
)

// Simulator drives a client's connection lifecycle without a server, for
// tests of modules and bots built on the client. Production code uses
// ConnectAndStart, which runs the real handshake.
type Simulator struct{ c *Client }

// NewSimulator returns a simulator for c.
func NewSimulator(c *Client) Simulator { return Simulator{c: c} }

// Connect starts a new session over conn in state without dialing, logging
// in or reading packets; feed packets to modules directly. With a nil conn,
// writes fail with an error. No lifecycle callback runs. The session ends with
// Disconnect.
func (s Simulator) Connect(conn net.Conn, state jp.State) *Session {
	if cur := s.c.Session(); cur != nil {
		cur.link.close()
	}
	transport := jp.NewTCPClient()
	if conn != nil {
		transport.SetConn(jp.NewConn(conn))
	}
	transport.SetState(state)
	host, port := "", ""
	if conn != nil {
		host, port, _ = net.SplitHostPort(conn.RemoteAddr().String())
	}
	return s.c.beginSession(context.Background(), newLink(transport, host, port, s.c.Logger.Printf))
}

// SetState switches the current session's protocol state, first connecting
// a session without a connection when there is none.
func (s Simulator) SetState(state jp.State) {
	if s.c.Session() == nil {
		s.Connect(nil, state)
		return
	}
	s.c.setState(state)
}

// Play fires the OnPlay callbacks, as when configuration finishes.
func (s Simulator) Play() { s.c.firePlay() }

// Transfer starts a new session on the same connection and fires the
// OnTransfer callbacks, as when the server returns the client to configuration.
func (s Simulator) Transfer() {
	if cur := s.c.Session(); cur != nil {
		s.c.beginSession(context.Background(), cur.link)
	}
	s.c.fireTransfer()
}

// Disconnect closes the connection, ends the session and fires the
// OnDisconnect callbacks.
func (s Simulator) Disconnect() {
	if cur := s.c.Session(); cur != nil {
		cur.link.close()
	}
	s.c.endSession()
	s.c.fireDisconnect()
}
