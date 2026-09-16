package world

import (
	"github.com/go-mclib/data/blocks"
	"github.com/go-mclib/nbt"
)

func (m *Module) clearChunkBlockEntitiesLocked(cx, cz int32) {
	for position := range m.blockEntities {
		if int32(position[0]>>4) == cx && int32(position[2]>>4) == cz {
			delete(m.blockEntities, position)
		}
	}
}

func (m *Module) updateBlockLocked(x, y, z int, state int32) {
	previous, known := m.loadedBlockLocked(x, y, z)
	oldBlock, newBlock := blocks.StateBlockID(previous), blocks.StateBlockID(state)
	if !known || oldBlock < 0 || newBlock < 0 || oldBlock != newBlock {
		delete(m.blockEntities, [3]int{x, y, z})
	}
	// One block update cannot make an unknown section's other 4095 blocks air.
	if known {
		m.chunks[ChunkKey(int32(x>>4), int32(z>>4))].SetBlockState(x, y, z, state)
	}
}

func cloneNBT(tag nbt.Tag) nbt.Tag {
	switch value := tag.(type) {
	case nbt.Compound:
		copy := make(nbt.Compound, len(value))
		for key, child := range value {
			copy[key] = cloneNBT(child)
		}
		return copy
	case nbt.List:
		copy := nbt.List{ElementType: value.ElementType, Elements: make([]nbt.Tag, len(value.Elements))}
		for i, child := range value.Elements {
			copy.Elements[i] = cloneNBT(child)
		}
		return copy
	case nbt.ByteArray:
		return append(nbt.ByteArray(nil), value...)
	case nbt.IntArray:
		return append(nbt.IntArray(nil), value...)
	case nbt.LongArray:
		return append(nbt.LongArray(nil), value...)
	default:
		return tag
	}
}
