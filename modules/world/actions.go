package world

import (
	"cmp"
	"slices"

	"github.com/go-mclib/data/chunks"
	"github.com/go-mclib/nbt"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// GetBlock returns the block state ID at the given world coordinates.
func (m *Module) GetBlock(x, y, z int) int32 {
	chunkX, chunkZ := chunks.ChunkPos(x, z)

	m.mu.RLock()
	defer m.mu.RUnlock()
	chunk := m.chunks[chunkKey(chunkX, chunkZ)]

	if chunk == nil {
		return 0
	}
	return chunk.GetBlockState(x, y, z)
}

// GetLoadedBlock returns a block state only when its chunk and vertical section
// are loaded. Unlike GetBlock, it distinguishes unavailable data from air.
func (m *Module) GetLoadedBlock(x, y, z int) (stateID int32, known bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.loadedBlockLocked(x, y, z)
}

// IsChunkLoaded checks if a chunk is loaded at the given chunk coordinates.
func (m *Module) IsChunkLoaded(chunkX, chunkZ int32) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.chunks[chunkKey(chunkX, chunkZ)]
	return ok
}

// GetChunk returns an independent deep copy of the chunk column at the given
// chunk coordinates, or nil if it is not loaded. Mutating the copy never
// affects the world, and later packets never change the copy.
func (m *Module) GetChunk(chunkX, chunkZ int32) *chunks.Column {
	m.mu.RLock()
	defer m.mu.RUnlock()
	column := m.chunks[chunkKey(chunkX, chunkZ)]
	if column == nil {
		return nil
	}
	return m.snapshotLocked(column)
}

// snapshotLocked deep-copies column, or returns nil (logged) when the live
// column cannot round-trip its own codec. Caller holds m.mu.
func (m *Module) snapshotLocked(column *chunks.Column) *chunks.Column {
	copied, err := cloneColumnLocked(column)
	if err != nil {
		if m.client != nil {
			m.client.Logger.Printf("world: snapshot chunk (%d, %d): %v", column.X, column.Z, err)
		}
		return nil
	}
	return copied
}

// LoadedChunkCount returns the number of loaded chunks.
func (m *Module) LoadedChunkCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.chunks)
}

// GetBlockEntity returns an independent snapshot, or nil if none was observed.
func (m *Module) GetBlockEntity(x, y, z int) *BlockEntityData {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entity := m.blockEntities[[3]int{x, y, z}]
	if entity == nil {
		return nil
	}
	return &BlockEntityData{Type: entity.Type, Data: cloneNBT(entity.Data).(nbt.Compound)}
}

// ChunkBlockEntities returns independent snapshots of every tracked block
// entity in chunk (cx, cz), including ones created after the chunk loaded,
// in chunk-packet form ordered by Y then packed XZ.
func (m *Module) ChunkBlockEntities(cx, cz int32) []ns.BlockEntity {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []ns.BlockEntity
	for pos, entity := range m.blockEntities {
		if int32(pos[0]>>4) != cx || int32(pos[2]>>4) != cz {
			continue
		}
		out = append(out, ns.BlockEntity{
			PackedXZ: ns.Uint8((pos[0]&15)<<4 | pos[2]&15),
			Y:        ns.Int16(pos[1]),
			Type:     ns.VarInt(entity.Type),
			Data:     cloneNBT(entity.Data),
		})
	}
	slices.SortFunc(out, func(a, b ns.BlockEntity) int {
		return cmp.Or(cmp.Compare(a.Y, b.Y), cmp.Compare(a.PackedXZ, b.PackedXZ))
	})
	return out
}
