package inventory

import (
	"context"
	"fmt"

	"github.com/go-mclib/data/items"
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

// ContainerClick left-clicks an absolute active-view slot (including player menu) and waits
// for a full authoritative response. Success means the expected pickup, merge,
// or swap was observed. Server-specific item click overrides can reject that
// expectation; inspect Snapshot after errors. Do not call in packet callbacks.
func (m *Module) ContainerClick(viewIndex int) error {
	return m.containerPickup(viewIndex, 0)
}

// ContainerRightClick picks up ceil(count/2) with an empty cursor, or places
// one item with a nonempty cursor, waiting for authoritative confirmation.
func (m *Module) ContainerRightClick(viewIndex int) error {
	return m.containerPickup(viewIndex, 1)
}

func (m *Module) containerPickup(viewIndex, button int) error {
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
	if !tx.current.Ready {
		return fmt.Errorf("unsupported or incomplete menu")
	}
	return tx.pickup(viewIndex, button)
}

// ContainerShiftClick moves a stack between supported storage and player
// sections. It explicitly merges component-identical stacks then fills empty
// slots, confirming every click rather than predicting QUICK_MOVE. A full
// destination or partial transfer returns an error; Snapshot shows what moved.
func (m *Module) ContainerShiftClick(viewIndex int) error {
	snapshot := m.Snapshot()
	if snapshot.WindowID == 0 {
		return fmt.Errorf("no container open")
	}
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
		return fmt.Errorf("clear the cursor before shift-clicking")
	}
	if _, _, err := storageRange(tx.current); err != nil {
		return err
	}
	source, err := tx.slot(viewIndex)
	if err != nil {
		return err
	}
	if source.IsEmpty() {
		return nil
	}
	start, end := 0, tx.current.ContainerSlots
	if viewIndex < end {
		start, end = end, len(tx.current.Slots)
	}
	for pass := range 2 {
		for i := start; i < end; i++ {
			source = tx.current.Slots[viewIndex]
			if source.IsEmpty() {
				return nil
			}
			dest := tx.current.Slots[i]
			if pass == 0 && (dest.IsEmpty() || !sameIdentity(source, dest)) {
				continue
			}
			if pass == 1 && !dest.IsEmpty() {
				continue
			}
			count := min(int(source.Count), int(items.SlotMaxStackSize(source))-int(dest.Count))
			if count > 0 {
				if err := tx.move(viewIndex, i, count); err != nil {
					return err
				}
			}
		}
	}
	if !tx.current.Slots[viewIndex].IsEmpty() {
		return fmt.Errorf("destination full; source stack not fully transferred")
	}
	return nil
}

// CloseContainer closes the current container and waits until an authoritative
// player-menu resync proves the server has closed it and returned cursor items.
func (m *Module) CloseContainer() error {
	snapshot := m.Snapshot()
	if snapshot.WindowID == 0 {
		return fmt.Errorf("no container open")
	}
	_, err := m.Execute(context.Background(), Operation{Kind: "close", WindowID: snapshot.WindowID, Revision: snapshot.Revision})
	return err
}
