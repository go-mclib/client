package inventory

// MenuType represents a Minecraft container menu type from the minecraft:menu registry.
type MenuType int32

const (
	MenuGeneric9x1 MenuType = 0
	MenuGeneric9x2 MenuType = 1
	MenuGeneric9x3 MenuType = 2 // single chest, barrel
	MenuGeneric9x4 MenuType = 3
	MenuGeneric9x5 MenuType = 4
	MenuGeneric9x6 MenuType = 5 // double chest
	MenuGeneric3x3 MenuType = 6 // dispenser, dropper
	MenuCrafter3x3 MenuType = 7
	MenuAnvil      MenuType = 8
	MenuBeacon     MenuType = 9
	MenuFurnace    MenuType = 14
	MenuHopper     MenuType = 16
	MenuShulkerBox MenuType = 20
)

type containerState struct {
	windowID int32
	menuType MenuType
	title    string
	stateID  int32
	slots    []slotEntry // container-only slots (excludes the 36 player inv slots)
}

// containerViewSlot returns the slotEntry at the given absolute container view index.
// Must be called under m.mu lock.
func (m *Module) containerViewSlot(idx int) slotEntry {
	containerSlotCount := len(m.container.slots)
	if idx >= 0 && idx < containerSlotCount {
		return m.container.slots[idx]
	}
	playerIdx := SlotMainStart + (idx - containerSlotCount)
	if playerIdx >= SlotMainStart && playerIdx < TotalSlots {
		return m.slots[playerIdx]
	}
	return slotEntry{}
}

// setContainerViewSlot sets the slotEntry at the given absolute container view index.
// Must be called under m.mu lock.
func (m *Module) setContainerViewSlot(idx int, entry slotEntry) {
	containerSlotCount := len(m.container.slots)
	if idx >= 0 && idx < containerSlotCount {
		m.container.slots[idx] = entry
		return
	}
	playerIdx := SlotMainStart + (idx - containerSlotCount)
	if playerIdx >= SlotMainStart && playerIdx < TotalSlots {
		m.slots[playerIdx] = entry
	}
}
