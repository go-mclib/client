package pathfinding

import (
	"math"

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
	Sneaking bool    // player must crouch at this node
	Jump     bool    // player must sprint-jump to reach this node
	JumpYaw  float64 // yaw direction for the sprint-jump

	// door interaction: if set, bot must open this door before passing
	DoorX, DoorY, DoorZ int
	InteractDoor        bool

	Parent *PathNode
	index  int // for heap
}

// canStandAt checks if the player can stand at the given block position.
// Uses AABB-based ground check for partial blocks (chests, slabs, etc.).
func canStandAt(_ *world.Module, col *collisions.Module, x, y, z int) bool {
	return canStandAtHeight(col, x, y, z, playerHeight)
}

func canStandAtSneaking(_ *world.Module, col *collisions.Module, x, y, z int) bool {
	return canStandAtHeight(col, x, y, z, playerSneakingHeight)
}

func canStandAtHeight(col *collisions.Module, x, y, z int, height float64) bool {
	cx := float64(x) + 0.5
	cy := float64(y)
	cz := float64(z) + 0.5

	// need solid ground below: use AABB probe to handle partial blocks
	if !col.IsOnGround(cx, cy, cz, playerWidth) {
		return false
	}

	// feet and head must be passable
	return col.CanFitAt(cx, cy, cz, playerWidth, height)
}

// moveCost returns the cost of moving to the given position.
// Returns -1 if impassable. Sets sneaking to true if crouching is required.
func moveCost(w *world.Module, col *collisions.Module, ents *entities.Module, x, y, z int) (float64, bool) {
	if canStandAt(w, col, x, y, z) {
		return moveCostInner(w, ents, x, y, z, false), false
	}
	if canStandAtSneaking(w, col, x, y, z) {
		return moveCostInner(w, ents, x, y, z, true), true
	}
	return -1, false
}

func moveCostInner(w *world.Module, ents *entities.Module, x, y, z int, sneaking bool) float64 {
	var cost float64
	if sneaking {
		cost = SneakOneBlockCost
	} else {
		cost = SprintOneBlockCost
	}

	// danger costs from the block at feet
	feetState := w.GetBlock(x, y, z)
	cost += blockDangerCost(feetState)

	// danger from block below (magma, campfire, ice)
	belowState := w.GetBlock(x, y-1, z)
	cost += blockDangerCost(belowState)

	// adjacent lava
	for _, offset := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
		adjState := w.GetBlock(x+offset[0], y, z+offset[1])
		adjBlockID, _ := blocks.StateProperties(int(adjState))
		if blocks.BlockName(adjBlockID) == "minecraft:lava" {
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
// Checks both the midpoint (for thin blocks at edges) and the destination center.
func canPassBetween(col *collisions.Module, cx, cz, nx, ny, nz int, height float64) bool {
	// check at destination center
	if !col.CanFitAt(float64(nx)+0.5, float64(ny), float64(nz)+0.5, playerWidth, height) {
		return false
	}
	// check at midpoint between source and destination (catches doors, fence gates at block edges)
	midX := float64(cx+nx)/2.0 + 0.5
	midZ := float64(cz+nz)/2.0 + 0.5
	return col.CanFitAt(midX, float64(ny), midZ, playerWidth, height)
}

// canStepUp checks if the player can step up from cy to cy+1 at block (nx, nz).
func canStepUp(w *world.Module, col *collisions.Module, nx, cy, nz int) bool {
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
	// reject if the block above also has collision (2-block obstacle like closed door)
	aboveState := w.GetBlock(nx, cy+1, nz)
	if block_shapes.HasCollision(aboveState) {
		return false
	}

	// verify player fits at the destination above
	return col.CanFitAt(float64(nx)+0.5, float64(cy+1), float64(nz)+0.5, playerWidth, playerHeight)
}

// canDiagonalTraverse checks if diagonal movement is safe.
func canDiagonalTraverse(w *world.Module, col *collisions.Module, cx, cy, cz, ox, oz int) bool {
	const maxDiagGapDepth = 2

	// fast path: both cardinal components standable
	if canStandAt(w, col, cx+ox, cy, cz) && canStandAt(w, col, cx, cy, cz+oz) {
		return true
	}

	// for each non-standable cardinal position, check gap is recoverable
	for _, pos := range [2][2]int{{cx + ox, cz}, {cx, cz + oz}} {
		bx, bz := pos[0], pos[1]
		if canStandAt(w, col, bx, cy, bz) {
			continue
		}
		recoverable := false
		for d := 1; d <= maxDiagGapDepth; d++ {
			if canStandAt(w, col, bx, cy-d, bz) {
				recoverable = true
				break
			}
		}
		if !recoverable {
			return false
		}
	}

	// verify AABB fits at the diagonal midpoint
	midX := float64(cx) + float64(ox)*0.5 + 0.5
	midZ := float64(cz) + float64(oz)*0.5 + 0.5
	return col.CanFitAt(midX, float64(cy), midZ, playerWidth, playerHeight)
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

// yawBetween returns the yaw angle from block (sx,sz) toward (dx,dz).
func yawBetween(sx, sz, dx, dz int) float64 {
	ddx := float64(dx - sx)
	ddz := float64(dz - sz)
	return math.Atan2(ddz, ddx)*180.0/math.Pi - 90.0
}
