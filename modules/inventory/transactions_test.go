package inventory

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/go-mclib/data/items"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func testInventorySession(t *testing.T, m *Module, send func(context.Context, jp.Packet) error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	m.session = &inventorySession{ctx: ctx, cancel: cancel, send: send}
	t.Cleanup(m.Reset)
}

func applyInventoryPacket(t *testing.T, m *Module, packet jp.Packet) {
	t.Helper()
	wire, err := jp.ToWire(packet)
	if err != nil {
		t.Fatal(err)
	}
	switch packet.(type) {
	case *packets.S2CContainerSetContent:
		m.handleContainerSetContent(wire)
	case *packets.S2CContainerSetSlot:
		m.handleContainerSetSlot(wire)
	case *packets.S2CSetCursorItem:
		m.handleSetCursorItem(wire)
	case *packets.S2COpenScreen:
		m.handleOpenScreen(wire)
	case *packets.S2CContainerClose:
		m.handleContainerClose(wire)
	default:
		t.Fatalf("unhandled server packet %T", packet)
	}
}

func decodeInventoryClick(t *testing.T, packet jp.Packet) packets.C2SContainerClick {
	t.Helper()
	wire, err := jp.ToWire(packet)
	if err != nil {
		t.Fatal(err)
	}
	var click packets.C2SContainerClick
	if err := wire.ReadInto(&click); err != nil {
		t.Fatal(err)
	}
	return click
}

type inventoryOutcome struct {
	result OperationResult
	err    error
}

func awaitInventoryOutcome(t *testing.T, ch <-chan inventoryOutcome) inventoryOutcome {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(time.Second):
		t.Fatal("inventory operation did not finish")
		return inventoryOutcome{}
	}
}

func TestExecuteRequiresFullAuthoritativeMutation(t *testing.T) {
	m := New()
	outgoing := make(chan jp.Packet, 4)
	testInventorySession(t, m, func(_ context.Context, p jp.Packet) error { outgoing <- p; return nil })
	slots := make([]ns.Slot, TotalSlots)
	slots[9] = ns.NewSlot(ns.VarInt(items.ItemID("minecraft:stone")), 9)
	applyInventoryPacket(t, m, &packets.S2CContainerSetContent{WindowId: 0, StateId: 10, Slots: slots})
	done := make(chan inventoryOutcome, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		r, err := m.Execute(ctx, Operation{Kind: "move", From: 9, To: 10})
		done <- inventoryOutcome{r, err}
	}()
	select {
	case <-outgoing:
	case <-time.After(time.Second):
		t.Fatal("no pickup")
	}
	if s := m.Snapshot(); s.Slots[9].Count != 9 || !s.Cursor.IsEmpty() {
		t.Fatal("packet submission predicted local contents")
	}
	// A partial slot/cursor update must not allow the next click yet.
	applyInventoryPacket(t, m, &packets.S2CContainerSetSlot{WindowId: 0, StateId: 11, Slot: 9})
	cursor := slots[9]
	applyInventoryPacket(t, m, &packets.S2CSetCursorItem{CarriedItem: cursor})
	select {
	case <-outgoing:
		t.Fatal("partial update advanced transaction")
	default:
	}
	slots[9] = ns.EmptySlot()
	applyInventoryPacket(t, m, &packets.S2CContainerSetContent{WindowId: 0, StateId: 12, Slots: slots, CarriedItem: cursor})
	select {
	case <-outgoing:
	case <-time.After(time.Second):
		t.Fatal("no placement after resync")
	}
	select {
	case <-done:
		t.Fatal("placement was labeled confirmed before server update")
	default:
	}
	slots[10] = cursor
	applyInventoryPacket(t, m, &packets.S2CContainerSetContent{WindowId: 0, StateId: 13, Slots: slots})
	outcome := awaitInventoryOutcome(t, done)
	if outcome.err != nil || outcome.result.Status != "confirmed" || outcome.result.Snapshot.Slots[10].Count != 9 || !outcome.result.Snapshot.Cursor.IsEmpty() {
		t.Fatalf("move not confirmed: %+v, %v", outcome.result, outcome.err)
	}
}

// This small storage-menu server implements wire click semantics, not transaction
// expectations. It maintains its own stacks and responds only via actual packets.
// It deliberately does not share inventory mutation helpers with the SDK.
type storageTestServer struct {
	t          *testing.T
	m          *Module
	window     int32
	slots      []ns.Slot
	cursor     ns.Slot
	state      ns.VarInt
	clicks     int
	afterClick func()
}

func newStorageTestServer(t *testing.T, window int32) *storageTestServer {
	m := New()
	s := &storageTestServer{t: t, m: m, window: window, slots: make([]ns.Slot, TotalSlots)}
	if window != 0 {
		s.slots = make([]ns.Slot, 27+36)
		applyInventoryPacket(t, m, &packets.S2COpenScreen{WindowId: ns.VarInt(window), WindowType: ns.VarInt(MenuGeneric9x3)})
	}
	testInventorySession(t, m, s.send)
	return s
}

func (s *storageTestServer) publish() {
	s.state++
	applyInventoryPacket(s.t, s.m, &packets.S2CContainerSetContent{WindowId: ns.VarInt(s.window), StateId: s.state, Slots: s.slots, CarriedItem: s.cursor})
}

func (s *storageTestServer) send(ctx context.Context, p jp.Packet) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c := decodeInventoryClick(s.t, p)
	s.clicks++
	if int32(c.WindowId) != s.window {
		return errors.New("wrong server window")
	}
	i := int(c.Slot)
	if i >= 0 {
		x := &s.slots[i]
		switch c.Mode {
		case 0:
			if s.cursor.Count == 0 {
				n := x.Count
				if c.Button == 1 {
					n = (n + 1) / 2
				}
				s.cursor = cloneRawSlot(*x)
				s.cursor.Count = n
				x.Count -= n
			} else if x.Count == 0 || (x.ItemID == s.cursor.ItemID && reflect.DeepEqual(x.Components, s.cursor.Components)) {
				n := min(s.cursor.Count, ns.VarInt(items.SlotMaxStackSize(s.cursor))-x.Count)
				if c.Button == 1 {
					n = min(n, 1)
				}
				previous := x.Count
				*x = cloneRawSlot(s.cursor)
				x.Count = previous + n
				s.cursor.Count -= n
			} else {
				*x, s.cursor = s.cursor, *x
			}
		case 2:
			base := 36
			if s.window != 0 {
				base = len(s.slots) - 9
			}
			s.slots[i], s.slots[base+int(c.Button)] = s.slots[base+int(c.Button)], s.slots[i]
		case 4:
			if c.Button == 1 {
				x.Count = 0
			} else {
				x.Count--
			}
		default:
			return errors.New("unsupported test-server click")
		}
	} else if i == -999 {
		if c.Button == 0 {
			s.cursor.Count = 0
		} else {
			s.cursor.Count--
		}
	}
	s.publish()
	if s.afterClick != nil {
		s.afterClick()
	}
	return nil
}

func TestSplitMergeCountsAndComponentIdentity(t *testing.T) {
	s := newStorageTestServer(t, 0)
	stone := ns.VarInt(items.ItemID("minecraft:stone"))
	s.slots[9] = ns.NewSlot(stone, 9)
	// A custom stack limit must survive splitting and govern merging.
	s.slots[9].Components.Add = []ns.RawSlotComponent{{ID: items.ComponentMaxStackSize, Data: []byte{9}}}
	s.publish()
	r, err := s.m.Execute(context.Background(), Operation{Kind: "split", From: 9, To: 10, Count: 4})
	if err != nil || r.Snapshot.Slots[9].Count != 5 || r.Snapshot.Slots[10].Count != 4 || !r.Snapshot.Cursor.IsEmpty() {
		t.Fatalf("odd split lost items or left cursor: %+v %v", r, err)
	}
	if !reflect.DeepEqual(r.Snapshot.Slots[9].Components, r.Snapshot.Slots[10].Components) {
		t.Fatal("split lost component patch")
	}
	// A component-distinct stack with the same item ID must not merge.
	s.slots[11] = ns.NewSlot(stone, 1)
	s.slots[11].Components.Add = []ns.RawSlotComponent{{ID: items.ComponentMaxStackSize, Data: []byte{8}}}
	s.publish()
	before := s.clicks
	if _, err := s.m.Execute(context.Background(), Operation{Kind: "move", From: 10, To: 11}); err == nil || s.clicks != before {
		t.Fatal("component-distinct move submitted a click")
	}
	r, err = s.m.Execute(context.Background(), Operation{Kind: "move", From: 10, To: 9})
	if err != nil || r.Snapshot.Slots[9].Count != 9 || !r.Snapshot.Slots[10].IsEmpty() {
		t.Fatalf("merge at custom stack limit failed: %v", err)
	}
	s.slots[10] = cloneRawSlot(s.slots[9])
	s.slots[10].Count = 1
	s.publish()
	before = s.clicks
	if _, err := s.m.Execute(context.Background(), Operation{Kind: "move", From: 10, To: 9}); err == nil || s.clicks != before {
		t.Fatal("over-limit merge submitted a click")
	}
}

func TestSwapSameItemCountsAndDistinctComponents(t *testing.T) {
	s := newStorageTestServer(t, 0)
	stone := ns.VarInt(items.ItemID("minecraft:stone"))
	s.slots[9], s.slots[10] = ns.NewSlot(stone, 9), ns.NewSlot(stone, 5)
	s.publish()
	r, err := s.m.Execute(context.Background(), Operation{Kind: "swap", From: 9, To: 10})
	if err != nil || r.Snapshot.Slots[9].Count != 5 || r.Snapshot.Slots[10].Count != 9 {
		t.Fatalf("equal-identity swap merged instead: %v", err)
	}
	s.slots[10].Components.Remove = []ns.VarInt{items.ComponentCustomName}
	s.publish()
	// Compare the consumer-visible stacks on both sides of the transaction.
	// Slot decoding canonicalizes nil component slices to empty slices.
	before := s.m.Snapshot()
	a, b := before.Slots[9], before.Slots[10]
	r, err = s.m.Execute(context.Background(), Operation{Kind: "swap", From: 9, To: 10})
	if err != nil || !reflect.DeepEqual(r.Snapshot.Slots[9], b) || !reflect.DeepEqual(r.Snapshot.Slots[10], a) || !r.Snapshot.Cursor.IsEmpty() {
		t.Fatalf("component-distinct swap lost identity: %v", err)
	}
}

func TestCanceledMovePreservesCursorAndAllowsExplicitRecovery(t *testing.T) {
	s := newStorageTestServer(t, 0)
	s.slots[9] = ns.NewSlot(ns.VarInt(items.ItemID("minecraft:stone")), 7)
	s.publish()
	ctx, cancel := context.WithCancel(context.Background())
	s.afterClick = cancel
	r, err := s.m.Execute(ctx, Operation{Kind: "move", From: 9, To: 10})
	if !errors.Is(err, context.Canceled) || s.clicks != 1 || r.Snapshot.Cursor.Count != 7 || !r.Snapshot.Slots[10].IsEmpty() {
		t.Fatalf("cancellation sent later click or lost cursor: %+v %v", r, err)
	}
	s.afterClick = nil
	r, err = s.m.Execute(context.Background(), Operation{Kind: "move", From: -1, To: 9})
	if err != nil || r.Snapshot.Slots[9].Count != 7 || !r.Snapshot.Cursor.IsEmpty() {
		t.Fatalf("explicit cursor recovery failed: %v", err)
	}
}

func TestWindowReplacementAndStaleRevisionPreventClicks(t *testing.T) {
	s := newStorageTestServer(t, 1)
	s.slots[0] = ns.NewSlot(ns.VarInt(items.ItemID("minecraft:stone")), 2)
	s.publish()
	old := s.m.Snapshot()
	s.publish()
	if _, err := s.m.Execute(context.Background(), Operation{Kind: "drop", WindowID: 1, Revision: old.Revision, From: 0}); err == nil || s.clicks != 0 {
		t.Fatal("stale revision submitted destructive click")
	}
	s.afterClick = func() {
		// Reuse the numeric window ID: epoch, not just WindowID, must fence work.
		applyInventoryPacket(t, s.m, &packets.S2COpenScreen{WindowId: 1, WindowType: ns.VarInt(MenuGeneric9x3)})
	}
	r, err := s.m.Execute(context.Background(), Operation{Kind: "move", WindowID: 1, From: 0, To: 1})
	if err == nil || s.clicks != 1 || r.Status == "confirmed" {
		t.Fatal("replaced view accepted stale transaction")
	}
}

func TestSortOnlyStorageAndKeepComponentDistinctStacks(t *testing.T) {
	s := newStorageTestServer(t, 0)
	stone, dirt := ns.VarInt(items.ItemID("minecraft:stone")), ns.VarInt(items.ItemID("minecraft:dirt"))
	s.slots[0] = ns.NewSlot(dirt, 1)
	s.slots[5] = ns.NewSlot(ns.VarInt(items.ItemID("minecraft:diamond_helmet")), 1)
	s.slots[45] = ns.NewSlot(dirt, 2)
	s.slots[9], s.slots[11], s.slots[44] = ns.NewSlot(stone, 40), ns.NewSlot(stone, 30), ns.NewSlot(dirt, 3)
	s.slots[10] = ns.NewSlot(stone, 2)
	s.slots[10].Components.Remove = []ns.VarInt{items.ComponentCustomName}
	s.publish()
	r, err := s.m.Execute(context.Background(), Operation{Kind: "sort"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Snapshot.Slots[0].Count != 1 || r.Snapshot.Slots[5].ItemID != ns.VarInt(items.ItemID("minecraft:diamond_helmet")) || r.Snapshot.Slots[45].Count != 2 {
		t.Fatal("sort touched non-storage slots")
	}
	plain, patched, dirtCount, plainStacks := 0, 0, 0, 0
	for _, slot := range r.Snapshot.Slots[9:45] {
		if slot.IsEmpty() {
			continue
		}
		if slot.ItemID == dirt {
			dirtCount += int(slot.Count)
		} else if len(slot.Components.Remove) != 0 {
			patched += int(slot.Count)
		} else {
			plain += int(slot.Count)
			plainStacks++
		}
	}
	if plain != 70 || plainStacks != 2 || patched != 2 || dirtCount != 3 || !r.Snapshot.Cursor.IsEmpty() {
		t.Fatalf("sort merged identities or lost items: plain=%d stacks=%d patched=%d dirt=%d", plain, plainStacks, patched, dirtCount)
	}
}

func TestServerRejectionNeverConfirmsDrop(t *testing.T) {
	m := New()
	slots := make([]ns.Slot, TotalSlots)
	slots[9] = ns.NewSlot(ns.VarInt(items.ItemID("minecraft:stone")), 6)
	testInventorySession(t, m, func(_ context.Context, p jp.Packet) error {
		decodeInventoryClick(t, p)
		applyInventoryPacket(t, m, &packets.S2CContainerSetContent{WindowId: 0, StateId: 2, Slots: slots})
		return nil
	})
	applyInventoryPacket(t, m, &packets.S2CContainerSetContent{WindowId: 0, StateId: 1, Slots: slots})
	r, err := m.Execute(context.Background(), Operation{Kind: "drop", From: 9, Count: 3})
	if err == nil || r.Status == "confirmed" || r.Clicks != 1 || r.Snapshot.Slots[9].Count != 6 {
		t.Fatalf("server rejection treated as success: %+v %v", r, err)
	}
}
