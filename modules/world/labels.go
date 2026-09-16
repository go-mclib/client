package world

import (
	"strings"
	"unicode/utf8"

	"github.com/go-mclib/data/blocks"
	"github.com/go-mclib/data/registries"
	"github.com/go-mclib/nbt"
)

// ContainerLabels returns owned text snapshots from ordinary wall signs whose
// supporting block is exactly this loaded container. Standing and hanging signs
// never label nearby containers. Unavailable text is omitted. FindBlocks also
// reports incomplete coverage when a sign cannot be fully observed/decoded.
func (m *Module) ContainerLabels(x, y, z int) []SignLabel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	labels, _ := m.containerLabelsLocked(x, y, z)
	return labels
}

func isContainer(name string) bool {
	switch name {
	case "minecraft:chest", "minecraft:trapped_chest", "minecraft:ender_chest", "minecraft:copper_chest",
		"minecraft:barrel", "minecraft:hopper", "minecraft:dispenser", "minecraft:dropper",
		"minecraft:furnace", "minecraft:blast_furnace", "minecraft:smoker", "minecraft:brewing_stand",
		"minecraft:crafter", "minecraft:chiseled_bookshelf", "minecraft:decorated_pot",
		"minecraft:jukebox", "minecraft:lectern", "minecraft:campfire", "minecraft:soul_campfire",
		"minecraft:shulker_box":
		return true
	}
	return strings.HasSuffix(name, "_shulker_box") || strings.HasSuffix(name, "_shelf") || strings.HasSuffix(name, "_copper_chest")
}

func (m *Module) loadedBlockLocked(x, y, z int) (int32, bool) {
	column := m.chunks[ChunkKey(int32(x>>4), int32(z>>4))]
	if column == nil {
		return 0, false
	}
	index := column.SectionIndex(y)
	if index < 0 || column.Sections[index] == nil || column.Sections[index].BlockStates == nil {
		return 0, false
	}
	return column.GetBlockState(x, y, z), true
}

func (m *Module) containerLabelsLocked(x, y, z int) ([]SignLabel, bool) {
	state, known := m.loadedBlockLocked(x, y, z)
	if !known {
		return nil, false
	}
	if !isContainer(blocks.BlockName(blocks.StateBlockID(state))) {
		return nil, true
	}
	var labels []SignLabel
	complete := true
	// XYZ ordering, including signs crossing negative chunk coordinates.
	for _, direction := range []struct {
		dx, dz int
		facing string
	}{
		{-1, 0, "west"}, {0, -1, "north"}, {0, 1, "south"}, {1, 0, "east"},
	} {
		sx, sz := x+direction.dx, z+direction.dz
		state, known := m.loadedBlockLocked(sx, y, sz)
		if !known {
			complete = false
			continue
		}
		name := blocks.BlockName(blocks.StateBlockID(state))
		if !strings.HasSuffix(name, "_wall_sign") {
			continue
		}
		_, props := blocks.StateProperties(int(state))
		// Vanilla WallSignBlock supports on facing.getOpposite().
		if props["facing"] != direction.facing {
			continue
		}
		entity := m.blockEntities[[3]int{sx, y, sz}]
		if entity == nil || entity.Type != registries.BlockEntityType.Get("minecraft:sign") {
			complete = false
			continue
		}
		front, frontComplete := signLines(entity.Data, "front_text")
		back, backComplete := signLines(entity.Data, "back_text")
		complete = complete && frontComplete && backComplete
		labels = append(labels, SignLabel{X: sx, Y: y, Z: sz, Front: front, Back: back})
	}
	return labels, complete
}

// SignText.DIRECT_CODEC contains four ComponentSerialization.CODEC messages per
// side. Current protocol strings are literal NBT strings, NOT JSON to execute or
// reinterpret. Only literal text, component lists and visible extra siblings
// are decoded; locale-dependent/dynamic component content is left unresolved.
func signLines(data nbt.Compound, side string) ([]string, bool) {
	lines := make([]string, 4)
	text, ok := data[side].(nbt.Compound)
	if !ok {
		return lines, false
	}
	messages, ok := text["messages"].(nbt.List)
	if !ok {
		return lines, false
	}
	complete := len(messages.Elements) == 4
	for i := range min(4, len(messages.Elements)) {
		decoder := signTextDecoder{remaining: 256, complete: true}
		decoder.component(unwrapListElement(messages.Elements[i]), 0)
		lines[i] = decoder.text.String()
		complete = complete && decoder.complete
	}
	return lines, complete
}

// Modern vanilla ListTag wraps heterogeneous list elements in a single-key
// compound on the wire. Unwrap exactly once, and only at list boundaries.
func unwrapListElement(tag nbt.Tag) nbt.Tag {
	if compound, ok := tag.(nbt.Compound); ok && len(compound) == 1 {
		if child, exists := compound[""]; exists {
			return child
		}
	}
	return tag
}

const maxSignLineBytes = 1024

type signTextDecoder struct {
	text      strings.Builder
	remaining int
	complete  bool
}

func (d *signTextDecoder) append(text string) {
	available := maxSignLineBytes - d.text.Len()
	if len(text) > available {
		d.complete = false
		end := available
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		text = text[:end]
	}
	d.text.WriteString(text)
}

func (d *signTextDecoder) component(tag nbt.Tag, depth int) {
	if depth > 16 || d.remaining <= 0 || d.text.Len() >= maxSignLineBytes {
		d.complete = false
		return
	}
	d.remaining--
	switch value := tag.(type) {
	case nbt.String:
		d.append(string(value))
	case nbt.List:
		for _, child := range value.Elements {
			if d.remaining <= 0 || d.text.Len() >= maxSignLineBytes {
				d.complete = false
				break
			}
			d.component(unwrapListElement(child), depth+1)
		}
	case nbt.Compound:
		text, literal := value["text"].(nbt.String)
		kind, typed := value["type"].(nbt.String)
		if literal && (!typed || kind == "text") {
			d.append(string(text))
		} else {
			d.complete = false
		}
		if extra, exists := value["extra"]; exists {
			if list, ok := extra.(nbt.List); ok {
				d.component(list, depth+1)
			} else {
				d.complete = false
			}
		}
		// Style, click_event, hover_event and insertion are deliberately ignored.
	default:
		d.complete = false
	}
}
