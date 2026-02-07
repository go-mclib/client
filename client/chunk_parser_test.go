package client

import (
	"testing"

	"github.com/go-mclib/data/pkg/data/chunks"
)

func TestChunkKey(t *testing.T) {
	tests := []struct {
		x, z int32
	}{
		{0, 0},
		{1, 1},
		{-1, -1},
		{100, -100},
		{-100, 100},
		{2147483647, 0},
		{0, 2147483647},
		{-2147483648, 0},
		{0, -2147483648},
	}

	for _, tt := range tests {
		key := chunkKey(tt.x, tt.z)
		// extract the coordinates back
		gotX := int32(key >> 32)
		gotZ := int32(key)
		if gotX != tt.x || gotZ != tt.z {
			t.Errorf("chunkKey(%d, %d) roundtrip failed: got (%d, %d)", tt.x, tt.z, gotX, gotZ)
		}
	}
}

func TestWorldStoreGetSetBlock(t *testing.T) {
	store := &WorldStore{
		Chunks: make(map[int64]*chunks.ChunkColumn),
	}

	// create a test chunk at (0, 0) with an empty section at Y=64 (section index 8)
	column := &chunks.ChunkColumn{X: 0, Z: 0}
	column.Sections[8] = chunks.NewEmptySection()
	store.Chunks[chunkKey(0, 0)] = column

	// get block at (8, 64, 8) - should be air
	got := store.GetBlock(8, 64, 8)
	if got != 0 {
		t.Errorf("GetBlock(8, 64, 8) = %d, want 0", got)
	}

	// set block at (8, 64, 8)
	store.setBlock(8, 64, 8, 1) // Stone

	// should now be stone
	got = store.GetBlock(8, 64, 8)
	if got != 1 {
		t.Errorf("GetBlock(8, 64, 8) = %d, want 1 after SetBlock", got)
	}
}

func TestWorldStoreGetBlockUnloadedChunk(t *testing.T) {
	store := &WorldStore{
		Chunks: make(map[int64]*chunks.ChunkColumn),
	}

	// get block from unloaded chunk should return 0
	got := store.GetBlock(1000, 64, 1000)
	if got != 0 {
		t.Errorf("GetBlock for unloaded chunk = %d, want 0", got)
	}
}

func TestWorldStoreIsChunkLoaded(t *testing.T) {
	store := &WorldStore{
		Chunks: make(map[int64]*chunks.ChunkColumn),
	}

	// chunk not loaded
	if store.IsChunkLoaded(0, 0) {
		t.Error("IsChunkLoaded(0, 0) = true, want false")
	}

	// ddd chunk
	store.Chunks[chunkKey(0, 0)] = &chunks.ChunkColumn{X: 0, Z: 0}

	// chunk should now be loaded
	if !store.IsChunkLoaded(0, 0) {
		t.Error("IsChunkLoaded(0, 0) = false, want true")
	}
}

func TestWorldStoreClear(t *testing.T) {
	store := &WorldStore{
		Chunks: make(map[int64]*chunks.ChunkColumn),
	}

	// add some chunks
	store.Chunks[chunkKey(0, 0)] = &chunks.ChunkColumn{X: 0, Z: 0}
	store.Chunks[chunkKey(1, 1)] = &chunks.ChunkColumn{X: 1, Z: 1}

	if store.GetLoadedChunkCount() != 2 {
		t.Errorf("GetLoadedChunkCount() = %d, want 2", store.GetLoadedChunkCount())
	}

	// Clear
	store.Clear()

	if store.GetLoadedChunkCount() != 0 {
		t.Errorf("GetLoadedChunkCount() after Clear() = %d, want 0", store.GetLoadedChunkCount())
	}
}
