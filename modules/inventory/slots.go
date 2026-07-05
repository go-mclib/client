package inventory

import (
	"hash/crc32"

	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

const (
	ModuleName = "inventory"
	TotalSlots = 46

	SlotCraftingResult = 0
	SlotArmorHead      = 5
	SlotArmorChest     = 6
	SlotArmorLegs      = 7
	SlotArmorFeet      = 8
	SlotMainStart      = 9
	SlotMainEnd        = 36
	SlotHotbarStart    = 36
	SlotHotbarEnd      = 45
	SlotOffhand        = 45
	PlayerInvSlots     = 36 // main(27) + hotbar(9) appended to every container view
)

var crc32c = crc32.MakeTable(crc32.Castagnoli)

// playerInvToContainer maps an Inventory.java slot index to the InventoryMenu
// container slot index used by the protocol.
func playerInvToContainer(invSlot int) int {
	switch {
	case invSlot >= 0 && invSlot <= 8:
		return SlotHotbarStart + invSlot // hotbar 0-8 → container 36-44
	case invSlot >= 9 && invSlot <= 35:
		return invSlot // main inventory is the same
	case invSlot >= 36 && invSlot <= 39:
		return 8 - (invSlot - 36) // armor: inv 36=feet→8, 37=legs→7, 38=chest→6, 39=head→5
	case invSlot == 40:
		return SlotOffhand
	default:
		return -1
	}
}

// slotToHashed converts a raw protocol Slot to a HashedSlot for use in
// client-to-server click packets. Component data is replaced with CRC32C hashes.
func slotToHashed(s ns.Slot) ns.HashedSlot {
	if s.IsEmpty() {
		return ns.EmptyHashedSlot()
	}
	hs := ns.HashedSlot{
		Present: true,
		ItemID:  s.ItemID,
		Count:   s.Count,
	}
	for _, comp := range s.Components.Add {
		hs.Components.Add = append(hs.Components.Add, ns.HashedComponent{
			ID:   comp.ID,
			Hash: ns.Int32(crc32.Checksum(comp.Data, crc32c)),
		})
	}
	hs.Components.Remove = s.Components.Remove
	return hs
}
