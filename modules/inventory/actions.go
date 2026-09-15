package inventory

import (
	"context"
	"fmt"
	"slices"

	"github.com/go-mclib/data/items"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

// RawSlots returns independent snapshots of all 46 protocol slots and cursor.
func (m *Module) RawSlots() (slots [TotalSlots]ns.Slot, stateID int32, cursor ns.Slot) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for i := range TotalSlots {
		slots[i] = cloneRawSlot(m.slots[i].raw)
	}
	return slots, m.stateID, cloneRawSlot(m.cursor.raw)
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
func (m *Module) GetSlot(index int) *items.ItemStack {
	if index < 0 || index >= TotalSlots {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.slots[index].item
}

// HeldItem returns the item in the currently selected hotbar slot.
func (m *Module) HeldItem() *items.ItemStack {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.slots[SlotHotbarStart+m.heldSlot].item
}

// HeldSlotIndex returns which hotbar slot is selected (0-8).
func (m *Module) HeldSlotIndex() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.heldSlot
}

// GetHotbar returns all 9 hotbar items (index 0 = hotbar slot 0).
func (m *Module) GetHotbar() [9]*items.ItemStack {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result [9]*items.ItemStack
	for i := range 9 {
		result[i] = m.slots[SlotHotbarStart+i].item
	}
	return result
}

// GetArmor returns the four armor slot items.
func (m *Module) GetArmor() (head, chest, legs, feet *items.ItemStack) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.slots[SlotArmorHead].item,
		m.slots[SlotArmorChest].item,
		m.slots[SlotArmorLegs].item,
		m.slots[SlotArmorFeet].item
}

// GetOffhand returns the offhand/shield slot item.
func (m *Module) GetOffhand() *items.ItemStack {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.slots[SlotOffhand].item
}

// CursorItem returns the item currently held on the cursor.
func (m *Module) CursorItem() *items.ItemStack {
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
	for i := SlotHotbarStart; i < SlotHotbarEnd; i++ {
		if s := m.slots[i].item; !s.IsEmpty() && s.ID == itemID {
			return i
		}
	}
	// then main inventory
	for i := SlotMainStart; i < SlotMainEnd; i++ {
		if s := m.slots[i].item; !s.IsEmpty() && s.ID == itemID {
			return i
		}
	}
	return -1
}

// FindItemByName returns the first container slot index containing an item with
// the given name (e.g. "minecraft:diamond_sword"). Returns -1 if not found.
func (m *Module) FindItemByName(name string) int {
	id := items.ItemID(name)
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
	for i := SlotMainStart; i < SlotHotbarEnd; i++ {
		if s := m.slots[i].item; !s.IsEmpty() && s.ID == itemID {
			result = append(result, i)
		}
	}
	return result
}

// SetHeldSlot submits a session-bound hotbar selection. Vanilla sends no success acknowledgement.
func (m *Module) SetHeldSlot(slot int) error {
	snapshot := m.Snapshot()
	_, err := m.Execute(context.Background(), Operation{Kind: "select_hotbar", WindowID: snapshot.WindowID, Revision: snapshot.Revision, Hotbar: slot})
	return err
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
	if err := send(transactionPacket{Packet: &packets.C2SSetCarriedItem{Slot: ns.Int16(slot)}, tx: &selection}); err != nil {
		return err
	}
	m.mu.Lock()
	if err := tx.checkLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	m.heldSlot = slot
	m.notifyLocked()
	m.mu.Unlock()
	for _, cb := range m.onHeldSlotChange {
		cb(slot)
	}
	return nil
}

// RequestHotbarSwap submits a main-inventory/hotbar swap without waiting for
// confirmation. It never predicts contents. The caller must observe server slot
// updates; another inventory transaction cannot start until resync or timeout.
// The supplied transport must be session-bound. A concurrent transaction returns busy.
func (m *Module) RequestHotbarSwap(containerSlot, hotbarIndex int, send func(jp.Packet) error) error {
	if containerSlot < SlotMainStart || containerSlot >= SlotHotbarStart || hotbarIndex < 0 || hotbarIndex > 8 {
		return fmt.Errorf("invalid main-inventory/hotbar swap")
	}
	return m.submitHotbarSwap(containerSlot, hotbarIndex, send)
}

// SwapToHotbar swaps an active-view slot into hotbar index 0..8 and waits for
// server confirmation. Slot indices use the current Snapshot's protocol view.
func (m *Module) SwapToHotbar(containerSlot, hotbarIndex int) error {
	snapshot := m.Snapshot()
	tx, done, err := m.beginTransaction(context.Background(), Operation{WindowID: snapshot.WindowID, Revision: snapshot.Revision})
	if err != nil {
		return err
	}
	defer done()
	if !tx.current.Ready {
		if err := tx.click(-1, 0, 0, nil); err != nil {
			return err
		}
	}
	if !tx.current.Cursor.IsEmpty() {
		return fmt.Errorf("clear the cursor before swapping")
	}
	return tx.hotbarSwap(containerSlot, hotbarIndex)
}

// HoldItem finds an item by ID in the hotbar and selects that slot.
// Returns an error if the item is not in the hotbar.
func (m *Module) HoldItem(itemID int32) error {
	m.mu.RLock()
	for i := range 9 {
		if s := m.slots[SlotHotbarStart+i].item; !s.IsEmpty() && s.ID == itemID {
			m.mu.RUnlock()
			return m.SetHeldSlot(i)
		}
	}
	m.mu.RUnlock()
	return fmt.Errorf("item %d not found in hotbar", itemID)
}
