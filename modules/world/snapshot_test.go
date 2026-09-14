package world

import (
	"sync"
	"testing"

	"github.com/go-mclib/data/chunks"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func TestLoadedBlockDistinguishesUnknownAndAir(t *testing.T) {
	m := New()
	if _, known := m.GetLoadedBlock(0, 64, 0); known {
		t.Fatal("unloaded chunk reported a block")
	}
	column := chunks.NewColumn(0, 0)
	m.chunks[ChunkKey(0, 0)] = column
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
	wire, err := jp.ToWire(&packets.S2CBlockUpdate{Location: ns.Position{X: 0, Y: 64, Z: 0}, BlockId: 1})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			m.handleBlockUpdate(wire)
		}
	}()
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
