package world

import (
	"sync"
	"testing"

	"github.com/go-mclib/data/chunks"
	"github.com/go-mclib/data/packets"
	"github.com/go-mclib/nbt"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

func TestLoadedBlockDistinguishesUnknownAndAir(t *testing.T) {
	m := New()
	if _, known := m.GetLoadedBlock(0, 64, 0); known {
		t.Fatal("unloaded chunk reported a block")
	}
	column := chunks.NewColumn(0, 0)
	m.chunks[chunkKey(0, 0)] = column
	if _, known := m.GetLoadedBlock(0, 64, 0); known {
		t.Fatal("missing section reported air")
	}
	column.Sections[column.SectionIndex(64)] = chunks.NewEmptySection()
	if state, known := m.GetLoadedBlock(0, 64, 0); !known || state != 0 {
		t.Fatalf("loaded air = %d, %v", state, known)
	}
	if _, known := m.GetLoadedBlock(0, column.MinY-1, 0); known {
		t.Fatal("below-dimension block reported air")
	}
	wire, err := jp.ToWire(&packets.S2CBlockUpdate{Location: ns.Position{X: 0, Y: 64, Z: 0}, BlockID: 1})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 100 {
			m.handleBlockUpdate(wire)
		}
	})
	for range 100 {
		m.GetLoadedBlock(0, 64, 0)
		m.GetBlock(0, 64, 0)
	}
	wg.Wait()
	if state, known := m.GetLoadedBlock(0, 64, 0); !known || state != 1 {
		t.Fatalf("updated block = %d, %v", state, known)
	}
	m.ClearChunks()
	if _, known := m.GetLoadedBlock(0, 64, 0); known {
		t.Fatal("cleared chunk remains visible")
	}
}

func TestGetChunkReturnsIndependentDeepCopy(t *testing.T) {
	m := New()
	column := chunks.NewColumn(0, 0)
	column.Sections[column.SectionIndex(64)] = chunks.NewEmptySection()
	column.Heightmaps[1] = []int64{7}
	column.BlockEntities = []ns.BlockEntity{{Y: 64, Type: 3, Data: nbt.Compound{"k": nbt.IntArray{1}}}}
	column.Light = &ns.LightData{SkyLightArrays: [][]byte{make([]byte, 2048)}}
	m.chunks[chunkKey(0, 0)] = column

	snapshot := m.GetChunk(0, 0)
	if snapshot == nil || snapshot == column || snapshot.GetBlockState(0, 64, 0) != 0 {
		t.Fatalf("snapshot = %p, live = %p", snapshot, column)
	}
	if all := m.GetChunks(); len(all) != 1 || all[0] == column {
		t.Fatal("GetChunks returned live columns")
	}

	// Mutating the copy never reaches the world.
	snapshot.SetBlockState(0, 64, 0, 1)
	snapshot.Heightmaps[1][0] = 9
	snapshot.BlockEntities[0].Data.(nbt.Compound)["k"].(nbt.IntArray)[0] = 9
	snapshot.Light.SkyLightArrays[0][0] = 9
	if m.GetBlock(0, 64, 0) != 0 || column.Heightmaps[1][0] != 7 ||
		column.BlockEntities[0].Data.(nbt.Compound)["k"].(nbt.IntArray)[0] != 1 || column.Light.SkyLightArrays[0][0] != 0 {
		t.Fatal("snapshot aliases live chunk data")
	}

	// Later packets never change an earlier copy.
	before := m.GetChunk(0, 0)
	wire, err := jp.ToWire(&packets.S2CBlockUpdate{Location: ns.Position{X: 0, Y: 64, Z: 0}, BlockID: 2})
	if err != nil {
		t.Fatal(err)
	}
	m.handleBlockUpdate(wire)
	if m.GetBlock(0, 64, 0) != 2 || before.GetBlockState(0, 64, 0) != 0 {
		t.Fatal("block update leaked into an earlier snapshot")
	}
	if m.GetChunk(1, 1) != nil {
		t.Fatal("unloaded chunk returned a snapshot")
	}
}
