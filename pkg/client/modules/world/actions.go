package world

import "github.com/go-mclib/data/pkg/data/chunks"

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
