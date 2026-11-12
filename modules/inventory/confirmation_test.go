package inventory

import (
	"context"
	"errors"
	"testing"

	"github.com/go-mclib/data/items"
	"github.com/go-mclib/data/packets"
	"github.com/go-mclib/data/slots"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

func TestRequestedHotbarSwapWaitsForServerContents(t *testing.T) {
	m := New()
	testInventorySession(t, m, func(context.Context, jp.Packet) error { return nil })
	stone := items.ID("minecraft:stone")
	contents := make([]ns.Slot, slots.PlayerInvSize)
	contents[slots.PlayerMainStart] = ns.NewSlot(ns.VarInt(stone), 12)
	applyInventoryPacket(t, m, &packets.S2CContainerSetContent{Slots: contents})
	send := func(p jp.Packet) error { _, err := jp.ToWire(p); return err }
	if err := m.RequestHotbarSwap(slots.PlayerMainStart, 0, send); err != nil {
		t.Fatal(err)
	}
	if !m.GetSlot(slots.PlayerHotbarStart).IsEmpty() || m.GetSlot(slots.PlayerMainStart).Count != 12 {
		t.Fatal("packet submission fabricated an inventory swap")
	}
	if err := m.RequestHotbarSwap(slots.PlayerMainStart, 1, send); err == nil {
		t.Fatal("unconfirmed request allowed a competing click")
	}
	update, err := jp.ToWire(&packets.S2CSetPlayerInventory{Slot: 0, SlotData: ns.NewSlot(ns.VarInt(stone), 12)})
	if err != nil {
		t.Fatal(err)
	}
	m.handleSetPlayerInventory(update)
	if held := m.GetSlot(slots.PlayerHotbarStart); held.IsEmpty() || held.ID != stone || held.Count != 12 {
		t.Fatal("authoritative server hotbar contents were not applied")
	}
	contents[slots.PlayerMainStart], contents[slots.PlayerHotbarStart] = ns.EmptySlot(), contents[slots.PlayerMainStart]
	applyInventoryPacket(t, m, &packets.S2CContainerSetContent{StateID: 1, Slots: contents})
	if err := m.SelectHotbar(0, send); err != nil {
		t.Fatalf("authoritative resync did not release transaction gate: %v", err)
	}
}

func TestFailedSelectionPreservesHeldSlot(t *testing.T) {
	m := New()
	testInventorySession(t, m, func(context.Context, jp.Packet) error { return nil })
	m.heldSlot = 2
	failure := errors.New("session cancelled")
	if err := m.SelectHotbar(5, func(jp.Packet) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("transport error lost: %v", err)
	}
	if m.HeldSlotIndex() != 2 {
		t.Fatal("failed selection changed held item locally")
	}
}

func TestQueuedSelectionSurvivesReturnButNotSessionReset(t *testing.T) {
	m := New()
	testInventorySession(t, m, func(context.Context, jp.Packet) error { return nil })
	var queued jp.Packet
	if err := m.SelectHotbar(4, func(p jp.Packet) error { queued = p; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := jp.ToWire(queued); err != nil {
		t.Fatalf("helper return invalidated accepted queued selection: %v", err)
	}
	m.Reset()
	if _, err := jp.ToWire(queued); err == nil {
		t.Fatal("queued selection survived session reset")
	}
}
