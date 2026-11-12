package inventory

import (
	"testing"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/packets"
	"github.com/go-mclib/data/slots"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

func TestRawSlotsDoNotAliasComponents(t *testing.T) {
	m := New()
	raw := ns.NewSlot(1, 1)
	raw.Components.Add = []ns.RawSlotComponent{{ID: 2, Data: []byte{3}}}
	raw.Components.Remove = []ns.VarInt{4}
	m.slots[9].raw = raw
	m.cursor.raw = raw
	slots, _, cursor := m.RawSlots()
	slots[9].Components.Add[0].Data[0] = 99
	slots[9].Components.Remove[0] = 99
	cursor.Components.Add[0].Data[0] = 88
	cursor.Components.Remove[0] = 88
	view := m.Snapshot()
	view.Slots[9].Components.Add[0].Data[0] = 77
	view.Cursor.Components.Remove[0] = 66
	fresh, _, freshCursor := m.RawSlots()
	if fresh[9].Components.Add[0].Data[0] != 3 || fresh[9].Components.Remove[0] != 4 || freshCursor.Components.Add[0].Data[0] != 3 || freshCursor.Components.Remove[0] != 4 {
		t.Fatal("raw snapshot aliases stored slot or cursor components")
	}
}

func TestKnownSlotsAndContentReplacedSignal(t *testing.T) {
	c := client.New("unused", "test", false)
	client.NewSimulator(c).SetState(jp.StatePlay)
	m := New()
	c.Register(m)
	var replaced []int32
	m.OnContentReplaced(func(windowID int32) { replaced = append(replaced, windowID) })
	deliver := func(p jp.Packet) {
		t.Helper()
		wire, err := jp.ToWire(p)
		if err != nil {
			t.Fatal(err)
		}
		m.HandlePacket(wire)
	}
	if m.Snapshot().Known != [slots.PlayerInvSize]bool{} {
		t.Fatal("slots known before any content")
	}
	deliver(&packets.S2CContainerSetSlot{WindowID: 0, Slot: slots.PlayerHotbarStart})
	known := m.Snapshot().Known
	if !known[slots.PlayerHotbarStart] || known[slots.PlayerHotbarStart+1] || len(replaced) != 0 {
		t.Fatalf("set-slot: known=%v replaced=%v", known, replaced)
	}
	deliver(&packets.S2CContainerSetContent{WindowID: 0, Slots: make([]ns.Slot, slots.PlayerInvSize)})
	if m.Snapshot().Known != func() (all [slots.PlayerInvSize]bool) {
		for i := range all {
			all[i] = true
		}
		return
	}() || len(replaced) != 1 || replaced[0] != 0 {
		t.Fatalf("set-content: replaced=%v", replaced)
	}
	deliver(&packets.S2CRespawn{})
	if m.Snapshot().Known != [slots.PlayerInvSize]bool{} {
		t.Fatal("respawn kept known slots")
	}
}
