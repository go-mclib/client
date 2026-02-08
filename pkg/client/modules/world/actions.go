package world

import (
	"github.com/go-mclib/data/pkg/data/blocks"
	"github.com/go-mclib/data/pkg/data/chunks"
)

// GetBlock returns the block state ID at the given world coordinates.
func (m *Module) GetBlock(x, y, z int) int32 {
	chunkX, chunkZ := chunks.ChunkPos(x, z)

	m.mu.RLock()
	chunk := m.Chunks[chunkKey(chunkX, chunkZ)]
	m.mu.RUnlock()

	if chunk == nil {
		return 0
	}
	return chunk.GetBlockState(x, y, z)
}

// IsChunkLoaded checks if a chunk is loaded at the given chunk coordinates.
func (m *Module) IsChunkLoaded(chunkX, chunkZ int32) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.Chunks[chunkKey(chunkX, chunkZ)]
	return ok
}

// GetChunk returns the chunk column at the given chunk coordinates.
func (m *Module) GetChunk(chunkX, chunkZ int32) *chunks.ChunkColumn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.Chunks[chunkKey(chunkX, chunkZ)]
}

// GetLoadedChunkCount returns the number of loaded chunks.
func (m *Module) GetLoadedChunkCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.Chunks)
}

// FindBlocks calls fn for every block in loaded chunks whose block ID matches
// one of the given IDs. fn receives the world coordinates and block state ID.
// If fn returns false, iteration stops early.
func (m *Module) FindBlocks(blockIDs []int32, fn func(x, y, z int, stateID int32) bool) {
	idSet := make(map[int32]bool, len(blockIDs))
	for _, id := range blockIDs {
		idSet[id] = true
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, chunk := range m.Chunks {
		for secIdx, sec := range chunk.Sections {
			if sec == nil {
				continue
			}
			baseX := int(chunk.X) * 16
			baseY := chunks.MinY + secIdx*16
			baseZ := int(chunk.Z) * 16
			for lx := range 16 {
				for ly := range 16 {
					for lz := range 16 {
						stateID := sec.GetBlockState(lx, ly, lz)
						if stateID == 0 {
							continue
						}
						blockID, _ := blocks.StateProperties(int(stateID))
						if !idSet[blockID] {
							continue
						}
						if !fn(baseX+lx, baseY+ly, baseZ+lz, stateID) {
							return
						}
					}
				}
			}
		}
	}
}
