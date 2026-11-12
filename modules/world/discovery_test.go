package world

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-mclib/data/blocks"
	"github.com/go-mclib/data/chunks"
	"github.com/go-mclib/data/registries"
	"github.com/go-mclib/nbt"
)

func discoveryState(t *testing.T, name string, overrides map[string]string) int32 {
	t.Helper()
	id := blocks.ID("minecraft:" + name)
	_, props := blocks.StateProperties(blocks.DefaultStateID(id))
	for key, value := range overrides {
		props[key] = value
	}
	state := blocks.StateID(id, props)
	if state < 0 {
		t.Fatalf("missing fixture state %s %v", name, props)
	}
	return state
}

func discoveryColumn(m *Module, cx, cz int32) *chunks.Column {
	column := chunks.NewColumn(cx, cz)
	column.Sections[column.SectionIndex(64)] = chunks.NewEmptySection()
	m.chunks[chunkKey(cx, cz)] = column
	return column
}

func signFixture(front, back nbt.Tag) *BlockEntityData {
	side := func(first nbt.Tag) nbt.Tag {
		return nbt.Compound{"messages": nbt.List{Elements: []nbt.Tag{first, nbt.String(""), nbt.String(""), nbt.String("")}}}
	}
	return &BlockEntityData{Type: registries.BlockEntityType.Get("minecraft:sign"), Data: nbt.Compound{
		"front_text": side(front), "back_text": side(back),
	}}
}

func TestFindBlocksNearestSignedCoordinatesAndCoverage(t *testing.T) {
	m := New()
	column := discoveryColumn(m, -1, -1)
	stone := discoveryState(t, "stone", nil)
	positions := [][3]int{{-8, 70, -9}, {-9, 71, -9}, {-9, 70, -8}, {-9, 70, -10}, {-9, 69, -9}, {-10, 70, -9}, {-7, 70, -9}}
	for _, p := range positions {
		column.SetBlockState(p[0], p[1], p[2], stone)
	}
	q := BlockQuery{X: -8.5, Y: 70.5, Z: -8.5, Radius: 4, Names: []string{"stone"}, Limit: 3}
	result, err := m.FindBlocks(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	want := [][3]int{{-10, 70, -9}, {-9, 69, -9}, {-9, 70, -10}}
	if !result.Complete || !result.Truncated || result.LoadedChunks != 1 || len(result.Blocks) != len(want) {
		t.Fatalf("coverage/limit = %+v", result)
	}
	for i, match := range result.Blocks {
		if [3]int{match.X, match.Y, match.Z} != want[i] || match.Distance != 1 || match.Name != "minecraft:stone" {
			t.Fatalf("match %d = %+v, want %v distance 1", i, match, want[i])
		}
	}
	q.Limit = 50
	all, err := m.FindBlocks(t.Context(), q)
	if err != nil || all.Truncated || len(all.Blocks) != len(positions) {
		t.Fatalf("full result = %+v, %v", all, err)
	}

	// The unknown half of the search must not turn into air matches.
	q = BlockQuery{X: -.5, Y: 70.5, Z: -.5, Radius: 2, Names: []string{"minecraft:air"}, Limit: 50}
	result, err = m.FindBlocks(t.Context(), q)
	if err != nil || result.Complete || result.Truncated || result.LoadedChunks != 1 {
		t.Fatalf("partial result = %+v, %v", result, err)
	}
	for _, match := range result.Blocks {
		if match.X >= 0 || match.Z >= 0 {
			t.Fatalf("unknown block matched: %+v", match)
		}
	}
	if len(result.Blocks) != 16 {
		t.Fatalf("known quarter-sphere air blocks = %d, want 16", len(result.Blocks))
	}
}

type cancelDuringSearch struct {
	context.Context
	checks int
}

func (c *cancelDuringSearch) Err() error {
	c.checks++
	if c.checks >= 4 {
		return context.Canceled
	}
	return nil
}

func TestFindBlocksCancellationAndInvalidQuery(t *testing.T) {
	m := New()
	discoveryColumn(m, 0, 0)
	q := BlockQuery{X: 8.5, Y: 70.5, Z: 8.5, Radius: 4, Names: []string{"air"}, Limit: 5}
	ctx := &cancelDuringSearch{Context: t.Context()}
	result, err := m.FindBlocks(ctx, q)
	if !errors.Is(err, context.Canceled) || result.Complete {
		t.Fatalf("cancelled search = %+v, %v", result, err)
	}
	for _, change := range []func(*BlockQuery){
		func(q *BlockQuery) { q.Radius = math.NaN() },
		func(q *BlockQuery) { q.Radius = 129 },
		func(q *BlockQuery) { q.X = math.Inf(1) },
		func(q *BlockQuery) { q.Names = []string{"not_a_block"} },
		func(q *BlockQuery) { q.Names = nil },
		func(q *BlockQuery) { q.Limit = 0 },
	} {
		invalid := q
		change(&invalid)
		if _, err := m.FindBlocks(t.Context(), invalid); err == nil {
			t.Fatalf("accepted invalid query %+v", invalid)
		}
	}
}

func TestContainerLabelsFacingTextAndSnapshots(t *testing.T) {
	for _, container := range []string{"chest", "hopper"} {
		t.Run(container, func(t *testing.T) {
			m := New()
			column := discoveryColumn(m, -1, -1)
			column.SetBlockState(-9, 70, -9, discoveryState(t, container, nil))
			for _, facing := range []struct {
				x, z int
				name string
			}{{-10, -9, "west"}, {-9, -10, "north"}, {-9, -8, "south"}, {-8, -9, "east"}} {
				column.SetBlockState(facing.x, 70, facing.z, discoveryState(t, "oak_wall_sign", map[string]string{"facing": facing.name}))
				m.blockEntities[[3]int{facing.x, 70, facing.z}] = signFixture(nbt.Compound{
					"text": nbt.String("Iron"), "extra": nbt.List{Elements: []nbt.Tag{nbt.String(" ingots")}},
					"click_event": nbt.Compound{"command": nbt.String("do-not-match")},
				}, nbt.String("BACK LABEL"))
			}
			labels := m.ContainerLabels(-9, 70, -9)
			if len(labels) != 4 || labels[0].Front[0] != "Iron ingots" || labels[0].Back[0] != "BACK LABEL" {
				t.Fatalf("labels = %+v", labels)
			}
			labels[0].Front[0] = "mutated"
			if again := m.ContainerLabels(-9, 70, -9); again[0].Front[0] != "Iron ingots" {
				t.Fatalf("mutable labels leaked: %+v", again)
			}
			q := BlockQuery{X: -8.5, Y: 70.5, Z: -8.5, Radius: 3, Label: "back label", Limit: 5}
			result, err := m.FindBlocks(t.Context(), q)
			if err != nil || !result.Complete || len(result.Blocks) != 1 || result.Blocks[0].Name != "minecraft:"+container {
				t.Fatalf("back match = %+v, %v", result, err)
			}
			result.Blocks[0].Labels[0].Back[0] = "mutated"
			if m.ContainerLabels(-9, 70, -9)[0].Back[0] != "BACK LABEL" {
				t.Fatal("search snapshot aliases sign text")
			}
			q.Label = "do-not-match"
			result, err = m.FindBlocks(t.Context(), q)
			if err != nil || len(result.Blocks) != 0 {
				t.Fatalf("click event matched: %+v, %v", result, err)
			}
			q.Label, q.Names = "iron", []string{"stone"}
			result, err = m.FindBlocks(t.Context(), q)
			if err != nil || len(result.Blocks) != 0 {
				t.Fatalf("names/label conjunction ignored: %+v, %v", result, err)
			}

			// All four cached entities remain, but none is an attached wall sign.
			column.SetBlockState(-10, 70, -9, discoveryState(t, "oak_wall_sign", map[string]string{"facing": "east"}))
			column.SetBlockState(-9, 70, -10, discoveryState(t, "oak_sign", nil))
			column.SetBlockState(-9, 70, -8, discoveryState(t, "oak_wall_hanging_sign", nil))
			column.SetBlockState(-8, 70, -9, discoveryState(t, "stone", nil))
			if labels := m.ContainerLabels(-9, 70, -9); len(labels) != 0 {
				t.Fatalf("unattached/stale signs labeled container: %+v", labels)
			}
		})
	}
}

func TestSignTextBoundsAndIncompleteSearch(t *testing.T) {
	m := New()
	column := discoveryColumn(m, 0, 0)
	column.SetBlockState(8, 70, 8, discoveryState(t, "barrel", nil))
	column.SetBlockState(8, 70, 7, discoveryState(t, "oak_wall_sign", map[string]string{"facing": "north"}))
	m.blockEntities[[3]int{8, 70, 7}] = signFixture(nbt.String(strings.Repeat("界", 1000)), nbt.Compound{"translate": nbt.String("not-visible-literal")})
	q := BlockQuery{X: 8.5, Y: 70.5, Z: 8.5, Radius: 2, Label: "界", Limit: 5}
	result, err := m.FindBlocks(t.Context(), q)
	if err != nil || result.Complete || len(result.Blocks) != 1 {
		t.Fatalf("bounded text search = %+v, %v", result, err)
	}
	line := result.Blocks[0].Labels[0].Front[0]
	if len(line) > maxSignLineBytes || !utf8.ValidString(line) || line != strings.Repeat("界", 341) {
		t.Fatalf("bad UTF-8 truncation: %q", line)
	}
	if result.Blocks[0].Labels[0].Back[0] != "" {
		t.Fatal("translation key treated as visible text")
	}
	q.Label = "not-visible-literal"
	result, err = m.FindBlocks(t.Context(), q)
	if err != nil || result.Complete || len(result.Blocks) != 0 {
		t.Fatalf("unresolved translation matched: %+v, %v", result, err)
	}

	// Cyclic test input also exercises the recursion cap without allocating a
	// maliciously deep tree. Network NBT itself cannot contain cycles.
	cycle := nbt.Compound{"text": nbt.String("x")}
	cycle["extra"] = nbt.List{Elements: []nbt.Tag{cycle}}
	decoder := signTextDecoder{remaining: 256, complete: true}
	decoder.component(cycle, 0)
	if decoder.complete {
		t.Fatal("recursive text was not marked incomplete")
	}
}

func TestBlockEntitySnapshotIsDeep(t *testing.T) {
	m := New()
	entity := signFixture(nbt.String("kept"), nbt.String("back"))
	entity.Data["bytes"] = nbt.ByteArray{1, 2}
	entity.Data["ints"] = nbt.IntArray{3, 4}
	entity.Data["longs"] = nbt.LongArray{5, 6}
	m.blockEntities[[3]int{1, 70, 1}] = entity
	copy := m.GetBlockEntity(1, 70, 1)
	copy.Type = -1
	copy.Data["front_text"].(nbt.Compound)["messages"].(nbt.List).Elements[0] = nbt.String("changed")
	copy.Data["bytes"].(nbt.ByteArray)[0] = 99
	copy.Data["ints"].(nbt.IntArray)[0] = 99
	copy.Data["longs"].(nbt.LongArray)[0] = 99
	if m.GetBlockEntity(1, 70, 1).Type != registries.BlockEntityType.Get("minecraft:sign") {
		t.Fatal("returned snapshot mutated cached entity type")
	}
	if front, _ := signLines(entity.Data, "front_text"); front[0] != "kept" {
		t.Fatal("nested list mutation reached cache")
	}
	if entity.Data["bytes"].(nbt.ByteArray)[0] != 1 || entity.Data["ints"].(nbt.IntArray)[0] != 3 || entity.Data["longs"].(nbt.LongArray)[0] != 5 {
		t.Fatal("array mutation reached cache")
	}
}
