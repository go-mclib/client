package world

import (
	"github.com/go-mclib/data/chunks"
)

// GetBlock returns the block state ID at the given world coordinates.
func (m *Module) GetBlock(x, y, z int) int32 {
	chunkX, chunkZ := chunks.ChunkPos(x, z)

	m.mu.RLock()
	defer m.mu.RUnlock()
	chunk := m.chunks[ChunkKey(chunkX, chunkZ)]

	if chunk == nil {
		return 0
	}
	return chunk.GetBlockState(x, y, z)
}

// GetLoadedBlock returns a block state only when its chunk and vertical section
// are loaded. Unlike GetBlock, it distinguishes unavailable data from air.
func (m *Module) GetLoadedBlock(x, y, z int) (stateID int32, known bool) {
	chunkX, chunkZ := chunks.ChunkPos(x, z)
	m.mu.RLock()
	defer m.mu.RUnlock()
	chunk := m.chunks[ChunkKey(chunkX, chunkZ)]
	if chunk == nil {
		return 0, false
	}
	index := chunk.SectionIndex(y)
	if index < 0 || index >= len(chunk.Sections) || chunk.Sections[index] == nil {
		return 0, false
	}
	return chunk.GetBlockState(x, y, z), true
}

// IsChunkLoaded checks if a chunk is loaded at the given chunk coordinates.
func (m *Module) IsChunkLoaded(chunkX, chunkZ int32) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.chunks[ChunkKey(chunkX, chunkZ)]
	return ok
}

// GetChunk returns the chunk column at the given chunk coordinates.
func (m *Module) GetChunk(chunkX, chunkZ int32) *chunks.ChunkColumn {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.chunks[ChunkKey(chunkX, chunkZ)]
}

// GetLoadedChunkCount returns the number of loaded chunks.
func (m *Module) GetLoadedChunkCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.chunks)
}

// GetBlockEntity returns the block entity data at the given position, or nil.
func (m *Module) GetBlockEntity(x, y, z int) *BlockEntityData {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.blockEntities[[3]int{x, y, z}]
}
