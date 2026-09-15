package inventory

import (
	"errors"
	"testing"

	"github.com/go-mclib/data/items"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func TestRequestedHotbarSwapWaitsForServerContents(t *testing.T) {
	m := New()
	stone := items.ItemID("minecraft:stone")
	m.slots[SlotMainStart] = decodeSlotEntry(ns.NewSlot(ns.VarInt(stone), 12))
	var outgoing jp.Packet
	if err := m.RequestHotbarSwap(SlotMainStart, 0, func(p jp.Packet) error { outgoing = p; return nil }); err != nil {
		t.Fatal(err)
	}
	if !m.GetSlot(SlotHotbarStart).IsEmpty() || m.GetSlot(SlotMainStart).Count != 12 {
		t.Fatal("packet submission fabricated an inventory swap")
	}
	if outgoing == nil {
		t.Fatal("inventory request emitted no protocol action")
	}
	update, err := jp.ToWire(&packets.S2CSetPlayerInventory{Slot: 0, SlotData: ns.NewSlot(ns.VarInt(stone), 12)})
	if err != nil {
		t.Fatal(err)
	}
	m.handleSetPlayerInventory(update)
	if held := m.GetSlot(SlotHotbarStart); held.IsEmpty() || held.ID != stone || held.Count != 12 {
		t.Fatal("authoritative server hotbar contents were not applied")
	}
}

func TestFailedSelectionPreservesHeldSlot(t *testing.T) {
	m := New()
	m.heldSlot = 2
	failure := errors.New("session cancelled")
	if err := m.SelectHotbar(5, func(jp.Packet) error { return failure }); err != failure {
		t.Fatalf("transport error lost: %v", err)
	}
	if m.HeldSlotIndex() != 2 {
		t.Fatal("failed selection changed held item locally")
	}
}
