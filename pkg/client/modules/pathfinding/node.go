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
	Jump     bool // whether the player must sprint-jump to reach this node
	Parent   *PathNode
	index    int // for heap
}

// danger block names and their cost modifiers
var dangerCosts = map[string]float64{
	"minecraft:magma_block":      50,
	"minecraft:cactus":           50,
	"minecraft:lava":             100,
	"minecraft:sweet_berry_bush": 5,
	"minecraft:powder_snow":      20,
	"minecraft:soul_sand":        2,
	"minecraft:water":            2,
	"minecraft:campfire":         50,
	"minecraft:soul_campfire":    75,
	"minecraft:fire":             100,
	"minecraft:soul_fire":        100,
	"minecraft:wither_rose":      100,
}

// TODO: do not hardcode, fetch from go-mclib/data
const (
	playerWidth          = 0.6
	playerHeight         = 1.8
	playerSneakingHeight = 1.5
	jumpArcPeak          = 1.2522 // peak height of a jump
	safeFallDistance     = 4      // max fall distance without damage (MC safe_fall_distance)
	maxDiagonalGapDepth  = 2      // max gap depth at cardinal positions for diagonal traversal
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

// canPassBetween checks if the player can physically move between two adjacent blocks.
// This catches thin blocks at block edges (doors, fence gates, etc.) that don't
// intersect the player at block center but block traversal at the boundary.
func canPassBetween(col *collisions.Module, cx, cz, nx, ny, nz int, height float64) bool {
	midX := float64(cx+nx)/2.0 + 0.5
	midZ := float64(cz+nz)/2.0 + 0.5
	return col.CanFitAt(midX, float64(ny), midZ, playerWidth, height)
}

// canStepUp checks if the player can step up from cy to cy+1 at block (nx, nz).
// The block at (nx, cy, nz) is the obstacle. For a valid step-up/jump:
//   - If no collision at ground level: always OK (just walking up onto empty space)
//   - If collision ≤ step-up height (0.6): step-up mechanic handles it
//   - If collision > step-up (full block): it's a jump — the block above (nx, cy+1, nz)
//     must NOT also have collision (otherwise it's a 2+ block wall like a door)
func canStepUp(w *world.Module, nx, cy, nz int) bool {
	stepState := w.GetBlock(nx, cy, nz)
	if !block_shapes.HasCollision(stepState) {
		return true
	}

	// check max collision height of the step block
	shapes := block_shapes.CollisionShape(stepState)
	maxY := 0.0
	for _, s := range shapes {
		if s.MaxY > maxY {
			maxY = s.MaxY
		}
	}

	if maxY <= collisions.StepUpHeight {
		return true // short enough to step over
	}

	// too tall for step-up — needs a jump
	// reject if the block above also has collision (2-block obstacle like a door)
	aboveState := w.GetBlock(nx, cy+1, nz)
	return !block_shapes.HasCollision(aboveState)
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

// canJumpTo checks if the player can sprint-jump from (sx, sy, sz) to (dx, dy, dz)
// across a gap. The jump path must be along a single cardinal axis.
func canJumpTo(w *world.Module, col *collisions.Module, sx, sy, sz, dx, dy, dz int) bool {
	// source must have solid ground
	if !block_shapes.HasCollision(w.GetBlock(sx, sy-1, sz)) {
		return false
	}

	// destination must be standable
	if !canStandAt(w, col, dx, dy, dz) {
		return false
	}

	stepX := sign(dx - sx)
	stepZ := sign(dz - sz)
	dist := iabs(dx-sx) + iabs(dz-sz)

	// clearance height: playerHeight + jumpArcPeak (~3.05 blocks)
	clearHeight := playerHeight + jumpArcPeak
	minY := min(sy, dy)

	// source column must have clearance for the full jump arc
	if !col.CanFitAt(float64(sx)+0.5, float64(minY), float64(sz)+0.5, playerWidth, clearHeight) {
		return false
	}

	// each intermediate column must be clear
	for i := 1; i < dist; i++ {
		ix := sx + stepX*i
		iz := sz + stepZ*i
		if !col.CanFitAt(float64(ix)+0.5, float64(minY), float64(iz)+0.5, playerWidth, clearHeight) {
			return false
		}
	}

	return true
}

// canDiagonalTraverse checks if diagonal movement is safe when one or both
// cardinal components are not standable. The player's width (0.6) means they
// maintain partial overlap with solid blocks during diagonal movement.
// Allows the diagonal if the gaps at cardinal positions are shallow enough
// to recover from (player can jump out).
func canDiagonalTraverse(w *world.Module, col *collisions.Module, cx, cy, cz, ox, oz int) bool {
	// fast path: both cardinal components standable (original behavior)
	if canStandAt(w, col, cx+ox, cy, cz) && canStandAt(w, col, cx, cy, cz+oz) {
		return true
	}

	// for each non-standable cardinal position, check the gap is recoverable
	for _, pos := range [2][2]int{{cx + ox, cz}, {cx, cz + oz}} {
		bx, bz := pos[0], pos[1]
		if canStandAt(w, col, bx, cy, bz) {
			continue
		}
		// check if the player can stand within maxDiagonalGapDepth blocks below
		recoverable := false
		for d := 1; d <= maxDiagonalGapDepth; d++ {
			if canStandAt(w, col, bx, cy-d, bz) {
				recoverable = true
				break
			}
		}
		if !recoverable {
			return false
		}
	}

	// verify AABB fits at the diagonal midpoint (no head collisions from blocks)
	midX := float64(cx) + float64(ox)*0.5 + 0.5
	midZ := float64(cz) + float64(oz)*0.5 + 0.5
	return col.CanFitAt(midX, float64(cy), midZ, playerWidth, playerHeight)
}

func sign(x int) int {
	if x > 0 {
		return 1
	}
	if x < 0 {
		return -1
	}
	return 0
}

func iabs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
