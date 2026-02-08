package inventory

import (
	"fmt"

	"github.com/go-mclib/data/pkg/data/items"
	"github.com/go-mclib/data/pkg/packets"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

// ContainerOpen returns true if a container is currently open.
func (m *Module) ContainerOpen() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.container != nil
}

// ContainerMenuType returns the menu type of the open container, or -1 if none.
func (m *Module) ContainerMenuType() MenuType {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.container == nil {
		return -1
	}
	return m.container.menuType
}

// ContainerSlotCount returns the number of container-specific slots
// (excluding the 36 player inventory slots), or 0 if no container is open.
func (m *Module) ContainerSlotCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.container == nil {
		return 0
	}
	return len(m.container.slots)
}

// ContainerSlot returns the item at a container slot index (0-based, container slots only).
func (m *Module) ContainerSlot(index int) *items.ItemStack {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.container == nil || index < 0 || index >= len(m.container.slots) {
		return nil
	}
	return m.container.slots[index].item
}

// ContainerSlots returns all container-specific slot items.
func (m *Module) ContainerSlots() []*items.ItemStack {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.container == nil {
		return nil
	}
	result := make([]*items.ItemStack, len(m.container.slots))
	for i, s := range m.container.slots {
		result[i] = s.item
	}
	return result
}

// ContainerClick performs a left-click (Mode 0, Button 0) on a slot in the open container view.
// viewIndex is the absolute index in the container view (0..totalSlots-1).
func (m *Module) ContainerClick(viewIndex int) error {
	m.mu.Lock()
	if m.container == nil {
		m.mu.Unlock()
		return fmt.Errorf("no container open")
	}

	c := m.container
	stateID := c.stateID
	clickedEntry := m.containerViewSlot(viewIndex)
	cursorEntry := m.cursor

	if cursorEntry.item.IsEmpty() && clickedEntry.item.IsEmpty() {
		m.mu.Unlock()
		return nil
	}

	// predict: swap cursor and clicked slot
	var newClicked, newCursor slotEntry
	if cursorEntry.item.IsEmpty() {
		// pick up stack
		newClicked = slotEntry{}
		newCursor = clickedEntry
	} else if clickedEntry.item.IsEmpty() {
		// place stack
		newClicked = cursorEntry
		newCursor = slotEntry{}
	} else {
		// swap (simplified — accurate merge for same-item stacks is complex)
		newClicked = cursorEntry
		newCursor = clickedEntry
	}

	m.setContainerViewSlot(viewIndex, newClicked)
	m.cursor = newCursor
	cursorHashed := slotToHashed(newCursor.raw)
	changedHashed := slotToHashed(newClicked.raw)
	m.mu.Unlock()

	return m.client.WritePacket(&packets.C2SContainerClick{
		WindowId: ns.VarInt(c.windowID),
		StateId:  ns.VarInt(stateID),
		Slot:     ns.Int16(viewIndex),
		Button:   0,
		Mode:     0, // PICKUP
		ChangedSlots: []packets.ChangedSlot{
			{SlotNum: ns.Int16(viewIndex), Item: changedHashed},
		},
		CarriedItem: cursorHashed,
	})
}

// ContainerRightClick performs a right-click (Mode 0, Button 1) on a slot in the open container view.
// If cursor is empty, picks up half the stack. If cursor has items, places one item.
func (m *Module) ContainerRightClick(viewIndex int) error {
	m.mu.Lock()
	if m.container == nil {
		m.mu.Unlock()
		return fmt.Errorf("no container open")
	}

	c := m.container
	stateID := c.stateID
	clickedEntry := m.containerViewSlot(viewIndex)
	cursorEntry := m.cursor

	if cursorEntry.item.IsEmpty() && clickedEntry.item.IsEmpty() {
		m.mu.Unlock()
		return nil
	}

	// right-click prediction is complex (half-stack pickup, place-one);
	// send the packet and let server re-sync
	cursorHashed := slotToHashed(cursorEntry.raw)
	m.mu.Unlock()

	return m.client.WritePacket(&packets.C2SContainerClick{
		WindowId:     ns.VarInt(c.windowID),
		StateId:      ns.VarInt(stateID),
		Slot:         ns.Int16(viewIndex),
		Button:       1,
		Mode:         0, // PICKUP
		ChangedSlots: nil,
		CarriedItem:  cursorHashed,
	})
}

// ContainerShiftClick performs a shift-click (Mode 1, Button 0) on a slot in the open container view.
// Moves items between the container and player inventory sections.
func (m *Module) ContainerShiftClick(viewIndex int) error {
	m.mu.Lock()
	if m.container == nil {
		m.mu.Unlock()
		return fmt.Errorf("no container open")
	}

	c := m.container
	stateID := c.stateID
	clickedEntry := m.containerViewSlot(viewIndex)
	if clickedEntry.item.IsEmpty() {
		m.mu.Unlock()
		return nil
	}

	// shift-click prediction is complex (depends on destination space);
	// send minimal prediction and rely on server re-sync
	cursorHashed := slotToHashed(m.cursor.raw)
	m.mu.Unlock()

	return m.client.WritePacket(&packets.C2SContainerClick{
		WindowId: ns.VarInt(c.windowID),
		StateId:  ns.VarInt(stateID),
		Slot:     ns.Int16(viewIndex),
		Button:   0,
		Mode:     1, // QUICK_MOVE
		ChangedSlots: []packets.ChangedSlot{
			{SlotNum: ns.Int16(viewIndex), Item: ns.EmptyHashedSlot()},
		},
		CarriedItem: cursorHashed,
	})
}

// CloseContainer closes the currently open container.
func (m *Module) CloseContainer() error {
	m.mu.Lock()
	if m.container == nil {
		m.mu.Unlock()
		return fmt.Errorf("no container open")
	}
	windowID := m.container.windowID
	m.container = nil
	m.mu.Unlock()

	for _, cb := range m.onContainerClose {
		cb()
	}

	return m.client.WritePacket(&packets.C2SContainerClose{
		WindowId: ns.VarInt(windowID),
	})
}
