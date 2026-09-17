package interaction

import (
	"bytes"
	"math"
	"testing"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/inventory"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/data/items"
	"github.com/go-mclib/data/packet_ids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func itemUseClient(t *testing.T) (*client.Client, *self.Module, *inventory.Module) {
	t.Helper()
	c := client.New("unused", "Grace", false)
	c.SetState(jp.StatePlay)
	me, inv := self.New(), inventory.New()
	c.Register(me)
	c.Register(inv)
	slots := make([]ns.Slot, inventory.TotalSlots)
	slots[inventory.SlotOffhand] = ns.NewSlot(ns.VarInt(items.ItemID("minecraft:bow")), 1)
	wire, err := jp.ToWire(&packets.S2CContainerSetContent{Slots: slots})
	if err != nil {
		t.Fatal(err)
	}
	inv.HandlePacket(wire)
	return c, me, inv
}

func TestItemUseVanillaWireAndNoPrediction(t *testing.T) {
	c, me, inv := itemUseClient(t)
	me.SetRotation(90, -30)
	var wire []*jp.WirePacket
	send := func(packet jp.Packet) error {
		p, err := jp.ToWire(packet)
		if err == nil {
			wire = append(wire, p)
		}
		return err
	}
	begin, err := BeginItemUse(c, 1, send)
	if err != nil {
		t.Fatal(err)
	}
	// Offhand must work even though the main hand is empty. Sequence 1 is
	// predicted only for begin; release uses vanilla's non-predicted sequence 0.
	if begin.Sequence != 1 {
		t.Fatalf("begin sequence = %d", begin.Sequence)
	}
	if err := ReleaseItemUse(send); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 2 {
		t.Fatalf("hold/release emitted %d packets", len(wire))
	}
	if wire[0].PacketID != packet_ids.C2SUseItemID || !bytes.Equal(wire[0].Data, []byte{1, 1, 0x42, 0xb4, 0, 0, 0xc1, 0xf0, 0, 0}) {
		t.Fatalf("wrong hand/sequence/current-aim wire packet: %+v", wire[0])
	}
	if wire[1].PacketID != packet_ids.C2SPlayerActionID || !bytes.Equal(wire[1].Data, []byte{6, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf("release did not use action6/origin/down/sequence0: %+v", wire[1])
	}
	if me.UsingItem() || inv.GetOffhand().Count != 1 {
		t.Fatal("sending use fabricated authoritative item use or consumption")
	}
}

func TestItemUseRejectsEmptyHandAndInvalidAimBeforeSending(t *testing.T) {
	c, me, _ := itemUseClient(t)
	sent := false
	send := func(jp.Packet) error { sent = true; return nil }
	if _, err := BeginItemUse(c, 0, send); err == nil {
		t.Fatal("empty main hand accepted")
	}
	if _, err := BeginItemUse(c, 2, send); err == nil {
		t.Fatal("invalid hand accepted")
	}
	me.SetRotation(0, 91)
	if _, err := BeginItemUse(c, 1, send); err == nil {
		t.Fatal("out-of-range pitch accepted")
	}
	me.SetRotation(math.Inf(1), 0)
	if _, err := BeginItemUse(c, 1, send); err == nil {
		t.Fatal("non-finite aim accepted")
	}
	if sent {
		t.Fatal("invalid request reached transport")
	}
}
