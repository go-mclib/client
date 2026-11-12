package world

import (
	"testing"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/chunks"
	"github.com/go-mclib/data/packets"
	"github.com/go-mclib/data/registries"
	"github.com/go-mclib/nbt"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

func TestBlockEntityInvalidationOnBlockPackets(t *testing.T) {
	for _, sectionPacket := range []bool{false, true} {
		name := "single"
		if sectionPacket {
			name = "section"
		}
		t.Run(name, func(t *testing.T) {
			m := New()
			column := discoveryColumn(m, -1, -1)
			column.SetBlockState(-8, 70, -8, discoveryState(t, "chest", nil))
			column.SetBlockState(-8, 70, -9, discoveryState(t, "oak_wall_sign", map[string]string{"facing": "north"}))
			m.blockEntities[[3]int{-8, 70, -9}] = signFixture(nbt.String("retained"), nbt.String("back"))
			update := func(state int32) {
				t.Helper()
				var wire *jp.WirePacket
				var err error
				if sectionPacket {
					sx, sy, sz := int64(-1), int64(4), int64(-1)
					packed := (sx&0x3fffff)<<42 | (sz&0x3fffff)<<20 | sy&0xfffff
					entry := int64(state)<<12 | 8<<8 | 7<<4 | 6
					wire, err = jp.ToWire(&packets.S2CSectionBlocksUpdate{ChunkSectionPosition: ns.Int64(packed), Blocks: ns.PrefixedArray[ns.VarLong]{ns.VarLong(entry)}})
					if err == nil {
						m.handleSectionBlocksUpdate(wire)
					}
				} else {
					wire, err = jp.ToWire(&packets.S2CBlockUpdate{Location: ns.Position{X: -8, Y: 70, Z: -9}, BlockID: ns.VarInt(state)})
					if err == nil {
						m.handleBlockUpdate(wire)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			update(discoveryState(t, "oak_wall_sign", map[string]string{"facing": "north", "waterlogged": "true"}))
			if labels := m.ContainerLabels(-8, 70, -8); len(labels) != 1 || labels[0].Front[0] != "retained" {
				t.Fatalf("same-block property update discarded text: %+v", labels)
			}
			update(discoveryState(t, "oak_wall_sign", map[string]string{"facing": "south"}))
			if m.GetBlockEntity(-8, 70, -9) == nil {
				t.Fatal("facing update discarded entity")
			}
			if labels := m.ContainerLabels(-8, 70, -8); len(labels) != 0 {
				t.Fatalf("wrong-facing label survived: %+v", labels)
			}
			update(discoveryState(t, "spruce_wall_sign", map[string]string{"facing": "north"}))
			if m.GetBlockEntity(-8, 70, -9) != nil {
				t.Fatal("changed sign block retained stale entity")
			}
			m.blockEntities[[3]int{-8, 70, -9}] = signFixture(nbt.String("stale"), nbt.String(""))
			update(discoveryState(t, "stone", nil))
			if m.GetBlockEntity(-8, 70, -9) != nil {
				t.Fatal("non-air replacement retained stale entity")
			}
		})
	}
}

func TestTransientBlockIsVisualOnly(t *testing.T) {
	c := client.New("unused", "test", false)
	client.NewSimulator(c).SetState(jp.StatePlay)
	m := New()
	c.Register(m)
	column := discoveryColumn(m, 0, 0)
	original := discoveryState(t, "oak_wall_sign", nil)
	column.SetBlockState(1, 70, 1, original)
	m.blockEntities[[3]int{1, 70, 1}] = signFixture(nbt.String("retained"), nbt.String(""))
	updates := 0
	m.OnBlockUpdate(func(_, _, _ int, _ int32) { updates++ })
	var visual [4]int
	m.OnTransientBlock(func(x, y, z int, state int32) { visual = [4]int{x, y, z, int(state)} })
	replacement := discoveryState(t, "stone", nil)
	wire, err := jp.ToWire(&packets.S2CAddTransientBlock{Location: ns.Position{X: 1, Y: 70, Z: 1}, BlockState: ns.VarInt(replacement)})
	if err != nil {
		t.Fatal(err)
	}
	m.HandlePacket(wire)
	if visual != [4]int{1, 70, 1, int(replacement)} {
		t.Fatalf("transient event lost: %v", visual)
	}
	if updates != 0 || m.GetBlock(1, 70, 1) != original || m.GetBlockEntity(1, 70, 1) == nil {
		t.Fatal("temporary visual changed authoritative blocks or block entities")
	}
}

func TestChunkReplacementClearsOnlyItsBlockEntities(t *testing.T) {
	m := New()
	old := discoveryColumn(m, -1, -1)
	old.SetBlockState(-1, 70, -1, discoveryState(t, "oak_wall_sign", nil))
	m.blockEntities[[3]int{-1, 70, -1}] = signFixture(nbt.String("stale"), nbt.String(""))
	m.blockEntities[[3]int{0, 70, -1}] = signFixture(nbt.String("neighbor"), nbt.String(""))
	replacement := chunks.NewColumn(-1, -1)
	replacement.SetBlockState(-1, 70, -1, discoveryState(t, "oak_wall_sign", nil))
	data, err := replacement.EncodeSections()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := jp.ToWire(&packets.S2CLevelChunkWithLight{ChunkX: -1, ChunkZ: -1, ChunkData: ns.ChunkData{Data: data}})
	if err != nil {
		t.Fatal(err)
	}
	m.handleChunkData(wire)
	if m.GetBlockEntity(-1, 70, -1) != nil {
		t.Fatal("replacement chunk retained omitted block entity")
	}
	if entity := m.GetBlockEntity(0, 70, -1); entity == nil {
		t.Fatal("replacement cleared adjacent chunk entity")
	}
}

func TestSingleUpdateDoesNotInventLoadedSection(t *testing.T) {
	m := New()
	m.chunks[chunkKey(0, 0)] = chunks.NewColumn(0, 0)
	wire, err := jp.ToWire(&packets.S2CBlockUpdate{Location: ns.Position{X: 8, Y: 70, Z: 8}, BlockID: 1})
	if err != nil {
		t.Fatal(err)
	}
	m.handleBlockUpdate(wire)
	result, err := m.FindBlocks(t.Context(), BlockQuery{X: 8.5, Y: 70.5, Z: 8.5, Radius: 2, Names: []string{"air"}, Limit: 50})
	if err != nil || result.Complete || len(result.Blocks) != 0 {
		t.Fatalf("single update invented air: %+v, %v", result, err)
	}
}

func TestSignPacketMixedComponentListsAcrossChunkBoundary(t *testing.T) {
	m := New()
	containerColumn := discoveryColumn(m, 0, 0)
	signColumn := discoveryColumn(m, -1, 0)
	containerColumn.SetBlockState(0, 70, 8, discoveryState(t, "hopper", nil))
	signColumn.SetBlockState(-1, 70, 8, discoveryState(t, "oak_wall_sign", map[string]string{"facing": "west"}))
	empty := nbt.Compound{"": nbt.String("")}
	messages := nbt.List{ElementType: nbt.TagCompound, Elements: []nbt.Tag{
		nbt.Compound{"text": nbt.String("IRON"), "extra": nbt.List{ElementType: nbt.TagCompound, Elements: []nbt.Tag{
			nbt.Compound{"": nbt.String(" ingots")}, nbt.Compound{"text": nbt.String("!")},
		}}},
		nbt.Compound{"": nbt.String("second line")}, empty, empty,
	}}
	data := nbt.Compound{
		"front_text": nbt.Compound{"messages": messages},
		"back_text": nbt.Compound{"messages": nbt.List{ElementType: nbt.TagString, Elements: []nbt.Tag{
			nbt.String("back"), nbt.String(""), nbt.String(""), nbt.String(""),
		}}},
	}
	wire, err := jp.ToWire(&packets.S2CBlockEntityData{
		Location: ns.Position{X: -1, Y: 70, Z: 8}, Type: ns.VarInt(registries.BlockEntityType.Get("minecraft:sign")), NBTData: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.handleBlockEntityData(wire)
	result, err := m.FindBlocks(t.Context(), BlockQuery{X: .5, Y: 70.5, Z: 8.5, Radius: 2, Label: "iron ingots!", Limit: 5})
	if err != nil || !result.Complete || len(result.Blocks) != 1 {
		t.Fatalf("mixed component packet = %+v, %v", result, err)
	}
	label := result.Blocks[0].Labels[0]
	if label.X != -1 || label.Front[1] != "second line" || label.Back[0] != "back" {
		t.Fatalf("decoded attached label = %+v", label)
	}
}

func TestChunkBlockEntitiesReturnsTrackedEntitiesAsClones(t *testing.T) {
	m := New()
	m.blockEntities[[3]int{-1, 70, -16}] = &BlockEntityData{Type: 7, Data: nbt.Compound{"Text": nbt.String("late sign")}}
	m.blockEntities[[3]int{-16, 64, -1}] = &BlockEntityData{Type: 2, Data: nbt.Compound{"Items": nbt.List{ElementType: nbt.TagCompound}}}
	m.blockEntities[[3]int{0, 64, -1}] = &BlockEntityData{Type: 3, Data: nbt.Compound{}}   // chunk (0, -1)
	m.blockEntities[[3]int{-1, 64, -17}] = &BlockEntityData{Type: 3, Data: nbt.Compound{}} // chunk (-1, -2)

	got := m.ChunkBlockEntities(-1, -1)
	want := []ns.BlockEntity{
		{PackedXZ: 0x0f, Y: 64, Type: 2},
		{PackedXZ: 0xf0, Y: 70, Type: 7},
	}
	if len(got) != len(want) {
		t.Fatalf("ChunkBlockEntities = %+v, want %d entries", got, len(want))
	}
	for i := range want {
		if got[i].PackedXZ != want[i].PackedXZ || got[i].Y != want[i].Y || got[i].Type != want[i].Type {
			t.Fatalf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	got[1].Data.(nbt.Compound)["Text"] = nbt.String("mutated")
	if text := m.blockEntities[[3]int{-1, 70, -16}].Data["Text"]; text != nbt.String("late sign") {
		t.Fatalf("snapshot aliases live NBT: %v", text)
	}
	if got := m.ChunkBlockEntities(5, 5); len(got) != 0 {
		t.Fatalf("empty chunk = %+v", got)
	}
}
