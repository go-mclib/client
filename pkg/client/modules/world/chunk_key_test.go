package world

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
		gotX := int32(key >> 32)
		gotZ := int32(key)
		if gotX != tt.x || gotZ != tt.z {
			t.Errorf("chunkKey(%d, %d) roundtrip failed: got (%d, %d)", tt.x, tt.z, gotX, gotZ)
		}
	}
}

func TestGetSetBlock(t *testing.T) {
	m := New()

	// create a test chunk at (0, 0) with an empty section at Y=64 (section index 8)
	column := &chunks.ChunkColumn{X: 0, Z: 0}
	column.Sections[8] = chunks.NewEmptySection()
	m.Chunks[chunkKey(0, 0)] = column

	got := m.GetBlock(8, 64, 8)
	if got != 0 {
		t.Errorf("GetBlock(8, 64, 8) = %d, want 0", got)
	}

	// set block via chunk section directly
	column.SetBlockState(8, 64, 8, 1) // stone

	got = m.GetBlock(8, 64, 8)
	if got != 1 {
		t.Errorf("GetBlock(8, 64, 8) = %d, want 1 after SetBlockState", got)
	}
}

func TestGetBlockUnloadedChunk(t *testing.T) {
	m := New()

	got := m.GetBlock(1000, 64, 1000)
	if got != 0 {
		t.Errorf("GetBlock for unloaded chunk = %d, want 0", got)
	}
}

func TestIsChunkLoaded(t *testing.T) {
	m := New()

	if m.IsChunkLoaded(0, 0) {
		t.Error("IsChunkLoaded(0, 0) = true, want false")
	}

	m.Chunks[chunkKey(0, 0)] = &chunks.ChunkColumn{X: 0, Z: 0}

	if !m.IsChunkLoaded(0, 0) {
		t.Error("IsChunkLoaded(0, 0) = false, want true")
	}
}

func TestReset(t *testing.T) {
	m := New()

	m.Chunks[chunkKey(0, 0)] = &chunks.ChunkColumn{X: 0, Z: 0}
	m.Chunks[chunkKey(1, 1)] = &chunks.ChunkColumn{X: 1, Z: 1}

	if m.GetLoadedChunkCount() != 2 {
		t.Errorf("GetLoadedChunkCount() = %d, want 2", m.GetLoadedChunkCount())
	}

	m.Reset()

	if m.GetLoadedChunkCount() != 0 {
		t.Errorf("GetLoadedChunkCount() after Reset() = %d, want 0", m.GetLoadedChunkCount())
	}
}
