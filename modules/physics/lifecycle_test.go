package physics

import (
	"context"
	"errors"
	"testing"

	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/data/packet_ids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
)

func TestQueuedPhysicsPacketsCannotSurviveSessionReset(t *testing.T) {
	m := New()
	m.self = self.New()
	ctx, cancel := context.WithCancel(context.Background())
	m.tickContext, m.cancel = ctx, cancel
	queue := make(chan jp.Packet, 1)
	m.outgoing = queue
	m.positionKnown = true
	if err := m.QueuePacket(&packets.C2SPlayerInput{Flags: 1}); err != nil {
		t.Fatal(err)
	}
	queued := <-queue
	m.Reset()
	if m.Available() {
		t.Fatal("reset physics remains available")
	}
	if _, err := jp.ToWire(queued); !errors.Is(err, context.Canceled) {
		t.Fatalf("stale packet survived reset: %v", err)
	}
}

func TestActionContextCancelsQueuedEffectWithoutEndingPhysics(t *testing.T) {
	m := New()
	m.self = self.New()
	session, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.tickContext, m.cancel = session, cancel
	m.outgoing = make(chan jp.Packet, 1)
	m.positionKnown = true
	action, stop := context.WithCancel(context.Background())
	if err := m.QueuePacketContext(action, &packets.C2SAttack{EntityId: 42}); err != nil {
		t.Fatal(err)
	}
	queued := <-m.outgoing
	stop()
	if !m.Available() {
		t.Fatal("cancelling an action shut down unrelated physics")
	}
	if _, err := jp.ToWire(queued); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled attack could still serialize: %v", err)
	}
}

func TestFullOutgoingQueueStopsPhysicsWithoutBlocking(t *testing.T) {
	m := New()
	m.self = self.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.tickContext, m.cancel = ctx, cancel
	m.outgoing = make(chan jp.Packet)
	m.positionKnown = true
	if err := m.QueuePacket(&packets.C2SPlayerInput{Flags: 1}); err == nil {
		t.Fatal("full transport queue accepted input")
	}
	if ctx.Err() != context.Canceled || m.Available() {
		t.Fatal("full queue left stale movement active")
	}
}

func TestSprintAndSneakUseCurrentPlayerInputProtocol(t *testing.T) {
	m, s := New(), self.New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.tickContext, m.cancel = ctx, cancel
	m.outgoing = make(chan jp.Packet, 16)
	s.SetSprinting(true)
	m.sendTickPackets(s)
	s.SetSprinting(false)
	s.SetSneaking(true)
	m.sendTickPackets(s)
	s.SetSneaking(false)
	m.sendTickPackets(s)

	// Current vanilla actions 1/2 start/stop sprint; 3/4 mean riding jump.
	wantIDs := []int{
		packet_ids.C2SPlayerInputID, packet_ids.C2SPlayerCommandID, packet_ids.C2SClientTickEndID,
		packet_ids.C2SPlayerInputID, packet_ids.C2SPlayerCommandID, packet_ids.C2SClientTickEndID,
		packet_ids.C2SPlayerInputID, packet_ids.C2SClientTickEndID,
	}
	wantFlags, wantActions := []uint8{64, 32, 0}, []int{1, 2}
	for _, id := range wantIDs {
		select {
		case packet := <-m.outgoing:
			wire, err := jp.ToWire(packet)
			if err != nil {
				t.Fatal(err)
			}
			if int(wire.PacketID) != id {
				t.Fatalf("packet ID = %d, want %d", wire.PacketID, id)
			}
			switch id {
			case packet_ids.C2SPlayerInputID:
				var input packets.C2SPlayerInput
				if err := wire.ReadInto(&input); err != nil {
					t.Fatal(err)
				}
				if uint8(input.Flags) != wantFlags[0] {
					t.Fatalf("input flags = %d, want %d", input.Flags, wantFlags[0])
				}
				wantFlags = wantFlags[1:]
			case packet_ids.C2SPlayerCommandID:
				var command packets.C2SPlayerCommand
				if err := wire.ReadInto(&command); err != nil {
					t.Fatal(err)
				}
				if int(command.ActionId) != wantActions[0] {
					t.Fatalf("player action = %d, want %d", command.ActionId, wantActions[0])
				}
				wantActions = wantActions[1:]
			}
		default:
			t.Fatal("missing movement packet")
		}
	}
	if len(m.outgoing) != 0 {
		t.Fatal("sneaking emitted an obsolete player command")
	}
}
