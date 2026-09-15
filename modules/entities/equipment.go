package entities

import (
	"maps"
	"slices"

	"github.com/go-mclib/data/items"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

// EquipmentItem is the observable portion of an equipment stack. Count zero
// means the server explicitly emptied this slot; an absent slot is unknown.
type EquipmentItem struct {
	ID                 int32            `json:"id"`
	Name               string           `json:"name"`
	Count              int32            `json:"count"`
	CustomName         string           `json:"custom_name,omitempty"`
	Lore               []string         `json:"lore,omitempty"`
	Enchantments       map[string]int32 `json:"enchantments,omitempty"`
	StoredEnchantments map[string]int32 `json:"stored_enchantments,omitempty"`
	ComponentsDecoded  bool             `json:"components_decoded"`
}

// ObserveItem decodes only the useful descriptive fields, never retaining raw
// component buffers or mutable ItemStack defaults.
func ObserveItem(slot ns.Slot) EquipmentItem {
	result := EquipmentItem{ID: int32(slot.ItemID), Name: items.ItemName(int32(slot.ItemID)), Count: int32(slot.Count)}
	if slot.Count <= 0 {
		return EquipmentItem{ComponentsDecoded: true}
	}
	stack, err := items.FromSlot(slot)
	if err != nil {
		return result
	}
	result.ComponentsDecoded = true
	if c := stack.Components; c != nil {
		if c.CustomName != nil {
			result.CustomName = c.CustomName.Text
			if len(c.CustomName.Raw) > 0 {
				var text ns.TextComponent
				if text.Decode(ns.NewReader(c.CustomName.Raw)) == nil {
					result.CustomName = text.String()
				}
			} else if result.CustomName == "" {
				result.CustomName = c.CustomName.Translate
			}
		}
		result.Lore = slices.Clone(c.Lore)
		result.Enchantments = maps.Clone(c.Enchantments)
		result.StoredEnchantments = maps.Clone(c.StoredEnchantments)
	}
	return result
}

func cloneEquipment(item EquipmentItem) EquipmentItem {
	item.Lore = slices.Clone(item.Lore)
	item.Enchantments = maps.Clone(item.Enchantments)
	item.StoredEnchantments = maps.Clone(item.StoredEnchantments)
	return item
}

func cloneEntity(e *Entity) *Entity {
	if e == nil {
		return nil
	}
	result := *e
	result.Metadata = slices.Clone(e.Metadata)
	for i := range result.Metadata {
		result.Metadata[i].Data = slices.Clone(result.Metadata[i].Data)
	}
	if e.Equipment != nil {
		result.Equipment = make(map[uint8]EquipmentItem, len(e.Equipment))
		for slot, item := range e.Equipment {
			result.Equipment[slot] = cloneEquipment(item)
		}
	}
	return &result
}

func (m *Module) handleSetEquipment(pkt *jp.WirePacket) {
	var d packets.S2CSetEquipment
	if pkt.ReadInto(&d) != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entities[int32(d.EntityId)]
	if e == nil {
		return
	}
	if e.Equipment == nil {
		e.Equipment = make(map[uint8]EquipmentItem)
	}
	for _, entry := range d.Slots {
		e.Equipment[uint8(entry.Slot)] = ObserveItem(entry.Item)
	}
}

func (m *Module) handleEntityEvent(pkt *jp.WirePacket) {
	var event packets.S2CEntityEvent
	if pkt.ReadInto(&event) != nil || event.EntityStatus != 55 {
		return
	}
	// LivingEntity.handleHandSwap sends SWAP_HANDS (55) instead of separate
	// SetEquipment entries. Move knowledge as well as values: an unobserved
	// hand stays unknown, rather than becoming an explicitly empty stack.
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entities[int32(event.EntityId)]
	if e == nil || e.Equipment == nil {
		return
	}
	mainhand, mainKnown := e.Equipment[0]
	offhand, offKnown := e.Equipment[1]
	if offKnown {
		e.Equipment[0] = offhand
	} else {
		delete(e.Equipment, 0)
	}
	if mainKnown {
		e.Equipment[1] = mainhand
	} else {
		delete(e.Equipment, 1)
	}
}
