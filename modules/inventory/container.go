package inventory

import (
	"github.com/go-mclib/data/packets"
	"github.com/go-mclib/data/slots"
)

// MenuType represents a Minecraft container menu type from the minecraft:menu registry.
// MenuPlayer (-1) denotes the player's 46-slot inventory, which has no menu registry ID.
type MenuType int32

const (
	MenuPlayer       MenuType = -1
	MenuGeneric9x1   MenuType = 0
	MenuGeneric9x2   MenuType = 1
	MenuGeneric9x3   MenuType = 2
	MenuGeneric9x4   MenuType = 3
	MenuGeneric9x5   MenuType = 4
	MenuGeneric9x6   MenuType = 5
	MenuGeneric3x3   MenuType = 6
	MenuCrafter3x3   MenuType = 7
	MenuAnvil        MenuType = 8
	MenuBeacon       MenuType = 9
	MenuBlastFurnace MenuType = 10
	MenuBrewingStand MenuType = 11
	MenuCrafting     MenuType = 12
	MenuEnchantment  MenuType = 13
	MenuFurnace      MenuType = 14
	MenuGrindstone   MenuType = 15
	MenuHopper       MenuType = 16
	MenuLectern      MenuType = 17
	MenuLoom         MenuType = 18
	MenuMerchant     MenuType = 19
	MenuShulkerBox   MenuType = 20
	MenuSmithing     MenuType = 21
	MenuSmoker       MenuType = 22
	MenuCartography  MenuType = 23
	MenuStonecutter  MenuType = 24
)

type containerState struct {
	windowID int32
	menuType MenuType
	title    string
	stateID  int32
	ready    bool
	slots    []slotEntry // menu-specific prefix, aliases view
	view     []slotEntry // entire authoritative protocol view
}

func menuSlots(menu MenuType) (own, player int) {
	if menu >= MenuGeneric9x1 && menu <= MenuGeneric9x6 {
		return (int(menu) + 1) * 9, PlayerInvSlots
	}
	switch menu {
	case MenuGeneric3x3, MenuCrafter3x3:
		return 9, PlayerInvSlots
	case MenuAnvil, MenuBlastFurnace, MenuFurnace, MenuGrindstone, MenuMerchant, MenuSmoker, MenuCartography:
		return 3, PlayerInvSlots
	case MenuBeacon:
		return 1, PlayerInvSlots
	case MenuBrewingStand, MenuHopper:
		return 5, PlayerInvSlots
	case MenuCrafting:
		return 10, PlayerInvSlots
	case MenuEnchantment, MenuStonecutter:
		return 2, PlayerInvSlots
	case MenuLectern:
		return 1, 0
	case MenuLoom, MenuSmithing:
		return 4, PlayerInvSlots
	case MenuShulkerBox:
		return 27, PlayerInvSlots
	default:
		return -1, 0
	}
}

// Unknown menu layouts are still visible, but not safe for slot-mapped mutation.
func (m *Module) installContainerContentLocked(d packets.S2CContainerSetContent) bool {
	c := m.container
	own, player := menuSlots(c.menuType)
	c.stateID = int32(d.StateID)
	c.view = make([]slotEntry, len(d.Slots))
	for i, raw := range d.Slots {
		c.view[i] = decodeSlotEntry(raw)
	}
	c.ready = own >= 0 && len(d.Slots) == own+player
	if !c.ready {
		c.slots = c.view
	} else {
		c.slots = c.view[:own]
		copy(m.slots[slots.PlayerMainStart:slots.PlayerMainStart+player], c.view[own:])
		for i := slots.PlayerMainStart; i < slots.PlayerMainStart+player; i++ {
			m.known[i] = true
		}
	}
	m.cursor = decodeSlotEntry(d.CarriedItem)
	return true
}

// All helpers below require m.mu.

func (m *Module) setContainerViewSlot(idx int, entry slotEntry) {
	c := m.container
	if idx < 0 || idx >= len(c.view) {
		return
	}
	c.view[idx] = entry
	if c.ready && idx >= len(c.slots) {
		playerIdx := slots.PlayerMainStart + idx - len(c.slots)
		if playerIdx < slots.PlayerHotbarEnd {
			m.slots[playerIdx] = entry
			m.known[playerIdx] = true
		}
	}
}

func (m *Module) setPlayerSlotLocked(idx int, entry slotEntry) {
	m.slots[idx] = entry
	m.known[idx] = true
	c := m.container
	if c != nil && c.ready && idx >= slots.PlayerMainStart && idx < slots.PlayerHotbarEnd {
		viewIdx := len(c.slots) + idx - slots.PlayerMainStart
		if viewIdx < len(c.view) {
			c.view[viewIdx] = entry
		}
	}
}
