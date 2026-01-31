package client

import (
	"testing"
)

func TestPalettedContainerSingleValue(t *testing.T) {
	// Create a single-value paletted container (all blocks the same)
	container := &PalettedContainer{
		BitsPerEntry: 0,
		SingleValue:  42, // Some block state ID
	}

	// All positions should return the single value
	for x := range 16 {
		for y := range 16 {
			for z := range 16 {
				got := container.GetBlockState(x, y, z)
				if got != 42 {
					t.Errorf("GetBlockState(%d, %d, %d) = %d, want 42", x, y, z, got)
				}
			}
		}
	}
}

func TestPalettedContainerSetBlockExpands(t *testing.T) {
	// Create a single-value container
	container := &PalettedContainer{
		BitsPerEntry: 0,
		SingleValue:  0, // Air
	}

	// Setting a different block should expand the palette
	container.SetBlockState(5, 5, 5, 100)

	// Should have expanded to 4 bits per entry
	if container.BitsPerEntry != 4 {
		t.Errorf("BitsPerEntry = %d, want 4 after expansion", container.BitsPerEntry)
	}

	// Palette should have both values
	if len(container.Palette) != 2 {
		t.Errorf("Palette len = %d, want 2", len(container.Palette))
	}

	// The new block should be retrievable
	got := container.GetBlockState(5, 5, 5)
	if got != 100 {
		t.Errorf("GetBlockState(5, 5, 5) = %d, want 100", got)
	}

	// Original blocks should still be 0
	got = container.GetBlockState(0, 0, 0)
	if got != 0 {
		t.Errorf("GetBlockState(0, 0, 0) = %d, want 0", got)
	}
}

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
		// Extract the coordinates back
		gotX := int32(key >> 32)
		gotZ := int32(key)
		if gotX != tt.x || gotZ != tt.z {
			t.Errorf("chunkKey(%d, %d) roundtrip failed: got (%d, %d)", tt.x, tt.z, gotX, gotZ)
		}
	}
}

func TestWorldStoreGetSetBlock(t *testing.T) {
	store := &WorldStore{
		Chunks: make(map[int64]*ChunkColumn),
	}

	// Create a test chunk at (0, 0)
	column := &ChunkColumn{
		X: 0,
		Z: 0,
	}
	// Create a section at Y=64 (section index = (64+64)/16 = 8)
	column.Sections[8] = &ChunkSection{
		BlockStates: &PalettedContainer{
			BitsPerEntry: 0,
			SingleValue:  0, // Air
		},
	}
	store.Chunks[chunkKey(0, 0)] = column

	// Get block at (8, 64, 8) - should be air
	got := store.GetBlock(8, 64, 8)
	if got != 0 {
		t.Errorf("GetBlock(8, 64, 8) = %d, want 0", got)
	}

	// Set block at (8, 64, 8)
	store.setBlock(8, 64, 8, 1) // Stone

	// Should now be stone
	got = store.GetBlock(8, 64, 8)
	if got != 1 {
		t.Errorf("GetBlock(8, 64, 8) = %d, want 1 after SetBlock", got)
	}
}

func TestWorldStoreGetBlockUnloadedChunk(t *testing.T) {
	store := &WorldStore{
		Chunks: make(map[int64]*ChunkColumn),
	}

	// Get block from unloaded chunk should return 0
	got := store.GetBlock(1000, 64, 1000)
	if got != 0 {
		t.Errorf("GetBlock for unloaded chunk = %d, want 0", got)
	}
}

func TestWorldStoreIsChunkLoaded(t *testing.T) {
	store := &WorldStore{
		Chunks: make(map[int64]*ChunkColumn),
	}

	// Chunk not loaded
	if store.IsChunkLoaded(0, 0) {
		t.Error("IsChunkLoaded(0, 0) = true, want false")
	}

	// Add chunk
	store.Chunks[chunkKey(0, 0)] = &ChunkColumn{X: 0, Z: 0}

	// Chunk should now be loaded
	if !store.IsChunkLoaded(0, 0) {
		t.Error("IsChunkLoaded(0, 0) = false, want true")
	}
}

func TestWorldStoreClear(t *testing.T) {
	store := &WorldStore{
		Chunks: make(map[int64]*ChunkColumn),
	}

	// Add some chunks
	store.Chunks[chunkKey(0, 0)] = &ChunkColumn{X: 0, Z: 0}
	store.Chunks[chunkKey(1, 1)] = &ChunkColumn{X: 1, Z: 1}

	if store.GetLoadedChunkCount() != 2 {
		t.Errorf("GetLoadedChunkCount() = %d, want 2", store.GetLoadedChunkCount())
	}

	// Clear
	store.Clear()

	if store.GetLoadedChunkCount() != 0 {
		t.Errorf("GetLoadedChunkCount() after Clear() = %d, want 0", store.GetLoadedChunkCount())
	}
}
