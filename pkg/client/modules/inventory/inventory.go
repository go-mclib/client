package inventory

import (
	"sync"

	"github.com/go-mclib/client/pkg/client"
	"github.com/go-mclib/data/pkg/data/items"
	"github.com/go-mclib/data/pkg/data/packet_ids"
	"github.com/go-mclib/data/pkg/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

type slotEntry struct {
	raw  ns.Slot
	item *items.ItemStack
}

type Module struct {
	client *client.Client
	mu     sync.RWMutex

	slots    [TotalSlots]slotEntry
	heldSlot int
	stateID  int32
	cursor   slotEntry

	onSlotUpdate     []func(index int, item *items.ItemStack)
	onHeldSlotChange []func(slot int)
}

func New() *Module { return &Module{} }

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) { m.client = c }

func (m *Module) Reset() {
	m.mu.Lock()
	m.slots = [TotalSlots]slotEntry{}
	m.heldSlot = 0
	m.stateID = 0
	m.cursor = slotEntry{}
	m.mu.Unlock()
}

func From(c *client.Client) *Module {
	mod := c.Module(ModuleName)
	if mod == nil {
		return nil
	}
	return mod.(*Module)
}

// events

func (m *Module) OnSlotUpdate(cb func(index int, item *items.ItemStack)) {
	m.onSlotUpdate = append(m.onSlotUpdate, cb)
}

func (m *Module) OnHeldSlotChange(cb func(slot int)) {
	m.onHeldSlotChange = append(m.onHeldSlotChange, cb)
}

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	switch pkt.PacketID {
	case packet_ids.S2CContainerSetContentID:
		m.handleContainerSetContent(pkt)
	case packet_ids.S2CContainerSetSlotID:
		m.handleContainerSetSlot(pkt)
	case packet_ids.S2CSetHeldSlotID:
		m.handleSetHeldSlot(pkt)
	case packet_ids.S2CSetPlayerInventoryID:
		m.handleSetPlayerInventory(pkt)
	}
}

func (m *Module) handleContainerSetContent(pkt *jp.WirePacket) {
	var d packets.S2CContainerSetContent
	if err := pkt.ReadInto(&d); err != nil {
		m.client.Logger.Println("inventory: failed to parse container set content:", err)
		return
	}

	// only handle player inventory (container 0)
	if d.WindowId != 0 {
		return
	}

	m.mu.Lock()
	m.stateID = int32(d.StateId)
	count := min(len(d.Slots), TotalSlots)
	for i := 0; i < count; i++ {
		m.slots[i] = decodeSlotEntry(d.Slots[i])
	}
	// clear remaining slots if server sent fewer
	for i := count; i < TotalSlots; i++ {
		m.slots[i] = slotEntry{}
	}
	m.cursor = decodeSlotEntry(d.CarriedItem)
	m.mu.Unlock()

	for i := 0; i < count; i++ {
		for _, cb := range m.onSlotUpdate {
			cb(i, m.slots[i].item)
		}
	}
}

func (m *Module) handleContainerSetSlot(pkt *jp.WirePacket) {
	var d packets.S2CContainerSetSlot
	if err := pkt.ReadInto(&d); err != nil {
		m.client.Logger.Println("inventory: failed to parse container set slot:", err)
		return
	}

	// WindowId -1 with Slot -1 means cursor-only update
	if int32(d.WindowId) == -1 && int16(d.Slot) == -1 {
		m.mu.Lock()
		m.stateID = int32(d.StateId)
		m.cursor = decodeSlotEntry(d.SlotData)
		m.mu.Unlock()
		return
	}

	// only handle player inventory
	if d.WindowId != 0 {
		return
	}

	idx := int(d.Slot)
	if idx < 0 || idx >= TotalSlots {
		return
	}

	entry := decodeSlotEntry(d.SlotData)

	m.mu.Lock()
	m.stateID = int32(d.StateId)
	m.slots[idx] = entry
	m.mu.Unlock()

	for _, cb := range m.onSlotUpdate {
		cb(idx, entry.item)
	}
}

func (m *Module) handleSetHeldSlot(pkt *jp.WirePacket) {
	var d packets.S2CSetHeldSlot
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	slot := int(d.Slot)
	if slot < 0 || slot > 8 {
		return
	}

	m.mu.Lock()
	m.heldSlot = slot
	m.mu.Unlock()

	for _, cb := range m.onHeldSlotChange {
		cb(slot)
	}
}

func (m *Module) handleSetPlayerInventory(pkt *jp.WirePacket) {
	var d packets.S2CSetPlayerInventory
	if err := pkt.ReadInto(&d); err != nil {
		m.client.Logger.Println("inventory: failed to parse set player inventory:", err)
		return
	}

	containerIdx := playerInvToContainer(int(d.Slot))
	if containerIdx < 0 || containerIdx >= TotalSlots {
		return
	}

	entry := decodeSlotEntry(d.SlotData)

	m.mu.Lock()
	m.slots[containerIdx] = entry
	m.mu.Unlock()

	for _, cb := range m.onSlotUpdate {
		cb(containerIdx, entry.item)
	}
}

func decodeSlotEntry(raw ns.Slot) slotEntry {
	stack, err := items.FromSlot(raw)
	if err != nil {
		stack = items.EmptyStack()
	}
	return slotEntry{raw: raw, item: stack}
}
