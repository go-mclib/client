package physics

import (
	"math"

	"github.com/go-mclib/client/pkg/client/modules/world"
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
	waterBlockID        int32
	lavaBlockID         int32
	bubbleColumnBlockID int32
)

func init() {
	waterBlockID = blocks.BlockID("minecraft:water")
	lavaBlockID = blocks.BlockID("minecraft:lava")
	bubbleColumnBlockID = blocks.BlockID("minecraft:bubble_column")
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

func blockSpeedFactorByID(blockID int32) float64 {
	name := blocks.BlockName(blockID)
	if f, ok := blockSpeedFactor[name]; ok {
		return f
	}
	return 1.0
}

// GetBlockSpeedFactorAt returns the speed factor at a world position.
// Matches vanilla Entity.getBlockSpeedFactor():
// 1. check block at feet; if water/bubble_column return its factor
// 2. if feet block factor != 1.0, return it
// 3. otherwise return factor of the block below (y - 0.5)
func GetBlockSpeedFactorAt(w *world.Module, x, y, z float64) float64 {
	bx, by, bz := int(math.Floor(x)), int(math.Floor(y)), int(math.Floor(z))
	feetState := w.GetBlock(bx, by, bz)
	feetBlockID, _ := blocks.StateProperties(int(feetState))

	// water and bubble columns return their own speed factor (1.0)
	if feetBlockID == waterBlockID || feetBlockID == bubbleColumnBlockID {
		return blockSpeedFactorByID(feetBlockID)
	}

	feetFactor := blockSpeedFactorByID(feetBlockID)
	if feetFactor != 1.0 {
		return feetFactor
	}

	// check block below (getBlockPosBelowThatAffectsMyMovement = y - 0.5)
	belowState := w.GetBlock(int(math.Floor(x)), int(math.Floor(y-0.5)), int(math.Floor(z)))
	belowBlockID, _ := blocks.StateProperties(int(belowState))
	return blockSpeedFactorByID(belowBlockID)
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
