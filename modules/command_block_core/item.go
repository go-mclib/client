package command_block_core

import (
	"errors"
	"slices"

	"github.com/go-mclib/data/registries"
	"github.com/go-mclib/nbt"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// commandBlockItem builds a command_block item carrying a block_entity_data
// component {Command, auto:1b} so it runs cmd once when placed.
//
// block_entity_data wire form (TypedEntityData stream codec): a VarInt block
// entity type id followed by a network-NBT compound (the type carries the "id",
// so the compound omits it).
func commandBlockItem(cmd string) (ns.Slot, error) {
	itemID := registries.Item.Get("minecraft:command_block")
	if itemID < 0 {
		return ns.Slot{}, errors.New("unknown item minecraft:command_block")
	}
	compID := registries.DataComponentType.Get("minecraft:block_entity_data")
	if compID < 0 {
		return ns.Slot{}, errors.New("unknown component minecraft:block_entity_data")
	}
	beType := registries.BlockEntityType.Get("minecraft:command_block")
	if beType < 0 {
		return ns.Slot{}, errors.New("unknown block entity minecraft:command_block")
	}

	tag, err := nbt.EncodeNetwork(nbt.Compound{
		"Command": nbt.String(cmd),
		"auto":    nbt.Byte(1),
	})
	if err != nil {
		return ns.Slot{}, err
	}
	buf := ns.NewWriter()
	if err := buf.WriteVarInt(ns.VarInt(beType)); err != nil {
		return ns.Slot{}, err
	}
	payload := slices.Concat(buf.Bytes(), tag)

	return ns.Slot{
		Count:  1,
		ItemID: ns.VarInt(itemID),
		Components: ns.SlotComponents{
			Add: []ns.RawSlotComponent{{ID: ns.VarInt(compID), Data: payload}},
		},
	}, nil
}
