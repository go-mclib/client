package world

import (
	"maps"

	"github.com/go-mclib/data/blocks"
)

func (m *Module) clearChunkBlockEntitiesLocked(cx, cz int32) {
	maps.DeleteFunc(m.blockEntities, func(position [3]int, _ *BlockEntityData) bool {
		return int32(position[0]>>4) == cx && int32(position[2]>>4) == cz
	})
}

func (m *Module) updateBlockLocked(x, y, z int, state int32) {
	previous, known := m.loadedBlockLocked(x, y, z)
	oldBlock, newBlock := blocks.StateBlockID(previous), blocks.StateBlockID(state)
	if !known || oldBlock < 0 || newBlock < 0 || oldBlock != newBlock {
		delete(m.blockEntities, [3]int{x, y, z})
	}
	// One block update cannot make an unknown section's other 4095 blocks air.
	if known {
		m.chunks[chunkKey(int32(x>>4), int32(z>>4))].SetBlockState(x, y, z, state)
	}
}
