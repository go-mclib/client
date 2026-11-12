package inventory

import (
	"sync"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/items"
	"github.com/go-mclib/data/packetids"
	"github.com/go-mclib/data/packets"
	"github.com/go-mclib/data/slots"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

type slotEntry struct {
	raw  ns.Slot
	item *items.Stack
}

type Module struct {
	client *client.Client
	mu     sync.RWMutex

	slots    [slots.PlayerInvSize]slotEntry
	known    [slots.PlayerInvSize]bool // slot contents received since reset/respawn
	heldSlot int
	stateID  int32
	cursor   slotEntry

	container       *containerState // nil when no container is open
	ready           bool
	revision        uint64
	epoch           uint64
	contentRevision uint64
	changed         chan struct{}
	transactions    chan struct{}
	session         *inventorySession
	pendingRequest  *pendingInventoryRequest

	onSlotUpdate     []func(index int, item *items.Stack)
	onHeldSlotChange []func(slot int)
	onContainerOpen  []func(windowID int32, menuType MenuType, title string)
	onContainerClose []func()
	onContentReplace []func(windowID int32)
}

func New() *Module { return &Module{} }

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c
	c.OnTransfer(m.Reset)
	c.OnDisconnect(m.Reset)
	c.OnPlay(m.startSession)
}

func (m *Module) Reset() {
	m.mu.Lock()
	if m.session != nil {
		m.session.cancel()
		m.session = nil
	}
	m.epoch++
	m.ready = false
	m.slots = [slots.PlayerInvSize]slotEntry{}
	m.known = [slots.PlayerInvSize]bool{}
	m.heldSlot = 0
	m.stateID = 0
	m.cursor = slotEntry{}
	m.container = nil
	m.notifyLocked()
	m.mu.Unlock()
}

func From(c *client.Client) *Module { return client.ModuleAs[*Module](c, ModuleName) }

func (m *Module) OnSlotUpdate(cb func(index int, item *items.Stack)) {
	m.onSlotUpdate = append(m.onSlotUpdate, cb)
}

func (m *Module) OnHeldSlotChange(cb func(slot int)) {
	m.onHeldSlotChange = append(m.onHeldSlotChange, cb)
}

func (m *Module) OnContainerOpen(cb func(windowID int32, menuType MenuType, title string)) {
	m.onContainerOpen = append(m.onContainerOpen, cb)
}

func (m *Module) OnContainerClose(cb func()) {
	m.onContainerClose = append(m.onContainerClose, cb)
}

// OnContentReplaced observes complete content (S2CContainerSetContent) of
// window 0 or the open container, after it was applied.
func (m *Module) OnContentReplaced(cb func(windowID int32)) {
	m.onContentReplace = append(m.onContentReplace, cb)
}

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	if m.client.State() != jp.StatePlay {
		return
	}
	switch pkt.PacketID {
	case packetids.S2COpenScreenID:
		m.handleOpenScreen(pkt)
	case packetids.S2CContainerSetContentID:
		m.handleContainerSetContent(pkt)
	case packetids.S2CContainerSetSlotID:
		m.handleContainerSetSlot(pkt)
	case packetids.S2CContainerCloseID:
		m.handleContainerClose(pkt)
	case packetids.S2CSetHeldSlotID:
		m.handleSetHeldSlot(pkt)
	case packetids.S2CSetPlayerInventoryID:
		m.handleSetPlayerInventory(pkt)
	case packetids.S2CSetCursorItemID:
		m.handleSetCursorItem(pkt)
	case packetids.S2CRespawnID:
		// the server resends the new player's inventory
		m.mu.Lock()
		m.known = [slots.PlayerInvSize]bool{}
		m.notifyLocked()
		m.mu.Unlock()
	}
}

func (m *Module) handleOpenScreen(pkt *jp.WirePacket) {
	var d packets.S2COpenScreen
	if err := pkt.ReadInto(&d); err != nil {
		m.client.Logger.Println("inventory: failed to parse open screen:", err)
		return
	}

	if d.WindowID <= 0 {
		return
	}
	title := d.WindowTitle.String()

	m.mu.Lock()
	m.epoch++
	m.container = &containerState{
		windowID: int32(d.WindowID),
		menuType: MenuType(d.WindowType),
		title:    title,
	}
	m.notifyLocked()
	m.mu.Unlock()

	for _, cb := range m.onContainerOpen {
		cb(int32(d.WindowID), MenuType(d.WindowType), title)
	}
}

func (m *Module) handleContainerSetContent(pkt *jp.WirePacket) {
	var d packets.S2CContainerSetContent
	if err := pkt.ReadInto(&d); err != nil {
		m.client.Logger.Println("inventory: failed to parse container set content:", err)
		return
	}

	if d.WindowID == 0 {
		if m.handlePlayerInvSetContent(d) {
			m.fireContentReplaced(0)
		}
		return
	}

	m.mu.Lock()
	if m.container == nil || m.container.windowID != int32(d.WindowID) {
		m.mu.Unlock()
		return
	}

	if !m.installContainerContentLocked(d) {
		m.mu.Unlock()
		return
	}
	m.contentRevision++
	m.notifyLocked()
	m.mu.Unlock()
	m.fireContentReplaced(int32(d.WindowID))
}

func (m *Module) fireContentReplaced(windowID int32) {
	for _, cb := range m.onContentReplace {
		cb(windowID)
	}
}

// handlePlayerInvSetContent applies window 0 content and reports whether it
// was complete.
func (m *Module) handlePlayerInvSetContent(d packets.S2CContainerSetContent) bool {
	m.mu.Lock()
	if len(d.Slots) != slots.PlayerInvSize {
		m.mu.Unlock()
		return false
	}
	m.stateID = int32(d.StateID)
	count := len(d.Slots)
	updated := make([]*items.Stack, count)
	for i := range count {
		entry := decodeSlotEntry(d.Slots[i])
		m.setPlayerSlotLocked(i, entry)
		updated[i] = entry.item
	}
	m.ready = true
	if m.container == nil {
		m.cursor = decodeSlotEntry(d.CarriedItem)
		m.contentRevision++
	}
	m.notifyLocked()
	m.mu.Unlock()

	for i := range count {
		for _, cb := range m.onSlotUpdate {
			cb(i, updated[i])
		}
	}
	return true
}

func (m *Module) handleContainerSetSlot(pkt *jp.WirePacket) {
	var d packets.S2CContainerSetSlot
	if err := pkt.ReadInto(&d); err != nil {
		m.client.Logger.Println("inventory: failed to parse container set slot:", err)
		return
	}

	// Cursor updates have their own packet in 26.2, not window -1.

	// player inventory
	if d.WindowID == 0 {
		idx := int(d.Slot)
		if idx < 0 || idx >= slots.PlayerInvSize {
			return
		}
		entry := decodeSlotEntry(d.SlotData)
		m.mu.Lock()
		m.stateID = int32(d.StateID)
		m.setPlayerSlotLocked(idx, entry)
		m.notifyLocked()
		m.mu.Unlock()
		for _, cb := range m.onSlotUpdate {
			cb(idx, entry.item)
		}
		return
	}

	// container window
	m.mu.Lock()
	if m.container != nil && m.container.ready && m.container.windowID == int32(d.WindowID) {
		idx := int(d.Slot)
		if idx >= 0 && idx < len(m.container.view) {
			m.container.stateID = int32(d.StateID)
			m.setContainerViewSlot(idx, decodeSlotEntry(d.SlotData))
			m.notifyLocked()
		}
	}
	m.mu.Unlock()
}

func (m *Module) handleContainerClose(pkt *jp.WirePacket) {
	var d packets.S2CContainerClose
	if !m.client.Decode(pkt, &d) {
		return
	}

	m.mu.Lock()
	if m.container == nil || m.container.windowID != int32(d.WindowID) {
		m.mu.Unlock()
		return
	}
	m.container = nil
	m.ready = false
	m.epoch++
	m.notifyLocked()
	m.mu.Unlock()

	for _, cb := range m.onContainerClose {
		cb()
	}
}

func (m *Module) handleSetHeldSlot(pkt *jp.WirePacket) {
	var d packets.S2CSetHeldSlot
	if !m.client.Decode(pkt, &d) {
		return
	}

	slot := int(d.Slot)
	if slot < 0 || slot > 8 {
		return
	}

	m.mu.Lock()
	m.heldSlot = slot
	m.notifyLocked()
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
	if containerIdx < 0 || containerIdx >= slots.PlayerInvSize {
		return
	}

	entry := decodeSlotEntry(d.SlotData)

	m.mu.Lock()
	m.setPlayerSlotLocked(containerIdx, entry)
	m.notifyLocked()
	m.mu.Unlock()

	for _, cb := range m.onSlotUpdate {
		cb(containerIdx, entry.item)
	}
}

func (m *Module) handleSetCursorItem(pkt *jp.WirePacket) {
	var d packets.S2CSetCursorItem
	if !m.client.Decode(pkt, &d) {
		return
	}
	m.mu.Lock()
	m.cursor = decodeSlotEntry(d.CarriedItem)
	m.notifyLocked()
	m.mu.Unlock()
}

// notifyLocked invalidates outstanding snapshots even when values are unchanged:
// a full resync is an authoritative response, not an optimistic local write.
func (m *Module) notifyLocked() {
	if p := m.pendingRequest; p != nil && (p.tx.epoch != m.epoch || p.tx.session != m.session || m.contentRevision > p.contentRevision) {
		m.finishRequestLocked(false)
	}
	m.revision++
	if m.changed != nil {
		close(m.changed)
	}
	m.changed = make(chan struct{})
}

func decodeSlotEntry(raw ns.Slot) slotEntry {
	stack, err := items.FromSlot(raw)
	if err != nil {
		stack = items.EmptyStack()
	}
	return slotEntry{raw: raw, item: stack}
}
