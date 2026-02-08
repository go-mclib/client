package physics

import (
	"github.com/go-mclib/data/pkg/data/blocks"
)

// block friction values from Minecraft source (BlockBehaviour.friction)
var blockFriction = map[string]float64{
	"minecraft:ice":         0.98,
	"minecraft:packed_ice":  0.98,
	"minecraft:blue_ice":    0.989,
	"minecraft:slime_block": 0.8,
}

// block speed factors from Minecraft source (BlockBehaviour.speedFactor)
var blockSpeedFactor = map[string]float64{
	"minecraft:soul_sand":   0.4,
	"minecraft:honey_block": 0.4,
}

// precomputed block IDs for fluid detection
var (
	waterBlockID int32
	lavaBlockID  int32
)

func init() {
	waterBlockID = blocks.BlockID("minecraft:water")
	lavaBlockID = blocks.BlockID("minecraft:lava")
}

// GetBlockFriction returns the friction value for a block state.
func GetBlockFriction(stateID int32) float64 {
	blockID, _ := blocks.StateProperties(int(stateID))
	name := blocks.BlockName(blockID)
	if f, ok := blockFriction[name]; ok {
		return f
	}
	return DefaultBlockFriction
}

// GetBlockSpeedFactor returns the speed factor for a block state.
func GetBlockSpeedFactor(stateID int32) float64 {
	blockID, _ := blocks.StateProperties(int(stateID))
	name := blocks.BlockName(blockID)
	if f, ok := blockSpeedFactor[name]; ok {
		return f
	}
	return 1.0
}

// IsWater returns true if the block state is water.
func IsWater(stateID int32) bool {
	blockID, _ := blocks.StateProperties(int(stateID))
	return blockID == waterBlockID
}

// IsLava returns true if the block state is lava.
func IsLava(stateID int32) bool {
	blockID, _ := blocks.StateProperties(int(stateID))
	return blockID == lavaBlockID
}

// IsFluid returns true if the block state is water or lava.
func IsFluid(stateID int32) bool {
	blockID, _ := blocks.StateProperties(int(stateID))
	return blockID == waterBlockID || blockID == lavaBlockID
}
