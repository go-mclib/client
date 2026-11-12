package world

import (
	"slices"

	"github.com/go-mclib/data/chunks"
	"github.com/go-mclib/nbt"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// cloneColumnLocked returns a deep copy of col that shares no mutable state
// with the live column. Caller holds m.mu (read or write).
//
// Paletted containers are copied with their own Clone. LightData keeps its
// bit sets unexported, so light is copied through its lossless wire codec; an
// error there means a corrupt live column and is returned rather than handing
// out a copy that silently differs.
func cloneColumnLocked(col *chunks.Column) (*chunks.Column, error) {
	out := &chunks.Column{
		X:          col.X,
		Z:          col.Z,
		MinY:       col.MinY,
		Sections:   make([]*chunks.Section, len(col.Sections)),
		Heightmaps: make(map[int32][]int64, len(col.Heightmaps)),
	}
	for i, section := range col.Sections {
		if section == nil {
			continue
		}
		out.Sections[i] = cloneSection(section)
	}
	for kind, heights := range col.Heightmaps {
		out.Heightmaps[kind] = slices.Clone(heights)
	}
	if col.BlockEntities != nil {
		out.BlockEntities = make([]ns.BlockEntity, len(col.BlockEntities))
		for i, entity := range col.BlockEntities {
			out.BlockEntities[i] = entity
			if entity.Data != nil {
				out.BlockEntities[i].Data = cloneNBT(entity.Data)
			}
		}
	}
	if col.Light != nil {
		buf := ns.NewWriter()
		if err := col.Light.Encode(buf); err != nil {
			return nil, err
		}
		out.Light = &ns.LightData{}
		if err := out.Light.Decode(ns.NewReader(buf.Bytes())); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func cloneSection(section *chunks.Section) *chunks.Section {
	out := &chunks.Section{BlockCount: section.BlockCount, FluidCount: section.FluidCount}
	if section.BlockStates != nil {
		out.BlockStates = section.BlockStates.Clone()
	}
	if section.Biomes != nil {
		out.Biomes = section.Biomes.Clone()
	}
	return out
}

// cloneNBT deep-copies tag so block-entity snapshots never alias live data.
func cloneNBT(tag nbt.Tag) nbt.Tag {
	switch value := tag.(type) {
	case nbt.Compound:
		out := make(nbt.Compound, len(value))
		for key, child := range value {
			out[key] = cloneNBT(child)
		}
		return out
	case nbt.List:
		out := nbt.List{ElementType: value.ElementType, Elements: make([]nbt.Tag, len(value.Elements))}
		for i, child := range value.Elements {
			out.Elements[i] = cloneNBT(child)
		}
		return out
	case nbt.ByteArray:
		return slices.Clone(value)
	case nbt.IntArray:
		return slices.Clone(value)
	case nbt.LongArray:
		return slices.Clone(value)
	default:
		return tag
	}
}
