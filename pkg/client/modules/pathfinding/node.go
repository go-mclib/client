package pathfinding

import (
	"github.com/go-mclib/client/pkg/client/modules/collisions"
	"github.com/go-mclib/client/pkg/client/modules/entities"
	"github.com/go-mclib/client/pkg/client/modules/world"
	"github.com/go-mclib/data/pkg/data/blocks"
	block_shapes "github.com/go-mclib/data/pkg/data/hitboxes/blocks"
)

// PathNode represents a node in the A* search.
type PathNode struct {
	X, Y, Z  int
	G, H, F  float64
	Sneaking bool // whether the player must crouch at this node
	Parent   *PathNode
	index    int // for heap
}

// danger block names and their cost modifiers
var dangerCosts = map[string]float64{
	"minecraft:magma_block":      100,
	"minecraft:cactus":           100,
	"minecraft:lava":             50,
	"minecraft:sweet_berry_bush": 5,
	"minecraft:powder_snow":      20,
	"minecraft:soul_sand":        3,
	"minecraft:water":            2,
	"minecraft:campfire":         50,
	"minecraft:soul_campfire":    50,
	"minecraft:fire":             100,
	"minecraft:soul_fire":        100,
	"minecraft:wither_rose":      50,
}

const (
	playerWidth          = 0.6
	playerHeight         = 1.8
	playerSneakingHeight = 1.5
)

// canStandAt checks if the player can stand at the given block position.
func canStandAt(w *world.Module, col *collisions.Module, x, y, z int) bool {
	return canStandAtHeight(w, col, x, y, z, playerHeight)
}

// canStandAtSneaking checks if the player can stand at the position while crouching.
func canStandAtSneaking(w *world.Module, col *collisions.Module, x, y, z int) bool {
	return canStandAtHeight(w, col, x, y, z, playerSneakingHeight)
}

func canStandAtHeight(w *world.Module, col *collisions.Module, x, y, z int, height float64) bool {
	// need solid ground below
	belowState := w.GetBlock(x, y-1, z)
	if !block_shapes.HasCollision(belowState) {
		return false
	}

	// feet and head must be passable
	feetState := w.GetBlock(x, y, z)
	headState := w.GetBlock(x, y+1, z)

	// fast path: both air-like
	if !block_shapes.HasCollision(feetState) && !block_shapes.HasCollision(headState) {
		return true
	}

	// either has collision — check with AABB at the given height
	return col.CanFitAt(float64(x)+0.5, float64(y), float64(z)+0.5, playerWidth, height)
}

// moveCost returns the cost of moving to the given position.
// Returns -1 if impassable. Sets sneaking to true if crouching is required.
func moveCost(w *world.Module, col *collisions.Module, ents *entities.Module, x, y, z int) (float64, bool) {
	if canStandAt(w, col, x, y, z) {
		return moveCostInner(w, ents, x, y, z, false), false
	}
	// try sneaking (lower hitbox)
	if canStandAtSneaking(w, col, x, y, z) {
		return moveCostInner(w, ents, x, y, z, true), true
	}
	return -1, false
}

func moveCostInner(w *world.Module, ents *entities.Module, x, y, z int, sneaking bool) float64 {
	cost := 1.0
	if sneaking {
		cost += 1.0 // slight penalty for crouching paths
	}

	// danger costs from the block at feet
	feetState := w.GetBlock(x, y, z)
	cost += blockDangerCost(feetState)

	// danger from block below (magma, campfire)
	belowState := w.GetBlock(x, y-1, z)
	cost += blockDangerCost(belowState)

	// check adjacent blocks for lava
	for _, offset := range [][3]int{{1, 0, 0}, {-1, 0, 0}, {0, 0, 1}, {0, 0, -1}} {
		adjState := w.GetBlock(x+offset[0], y+offset[1], z+offset[2])
		adjBlockID, _ := blocks.StateProperties(int(adjState))
		adjName := blocks.BlockName(adjBlockID)
		if adjName == "minecraft:lava" {
			cost += 50
		}
	}

	// entity avoidance
	if ents != nil {
		nearby := ents.GetNearbyEntities(float64(x)+0.5, float64(y), float64(z)+0.5, 3.0)
		cost += float64(len(nearby)) * 20
	}

	return cost
}

func blockDangerCost(stateID int32) float64 {
	if stateID == 0 {
		return 0
	}
	blockID, _ := blocks.StateProperties(int(stateID))
	name := blocks.BlockName(blockID)
	if c, ok := dangerCosts[name]; ok {
		return c
	}
	return 0
}
