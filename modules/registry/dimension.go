package registry

import (
	"github.com/go-mclib/data/chunks"
	"github.com/go-mclib/nbt"
)

// DimensionType holds the height-relevant fields of a dimension_type registry
// entry. Datapacks may change these, so they must be read per server, not
// assumed. The full element is still available via the source Entry.Data.
type DimensionType struct {
	MinY          int32 // lowest buildable Y
	Height        int32 // number of buildable layers
	LogicalHeight int32 // height usable by gameplay (portals, chorus, etc.)
	HasCeiling    bool
	HasSkylight   bool
}

// MaxY returns the exclusive top of the world (build limit): the first Y at
// which blocks can no longer be placed.
func (d DimensionType) MaxY() int32 { return d.MinY + d.Height }

// DimensionType parses the dimension_type registry entry at the given index.
func (m *Module) DimensionType(index int32) (DimensionType, bool) {
	e, ok := m.EntryByIndex(DimensionTypeRegistry, int(index))
	if !ok || e.Data == nil {
		return DimensionType{}, false
	}
	return parseDimensionType(e.Data), true
}

// CurrentDimensionType returns the dimension type of the dimension the player is
// currently in. Requires the self module.
func (m *Module) CurrentDimensionType() (DimensionType, bool) {
	if m.self == nil {
		return DimensionType{}, false
	}
	return m.DimensionType(m.self.DimensionType())
}

// CurrentBounds returns the buildable Y range [minY, maxY) of the current
// dimension, falling back to the vanilla overworld when the registry module is
// absent (nil receiver) or the dimension type is unknown.
func (m *Module) CurrentBounds() (minY, maxY int) {
	if m != nil {
		if dt, ok := m.CurrentDimensionType(); ok && dt.Height > 0 {
			return int(dt.MinY), int(dt.MaxY())
		}
	}
	return chunks.MinY, chunks.MaxY
}

// Biomes returns the namespaced ids of every available biome.
func (m *Module) Biomes() []string {
	return m.EntryIDs(BiomeRegistry)
}

func parseDimensionType(c nbt.Compound) DimensionType {
	return DimensionType{
		MinY:          c.GetInt("min_y"),
		Height:        c.GetInt("height"),
		LogicalHeight: c.GetInt("logical_height"),
		HasCeiling:    c.GetByte("has_ceiling") != 0,
		HasSkylight:   c.GetByte("has_skylight") != 0,
	}
}
