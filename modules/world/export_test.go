package world

import "github.com/go-mclib/data/chunks"

// PutChunk installs a chunk column directly, bypassing packet handling.
func (m *Module) PutChunk(x, z int32, col *chunks.Column) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chunks[chunkKey(x, z)] = col
}
