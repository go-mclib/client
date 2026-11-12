package physics

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/data/packetids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
)

// pipePhysics returns an active physics session whose packets are written to
// an unbuffered pipe: a packet stays in flight until remote reads it.
func pipePhysics(t *testing.T) (*Module, net.Conn) {
	t.Helper()
	c := client.New("unused", "test", false)
	local, remote := net.Pipe()
	t.Cleanup(func() { local.Close(); remote.Close() })
	remote.SetDeadline(time.Now().Add(2 * time.Second))
	m := New()
	m.client, m.self = c, self.New()
	m.session = client.NewSimulator(c).Connect(local, jp.StatePlay)
	m.tickContext, m.cancel = context.WithCancel(m.session.Context())
	m.positionKnown = true
	return m, remote
}

func readPacketID(t *testing.T, remote net.Conn) int {
	t.Helper()
	wire, err := jp.ReadWirePacketFrom(remote, -1)
	if err != nil {
		t.Fatal(err)
	}
	return int(wire.PacketID)
}

func awaitResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("queued packet never completed")
		return nil
	}
}

func TestQueuedPhysicsPacketsCannotSurviveSessionReset(t *testing.T) {
	m, remote := pipePhysics(t)
	// a session packet ahead of the physics packets blocks the writer on the
	// pipe until remote reads it
	inFlight, err := m.session.Send(t.Context(), &packets.C2SClientTickEnd{})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := m.QueuePacketDone(t.Context(), &packets.C2SPlayerInput{Flags: 1})
	if err != nil {
		t.Fatal(err)
	}
	m.Reset()
	if m.Available() {
		t.Fatal("reset physics remains available")
	}
	if id := readPacketID(t, remote); id != packetids.C2SClientTickEndID {
		t.Fatalf("first packet = %d", id)
	}
	if err := awaitResult(t, inFlight); err != nil {
		t.Fatalf("written packet reported %v", err)
	}
	if err := awaitResult(t, queued); !errors.Is(err, context.Canceled) {
		t.Fatalf("stale packet survived reset: %v", err)
	}
}

func TestActionContextCancelsQueuedEffectWithoutEndingPhysics(t *testing.T) {
	m, remote := pipePhysics(t)
	if _, err := m.session.Send(t.Context(), &packets.C2SClientTickEnd{}); err != nil {
		t.Fatal(err)
	}
	action, stop := context.WithCancel(t.Context())
	attack, err := m.QueuePacketDone(action, &packets.C2SAttack{EntityID: 42})
	if err != nil {
		t.Fatal(err)
	}
	stop()
	readPacketID(t, remote)
	if err := awaitResult(t, attack); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled attack could still serialize: %v", err)
	}
	if !m.Available() {
		t.Fatal("cancelling an action shut down unrelated physics")
	}
}

func TestQueuePacketDoneReportsWriteInOrder(t *testing.T) {
	m, remote := pipePhysics(t)
	if err := m.QueuePacket(&packets.C2SClientTickEnd{}); err != nil {
		t.Fatal(err)
	}
	done, err := m.QueuePacketDone(t.Context(), &packets.C2SAttack{EntityID: 7})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("completion before the packet was written: %v", err)
	default:
	}
	if id := readPacketID(t, remote); id != packetids.C2SClientTickEndID {
		t.Fatalf("earlier packet not written first: %d", id)
	}
	if id := readPacketID(t, remote); id != packetids.C2SAttackID {
		t.Fatalf("second packet = %d", id)
	}
	if err := awaitResult(t, done); err != nil {
		t.Fatalf("written packet reported %v", err)
	}
}

func TestFullOutgoingQueueStopsPhysicsWithoutBlocking(t *testing.T) {
	m, _ := pipePhysics(t)
	var err error
	for range 200 {
		if err = m.QueuePacket(&packets.C2SPlayerInput{Flags: 1}); err != nil {
			break
		}
	}
	if err == nil {
		t.Fatal("full transport queue accepted input")
	}
	if !errors.Is(m.tickContext.Err(), context.Canceled) || m.Available() {
		t.Fatal("full queue left stale movement active")
	}
}

func TestSprintAndSneakUseCurrentPlayerInputProtocol(t *testing.T) {
	m, remote := pipePhysics(t)
	s := self.New()
	received := make(chan *jp.WirePacket, 16)
	go func() {
		defer close(received)
		for {
			wire, err := jp.ReadWirePacketFrom(remote, -1)
			if err != nil {
				return
			}
			received <- wire
		}
	}()
	s.SetSprinting(true)
	m.sendTickPackets(s)
	s.SetSprinting(false)
	s.SetSneaking(true)
	m.sendTickPackets(s)
	s.SetSneaking(false)
	m.sendTickPackets(s)

	// Current vanilla actions 1/2 start/stop sprint; 3/4 mean riding jump.
	wantIDs := []int{
		packetids.C2SPlayerInputID, packetids.C2SPlayerCommandID, packetids.C2SClientTickEndID,
		packetids.C2SPlayerInputID, packetids.C2SPlayerCommandID, packetids.C2SClientTickEndID,
		packetids.C2SPlayerInputID, packetids.C2SClientTickEndID,
	}
	wantFlags, wantActions := []uint8{64, 32, 0}, []int{1, 2}
	for _, id := range wantIDs {
		select {
		case wire := <-received:
			if int(wire.PacketID) != id {
				t.Fatalf("packet ID = %d, want %d", wire.PacketID, id)
			}
			switch id {
			case packetids.C2SPlayerInputID:
				var input packets.C2SPlayerInput
				if err := wire.ReadInto(&input); err != nil {
					t.Fatal(err)
				}
				if uint8(input.Flags) != wantFlags[0] {
					t.Fatalf("input flags = %d, want %d", input.Flags, wantFlags[0])
				}
				wantFlags = wantFlags[1:]
			case packetids.C2SPlayerCommandID:
				var command packets.C2SPlayerCommand
				if err := wire.ReadInto(&command); err != nil {
					t.Fatal(err)
				}
				if int(command.ActionID) != wantActions[0] {
					t.Fatalf("player action = %d, want %d", command.ActionID, wantActions[0])
				}
				wantActions = wantActions[1:]
			}
		case <-time.After(2 * time.Second):
			t.Fatal("missing movement packet")
		}
	}
	if err := m.QueuePacket(&packets.C2SClientTickEnd{}); err != nil {
		t.Fatal(err)
	}
	if wire := <-received; wire == nil || int(wire.PacketID) != packetids.C2SClientTickEndID {
		t.Fatal("sneaking emitted an obsolete player command")
	}
}
