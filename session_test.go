package client

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/go-mclib/data/packetids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
)

// fakeServer drives one connection's server side.
type fakeServer struct {
	t    *testing.T
	conn net.Conn
}

func (s fakeServer) expect(ids ...int) {
	s.t.Helper()
	for _, id := range ids {
		wire, err := jp.ReadWirePacketFrom(s.conn, -1)
		if err != nil {
			s.t.Errorf("read packet %d: %v", id, err)
			return
		}
		if int(wire.PacketID) != id {
			s.t.Errorf("client sent packet %d, want %d", wire.PacketID, id)
		}
	}
}

func (s fakeServer) send(p jp.Packet) {
	s.t.Helper()
	wire, err := jp.ToWire(p)
	if err != nil {
		s.t.Error(err)
		return
	}
	if err := wire.WriteTo(s.conn, -1); err != nil {
		s.t.Error(err)
	}
}

func TestHandshakeDrivesSessionsThroughLoginPlayAndTransfer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c, _ := newTestClient(listener.Addr().String(), 0)

	type phase struct {
		event   string
		session *Session
		state   jp.State
	}
	phases := make(chan phase, 8)
	record := func(event string) func() {
		return func() { phases <- phase{event, c.Session(), c.State()} }
	}
	c.OnConnect(record("connect"))
	c.OnPlay(record("play"))
	c.OnTransfer(record("transfer"))
	c.OnDisconnect(record("disconnect"))
	await := func(event string) phase {
		t.Helper()
		select {
		case p := <-phases:
			if p.event != event {
				t.Fatalf("lifecycle event %q, want %q", p.event, event)
			}
			return p
		case <-time.After(5 * time.Second):
			t.Fatalf("no %q event", event)
			return phase{}
		}
	}

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		s := fakeServer{t: t, conn: conn}
		s.expect(packetids.C2SIntentionID, packetids.C2SHelloID)
		s.send(&packets.S2CLoginFinished{Profile: packets.GameProfile{Name: "tester"}})
		s.expect(packetids.C2SLoginAcknowledgedID, packetids.C2SCustomPayloadConfigurationID, packetids.C2SClientInformationConfigurationID)
		s.send(&packets.S2CFinishConfiguration{})
		s.expect(packetids.C2SFinishConfigurationID)
		s.send(&packets.S2CKeepAlivePlay{KeepAliveID: 42})
		s.expect(packetids.C2SKeepAlivePlayID)
		s.send(&packets.S2CStartConfiguration{})
		s.expect(packetids.C2SConfigurationAcknowledgedID)
	}()
	done := make(chan error, 1)
	go func() { done <- c.ConnectAndStart(t.Context()) }()

	connected := await("connect")
	if connected.session == nil || connected.state != jp.StateLogin {
		t.Fatalf("connect: session %v state %v", connected.session, connected.state)
	}
	played := await("play")
	if played.session != connected.session || played.state != jp.StatePlay {
		t.Fatalf("play: new session %v or state %v", played.session != connected.session, played.state)
	}
	transferred := await("transfer")
	if transferred.session == nil || transferred.session == played.session {
		t.Fatal("transfer did not start a new session")
	}
	if played.session.Context().Err() == nil {
		t.Fatal("play session outlived the transfer")
	}
	if _, err := played.session.Send(t.Context(), &packets.C2SClientTickEnd{}); err == nil {
		t.Fatal("ended session accepted a packet")
	}
	disconnected := await("disconnect")
	if disconnected.session != nil || transferred.session.Context().Err() == nil {
		t.Fatal("session survived the disconnect")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ConnectAndStart did not return")
	}
}

func TestSessionSendReportsCompletionInOrder(t *testing.T) {
	c := New("unused", "tester", false)
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	remote.SetDeadline(time.Now().Add(2 * time.Second))
	s := NewSimulator(c).Connect(local, jp.StatePlay)

	first, err := s.Send(t.Context(), &packets.C2SClientTickEnd{})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	dropped, err := s.Send(canceled, &packets.C2SPunch{})
	if err != nil {
		t.Fatal(err)
	}
	last, err := s.Send(t.Context(), &packets.C2SPunch{})
	if err != nil {
		t.Fatal(err)
	}
	cancel() // the writer is blocked on the first packet until remote reads

	for _, id := range []int{packetids.C2SClientTickEndID, packetids.C2SPunchID} {
		wire, err := jp.ReadWirePacketFrom(remote, -1)
		if err != nil {
			t.Fatal(err)
		}
		if int(wire.PacketID) != id {
			t.Fatalf("packet %d, want %d", wire.PacketID, id)
		}
	}
	if err := <-first; err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := <-dropped; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled packet: %v", err)
	}
	if err := <-last; err != nil {
		t.Fatalf("last: %v", err)
	}
}

func TestSessionWriteAndSendFailAfterSessionEnds(t *testing.T) {
	c := New("unused", "tester", false)
	sim := NewSimulator(c)
	old := sim.Connect(nil, jp.StatePlay)
	sim.Transfer()
	if c.Session() == old || old.Context().Err() == nil {
		t.Fatal("transfer kept the old session")
	}
	if err := old.Write(t.Context(), &packets.C2SPunch{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("write on ended session: %v", err)
	}
	if _, err := old.Send(t.Context(), &packets.C2SPunch{}); err == nil {
		t.Fatal("send on ended session accepted")
	}
	sim.Disconnect()
	if c.Session() != nil {
		t.Fatal("session after disconnect")
	}
	if err := c.SendPacket(&packets.C2SPunch{}); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("SendPacket after disconnect: %v", err)
	}
}

func TestSessionSendRejectsFullQueue(t *testing.T) {
	c := New("unused", "tester", false)
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	s := NewSimulator(c).Connect(local, jp.StatePlay)
	var err error
	for range outgoingQueueSize + 2 {
		if _, err = s.Send(t.Context(), &packets.C2SPunch{}); err != nil {
			break
		}
	}
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("full queue: %v", err)
	}
}
