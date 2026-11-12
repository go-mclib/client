package inventory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/go-mclib/data/items"
	"github.com/go-mclib/data/packets"
	"github.com/go-mclib/data/slots"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// RawSlots returns independent snapshots of all 46 protocol slots and cursor.
func (m *Module) RawSlots() (raw [slots.PlayerInvSize]ns.Slot, stateID int32, cursor ns.Slot) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range slots.PlayerInvSize {
		raw[i] = cloneRawSlot(m.slots[i].raw)
	}
	return raw, m.stateID, cloneRawSlot(m.cursor.raw)
}

func cloneRawSlot(slot ns.Slot) ns.Slot {
	slot.Components.Add = slices.Clone(slot.Components.Add)
	for i := range slot.Components.Add {
		slot.Components.Add[i].Data = slices.Clone(slot.Components.Add[i].Data)
	}
	slot.Components.Remove = slices.Clone(slot.Components.Remove)
	return slot
}

// GetSlot returns the item at a container slot index (0-45), or nil if empty.
func (m *Module) GetSlot(index int) *items.Stack {
	if index < 0 || index >= slots.PlayerInvSize {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.slots[index].item
}

// HeldItem returns the item in the currently selected hotbar slot.
func (m *Module) HeldItem() *items.Stack {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.slots[slots.PlayerHotbarStart+m.heldSlot].item
}

// HeldSlotIndex returns which hotbar slot is selected (0-8).
func (m *Module) HeldSlotIndex() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.heldSlot
}

// Hotbar returns all 9 hotbar items (index 0 = hotbar slot 0).
func (m *Module) Hotbar() [9]*items.Stack {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result [9]*items.Stack
	for i := range 9 {
		result[i] = m.slots[slots.PlayerHotbarStart+i].item
	}
	return result
}

// Armor returns the four armor slot items.
func (m *Module) Armor() (head, chest, legs, feet *items.Stack) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.slots[slots.PlayerArmorHead].item,
		m.slots[slots.PlayerArmorChest].item,
		m.slots[slots.PlayerArmorLegs].item,
		m.slots[slots.PlayerArmorFeet].item
}

// Offhand returns the offhand/shield slot item.
func (m *Module) Offhand() *items.Stack {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.slots[slots.PlayerOffhand].item
}

// CursorItem returns the item currently held on the cursor.
func (m *Module) CursorItem() *items.Stack {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cursor.item
}

// FindItem returns the first container slot index containing the given item ID,
// searching hotbar first then main inventory. Returns -1 if not found.
func (m *Module) FindItem(itemID int32) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// search hotbar first
	for i := slots.PlayerHotbarStart; i < slots.PlayerHotbarEnd; i++ {
		if s := m.slots[i].item; !s.IsEmpty() && s.ID == itemID {
			return i
		}
	}
	// then main inventory
	for i := slots.PlayerMainStart; i < slots.PlayerMainEnd; i++ {
		if s := m.slots[i].item; !s.IsEmpty() && s.ID == itemID {
			return i
		}
	}
	return -1
}

// FindItemByName returns the first container slot index containing an item with
// the given name (e.g. "minecraft:diamond_sword"). Returns -1 if not found.
func (m *Module) FindItemByName(name string) int {
	id := items.ID(name)
	if id < 0 {
		return -1
	}
	return m.FindItem(id)
}

// FindItems returns all container slot indices containing the given item ID.
func (m *Module) FindItems(itemID int32) []int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []int
	for i := slots.PlayerMainStart; i < slots.PlayerHotbarEnd; i++ {
		if s := m.slots[i].item; !s.IsEmpty() && s.ID == itemID {
			result = append(result, i)
		}
	}
	return result
}

// SetHeldSlot submits a session-bound hotbar selection. Vanilla sends no success acknowledgement.
func (m *Module) SetHeldSlot(slot int) error {
	snapshot := m.Snapshot()
	_, err := m.Execute(context.Background(), Operation{Kind: OpSelectHotbar, WindowID: snapshot.WindowID, Revision: snapshot.Revision, Hotbar: slot})
	return err
}

// SetCreativeSlot sets a player-inventory protocol slot (hotbar 0-8 is 36-44)
// in creative mode. Like vanilla, the server does not echo the change, so the
// local slot is updated once the packet is sent on this inventory session.
func (m *Module) SetCreativeSlot(index int, item ns.Slot) error {
	if index < 0 || index >= slots.PlayerInvSize {
		return fmt.Errorf("invalid inventory slot %d", index)
	}
	snapshot := m.Snapshot()
	tx, done, err := m.tryTransaction(snapshot.WindowID, snapshot.Revision)
	if err != nil {
		return err
	}
	defer done()
	if err := tx.send(&packets.C2SSetCreativeModeSlot{Slot: ns.Int16(index), ClickedItem: item}); err != nil {
		return err
	}
	entry := decodeSlotEntry(item)
	m.mu.Lock()
	if err := tx.checkLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	m.setPlayerSlotLocked(index, entry)
	m.notifyLocked()
	m.mu.Unlock()
	for _, cb := range m.onSlotUpdate {
		cb(index, entry.item)
	}
	return nil
}

// SelectHotbar submits a selection using the caller's session-bound transport.
// It never waits for inventory work: a concurrent transaction returns busy.
// Success means sent, not server-confirmed.
func (m *Module) SelectHotbar(slot int, send func(jp.Packet) error) error {
	if slot < 0 || slot > 8 {
		return fmt.Errorf("invalid hotbar slot %d", slot)
	}
	snapshot := m.Snapshot()
	tx, done, err := m.tryTransaction(snapshot.WindowID, snapshot.Revision)
	if err != nil {
		return err
	}
	defer done()
	// Caller transports may enqueue rather than serialize immediately. The
	// queued packet belongs to the session, not this short-lived gate lease.
	selection := *tx
	selection.ctx = tx.session.ctx
	return tx.selectHotbar(slot, func(p jp.Packet) error {
		return send(transactionPacket{Packet: p, tx: &selection})
	})
}

// RequestHotbarSwap submits a main-inventory/hotbar swap without waiting for
// confirmation. It never predicts contents. The caller must observe server slot
// updates; another inventory transaction cannot start until resync or timeout.
// The supplied transport must be session-bound. A concurrent transaction returns busy.
func (m *Module) RequestHotbarSwap(containerSlot, hotbarIndex int, send func(jp.Packet) error) error {
	if containerSlot < slots.PlayerMainStart || containerSlot >= slots.PlayerHotbarStart || hotbarIndex < 0 || hotbarIndex > 8 {
		return errors.New("invalid main-inventory/hotbar swap")
	}
	// Deliberately nonblocking: action tick callers must keep dispatching
	// packets. Click ordering stays reserved until full resync or timeout.
	snapshot := m.Snapshot()
	tx, done, err := m.tryTransaction(0, snapshot.Revision)
	if err != nil {
		return err
	}
	if !snapshot.Ready || !snapshot.Cursor.IsEmpty() {
		done()
		return errors.New("inventory must be ready with an empty cursor")
	}
	m.mu.Lock()
	if err := tx.checkLocked(); err != nil {
		m.mu.Unlock()
		done()
		return err
	}
	pending := &pendingInventoryRequest{tx: tx, contentRevision: m.contentRevision, done: done}
	m.pendingRequest = pending
	pending.timer = time.AfterFunc(inventoryClickTimeout, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.pendingRequest == pending {
			m.finishRequestLocked(true)
			m.notifyLocked()
		}
	})
	m.mu.Unlock()
	err = send(transactionPacket{Packet: &packets.C2SContainerClick{
		WindowID: 0, StateID: -1, Slot: ns.Int16(containerSlot), Button: ns.Int8(hotbarIndex),
		Mode: 2, CarriedItem: ns.EmptyHashedSlot(),
	}, tx: tx, checkContents: true})
	if err != nil {
		m.mu.Lock()
		if m.pendingRequest == pending {
			m.finishRequestLocked(true)
			m.notifyLocked()
		}
		m.mu.Unlock()
	}
	return err
}

// SwapToHotbar swaps an active-view slot into hotbar index 0..8 and waits for
// server confirmation. Slot indices use the current Snapshot's protocol view.
func (m *Module) SwapToHotbar(containerSlot, hotbarIndex int) error {
	tx, done, err := m.beginCurrent(context.Background())
	if err != nil {
		return err
	}
	defer done()
	if !tx.current.Cursor.IsEmpty() {
		return errors.New("clear the cursor before swapping")
	}
	return tx.hotbarSwap(containerSlot, hotbarIndex)
}

// HoldItem finds an item by ID in the hotbar and selects that slot.
// Returns an error if the item is not in the hotbar.
func (m *Module) HoldItem(itemID int32) error {
	m.mu.RLock()
	for i := range 9 {
		if s := m.slots[slots.PlayerHotbarStart+i].item; !s.IsEmpty() && s.ID == itemID {
			m.mu.RUnlock()
			return m.SetHeldSlot(i)
		}
	}
	m.mu.RUnlock()
	return fmt.Errorf("item %d not found in hotbar", itemID)
}
