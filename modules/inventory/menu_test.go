package inventory

import (
	"context"
	"testing"
	"time"

	"github.com/go-mclib/data/items"
	"github.com/go-mclib/data/packets"
	"github.com/go-mclib/data/slots"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

func TestActiveContainerMapsOnlyItsPlayerSection(t *testing.T) {
	m := New()
	applyInventoryPacket(t, m, &packets.S2COpenScreen{WindowID: 7, WindowType: ns.VarInt(MenuGeneric9x3)})
	if m.Snapshot().Ready {
		t.Fatal("open screen invented initialized menu contents")
	}
	contents := make([]ns.Slot, 27+36)
	stone := ns.NewSlot(ns.VarInt(items.ID("minecraft:stone")), 1)
	contents[27] = stone
	contents[62] = stone
	applyInventoryPacket(t, m, &packets.S2CContainerSetContent{WindowID: 7, StateID: 9, Slots: contents, CarriedItem: stone})
	if s := m.Snapshot(); !s.Ready || len(s.Slots) != 63 || s.ContainerSlots != 27 || m.GetSlot(9).Count != 1 || m.GetSlot(44).Count != 1 {
		t.Fatal("wrong active menu/player slot mapping")
	}
	// Out-of-range trailing slot 63 is not player offhand 45.
	applyInventoryPacket(t, m, &packets.S2CContainerSetSlot{WindowID: 7, StateID: 99, Slot: 63, SlotData: stone})
	if s := m.Snapshot(); s.StateID != 9 || !m.Offhand().IsEmpty() {
		t.Fatal("out-of-range menu update corrupted offhand/state ID")
	}
	player := make([]ns.Slot, slots.PlayerInvSize)
	applyInventoryPacket(t, m, &packets.S2CContainerSetContent{WindowID: 0, StateID: 4, Slots: player})
	if s := m.Snapshot(); s.Cursor.Count != 1 || s.StateID != 9 {
		t.Fatal("background player menu overwrote active cursor or state ID")
	}
	applyInventoryPacket(t, m, &packets.S2CSetCursorItem{})
	if s := m.Snapshot(); !s.Cursor.IsEmpty() || s.StateID != 9 {
		t.Fatal("cursor packet changed menu state ID or was ignored")
	}
	closes := 0
	m.OnContainerClose(func() { closes++ })
	applyInventoryPacket(t, m, &packets.S2CContainerClose{WindowID: 6})
	if closes != 0 || m.Snapshot().WindowID != 7 {
		t.Fatal("stale close replaced current window")
	}
	applyInventoryPacket(t, m, &packets.S2CContainerClose{WindowID: 7})
	if closes != 1 || m.Snapshot().WindowID != 0 {
		t.Fatal("matching close not applied")
	}
}

func TestLecternSnapshotHasNoInventedPlayerSlots(t *testing.T) {
	m := New()
	applyInventoryPacket(t, m, &packets.S2COpenScreen{WindowID: 1, WindowType: ns.VarInt(MenuLectern)})
	applyInventoryPacket(t, m, &packets.S2CContainerSetContent{WindowID: 1, Slots: []ns.Slot{ns.NewSlot(ns.VarInt(items.ID("minecraft:written_book")), 1)}})
	s := m.Snapshot()
	if !s.Ready || len(s.Slots) != 1 || s.ContainerSlots != 1 {
		t.Fatalf("lectern view fabricated player section: %+v", s)
	}
}

func TestCloseRequiresPlayerMenuReadback(t *testing.T) {
	s := newStorageTestServer(t, 1)
	s.publish()
	outgoing := make(chan jp.Packet, 4)
	s.m.session.send = func(_ context.Context, packet jp.Packet) error { outgoing <- packet; return nil }
	done := make(chan inventoryOutcome, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		result, err := s.m.Execute(ctx, Operation{Kind: OpClose, WindowID: 1})
		done <- inventoryOutcome{result, err}
	}()
	select {
	case <-outgoing:
	case <-time.After(time.Second):
		t.Fatal("no close request")
	}
	select {
	case <-outgoing:
	case <-time.After(time.Second):
		t.Fatal("no player-menu readback request")
	}
	select {
	case <-done:
		t.Fatal("close confirmed without server readback")
	default:
	}
	player := make([]ns.Slot, slots.PlayerInvSize)
	player[9] = ns.NewSlot(ns.VarInt(items.ID("minecraft:stone")), 3)
	applyInventoryPacket(t, s.m, &packets.S2CContainerSetContent{WindowID: 0, StateID: 2, Slots: player})
	outcome := awaitInventoryOutcome(t, done)
	if outcome.err != nil || outcome.result.Status != StatusConfirmed || outcome.result.Snapshot.WindowID != 0 || !outcome.result.Snapshot.Ready {
		t.Fatalf("close not confirmed after readback: %v", outcome.err)
	}
}

func TestQueuedClickCannotSerializeAfterSessionReset(t *testing.T) {
	m := New()
	outgoing := make(chan jp.Packet, 1)
	testInventorySession(t, m, func(_ context.Context, packet jp.Packet) error { outgoing <- packet; return nil })
	contents := make([]ns.Slot, slots.PlayerInvSize)
	contents[9] = ns.NewSlot(ns.VarInt(items.ID("minecraft:stone")), 1)
	applyInventoryPacket(t, m, &packets.S2CContainerSetContent{Slots: contents})
	done := make(chan inventoryOutcome, 1)
	go func() {
		result, err := m.Execute(t.Context(), Operation{Kind: OpDrop, From: 9})
		done <- inventoryOutcome{result, err}
	}()
	var queued jp.Packet
	select {
	case queued = <-outgoing:
	case <-time.After(time.Second):
		t.Fatal("no queued click")
	}
	m.Reset()
	if _, err := jp.ToWire(queued); err == nil {
		t.Fatal("old click survived session reset at serialization boundary")
	}
	if result := awaitInventoryOutcome(t, done); result.err == nil || result.result.Status == StatusConfirmed {
		t.Fatal("reset operation reported success")
	}
}

func TestDropRequestedCountLeavesRemainder(t *testing.T) {
	s := newStorageTestServer(t, 0)
	s.slots[9] = ns.NewSlot(ns.VarInt(items.ID("minecraft:stone")), 7)
	s.publish()
	r, err := s.m.Execute(t.Context(), Operation{Kind: OpDrop, From: 9, Count: 3})
	if err != nil || r.Snapshot.Slots[9].Count != 4 || !r.Snapshot.Cursor.IsEmpty() {
		t.Fatalf("partial drop lost remainder: %v", err)
	}
	r, err = s.m.Execute(t.Context(), Operation{Kind: OpDrop, From: 9})
	if err != nil || !r.Snapshot.Slots[9].IsEmpty() {
		t.Fatalf("whole-stack drop left remainder: %v", err)
	}
}

func TestChestTransferAndSortPreservePlayerSection(t *testing.T) {
	s := newStorageTestServer(t, 3)
	stone, dirt := ns.VarInt(items.ID("minecraft:stone")), ns.VarInt(items.ID("minecraft:dirt"))
	s.slots[0], s.slots[2], s.slots[27] = ns.NewSlot(stone, 5), ns.NewSlot(dirt, 2), ns.NewSlot(dirt, 7)
	s.publish()
	r, err := s.m.Execute(t.Context(), Operation{Kind: OpMove, WindowID: 3, From: 0, To: 28, Count: 2})
	if err != nil || r.Snapshot.Slots[0].Count != 3 || r.Snapshot.Slots[28].Count != 2 || s.m.GetSlot(10).Count != 2 {
		t.Fatalf("container-to-player transfer used wrong protocol indices: %v", err)
	}
	r, err = s.m.Execute(t.Context(), Operation{Kind: OpSort, WindowID: 3})
	if err != nil || r.Snapshot.Slots[27].ItemID != dirt || r.Snapshot.Slots[27].Count != 7 || r.Snapshot.Slots[28].ItemID != stone || r.Snapshot.Slots[28].Count != 2 {
		t.Fatalf("container sort touched appended player inventory: %v", err)
	}
	if err := s.m.ContainerShiftClick(28); err != nil {
		t.Fatalf("player-to-container transfer failed: %v", err)
	}
	snapshot := s.m.Snapshot()
	if !snapshot.Slots[28].IsEmpty() || !s.m.GetSlot(10).IsEmpty() {
		t.Fatal("confirmed transfer left source player slot populated")
	}
	count := 0
	for _, slot := range snapshot.Slots[:27] {
		if slot.ItemID == stone && slot.Count > 0 {
			count += int(slot.Count)
		}
	}
	if count != 5 || !snapshot.Cursor.IsEmpty() {
		t.Fatal("chest round trip lost items or stranded cursor")
	}
}
