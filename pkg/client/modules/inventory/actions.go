package inventory

import (
	"fmt"

	"github.com/go-mclib/data/pkg/data/items"
	"github.com/go-mclib/data/pkg/packets"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

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

// SetHeldSlot changes the selected hotbar slot (0-8) and notifies the server.
func (m *Module) SetHeldSlot(slot int) error {
	if slot < 0 || slot > 8 {
		return fmt.Errorf("invalid hotbar slot %d", slot)
	}

	m.mu.Lock()
	m.heldSlot = slot
	m.mu.Unlock()

	if err := m.client.WritePacket(&packets.C2SSetCarriedItem{
		Slot: ns.Int16(slot),
	}); err != nil {
		return err
	}

	for _, cb := range m.onHeldSlotChange {
		cb(slot)
	}
	return nil
}

// SwapToHotbar swaps an item from any container slot into a hotbar slot (0-8).
// Uses the SWAP click mode.
func (m *Module) SwapToHotbar(containerSlot, hotbarIndex int) error {
	if containerSlot < 0 || containerSlot >= TotalSlots {
		return fmt.Errorf("invalid container slot %d", containerSlot)
	}
	if hotbarIndex < 0 || hotbarIndex > 8 {
		return fmt.Errorf("invalid hotbar index %d", hotbarIndex)
	}

	hotbarSlot := SlotHotbarStart + hotbarIndex

	m.mu.Lock()
	stateID := m.stateID

	// predict the swap
	srcEntry := m.slots[containerSlot]
	dstEntry := m.slots[hotbarSlot]
	m.slots[containerSlot] = dstEntry
	m.slots[hotbarSlot] = srcEntry

	cursorHashed := slotToHashed(m.cursor.raw)
	m.mu.Unlock()

	err := m.client.WritePacket(&packets.C2SContainerClick{
		WindowId: 0,
		StateId:  ns.VarInt(stateID),
		Slot:     ns.Int16(containerSlot),
		Button:   ns.Int8(hotbarIndex),
		Mode:     2, // SWAP
		ChangedSlots: []packets.ChangedSlot{
			{SlotNum: ns.Int16(containerSlot), Item: slotToHashed(dstEntry.raw)},
			{SlotNum: ns.Int16(hotbarSlot), Item: slotToHashed(srcEntry.raw)},
		},
		CarriedItem: cursorHashed,
	})
	if err != nil {
		// revert prediction on send failure
		m.mu.Lock()
		m.slots[containerSlot] = srcEntry
		m.slots[hotbarSlot] = dstEntry
		m.mu.Unlock()
		return err
	}

	for _, cb := range m.onSlotUpdate {
		cb(containerSlot, dstEntry.item)
		cb(hotbarSlot, srcEntry.item)
	}
	return nil
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
