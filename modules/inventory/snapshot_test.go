package inventory

import (
	"testing"

	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
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
