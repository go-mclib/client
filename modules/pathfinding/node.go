package pathfinding

import (
	"math"

	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/entities"
	"github.com/go-mclib/client/modules/world"
	"github.com/go-mclib/data/blocks"
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

// standSurface returns the height at which the player's feet rest when standing
// at node (x,y,z), or ok=false if the node isn't standable. The surface may be
// fractional for partial blocks (slabs, stairs, snow layers, dirt paths).
func standSurface(col *collisions.Module, x, y, z int, height float64, cfg simulationConfig) (float64, bool) {
	fy, ok := col.SupportSurfaceY(x, y, z, cfg.width)
	if !ok {
		return 0, false
	}
	// feet (resting on the surface) and head must fit above it
	if !col.CanFitAt(float64(x)+0.5, fy, float64(z)+0.5, cfg.width, height) {
		return 0, false
	}
	return fy, true
}

// nodeSurfaceY returns the feet surface height for an (assumed standable) node,
// falling back to the integer block Y if it isn't standable.
func nodeSurfaceY(col *collisions.Module, x, y, z int, cfg simulationConfig) float64 {
	if fy, ok := standSurface(col, x, y, z, cfg.height, cfg); ok {
		return fy
	}
	if fy, ok := standSurface(col, x, y, z, cfg.sneakingHeight, cfg); ok {
		return fy
	}
	return float64(y)
}

// canStandAt checks if the player can stand at the given block position,
// accounting for partial-block support surfaces (slabs, stairs, etc.).
func canStandAt(w *world.Module, col *collisions.Module, x, y, z int, cfg simulationConfig) bool {
	if !nodeLoaded(w, x, y, z, cfg) {
		return false
	}
	_, ok := standSurface(col, x, y, z, cfg.height, cfg)
	return ok
}

func canStandAtHeight(col *collisions.Module, x, y, z int, height float64, cfg simulationConfig) bool {
	_, ok := standSurface(col, x, y, z, height, cfg)
	return ok
}

// moveCost returns the cost of moving to the given position, plus the surface
// height the player would rest at. Returns cost -1 if impassable. Sets sneaking
// to true if crouching is required.
func moveCost(w *world.Module, col *collisions.Module, ents *entities.Module, x, y, z int, cfg simulationConfig) (cost float64, sneaking bool, surfaceY float64) {
	if !nodeLoaded(w, x, y, z, cfg) {
		return -1, false, 0
	}
	if fy, ok := standSurface(col, x, y, z, cfg.height, cfg); ok {
		return moveCostInner(w, ents, x, y, z, false), false, fy
	}
	if fy, ok := standSurface(col, x, y, z, cfg.sneakingHeight, cfg); ok {
		return moveCostInner(w, ents, x, y, z, true), true, fy
	}
	return -1, false, 0
}

// Unknown sections are not air. Include tall-shape origins and full headroom.
func nodeLoaded(w *world.Module, x, y, z int, cfg simulationConfig) bool {
	return terrainLoaded(w, float64(x)+0.5, float64(y), float64(z)+0.5, cfg.height+1, cfg)
}

func terrainLoaded(w *world.Module, x, y, z, height float64, cfg simulationConfig) bool {
	half := cfg.width / 2
	for bx := int(math.Floor(x - half)); bx <= int(math.Floor(x+half)); bx++ {
		for bz := int(math.Floor(z - half)); bz <= int(math.Floor(z+half)); bz++ {
			for by := int(math.Floor(y)) - 1; by <= int(math.Floor(y+height)); by++ {
				state, loaded := w.GetLoadedBlock(bx, by, bz)
				if !loaded {
					return false
				}
				// Their collision depends on descent/equipment, not just state.
				switch blocks.BlockName(blocks.StateBlockID(state)) {
				case "minecraft:scaffolding", "minecraft:powder_snow":
					return false
				}
			}
		}
	}
	return true
}

func moveCostInner(w *world.Module, ents *entities.Module, x, y, z int, sneaking bool) float64 {
	var cost float64
	if sneaking {
		cost = sneakOneBlockCost
	} else {
		cost = sprintOneBlockCost
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

// canPassBetween checks the whole walking edge with the live collision resolver.
// Short sweeps allow both half-steps of a stair, but never lift the player over
// a wall merely because its destination and midpoint have headroom.
func canPassBetween(w *world.Module, col *collisions.Module, cx, cz, nx, nz int, srcFy, destFy, height float64, cfg simulationConfig) bool {
	x, y, z := float64(cx)+0.5, srcFy, float64(cz)+0.5
	dx, dz := float64(nx-cx), float64(nz-cz)
	steps := int(math.Ceil(math.Max(math.Abs(dx), math.Abs(dz)) * 4))
	if steps == 0 {
		return math.Abs(destFy-srcFy) <= collisions.Epsilon
	}
	dx, dz = dx/float64(steps), dz/float64(steps)
	onGround := col.IsOnGround(x, y, z, cfg.width)
	for range steps {
		if !terrainLoaded(w, x+dx, y, z+dz, height+cfg.maxStep, cfg) {
			return false
		}
		ax, ay, az, _, _ := col.CollideMovement(x, y, z, cfg.width, height, dx, 0, dz, onGround, cfg.maxStep)
		if math.Abs(ax-dx) > collisions.Epsilon || math.Abs(az-dz) > collisions.Epsilon {
			return false
		}
		x, y, z = x+ax, y+ay, z+az
		onGround = col.IsOnGround(x, y, z, cfg.width)
	}
	if !onGround && cfg.gravity <= 0 {
		return false
	}
	return y+collisions.Epsilon >= destFy && col.CanFitAt(x, y, z, cfg.width, height)
}

// canDiagonalTraverse checks if diagonal movement is safe.
func canDiagonalTraverse(w *world.Module, col *collisions.Module, cx, cy, cz, ox, oz int, cfg simulationConfig) bool {
	const maxDiagGapDepth = 2

	// fast path: both cardinal components standable
	if canStandAt(w, col, cx+ox, cy, cz, cfg) && canStandAt(w, col, cx, cy, cz+oz, cfg) {
		return true
	}

	// for each non-standable cardinal position, check gap is recoverable
	for _, pos := range [2][2]int{{cx + ox, cz}, {cx, cz + oz}} {
		bx, bz := pos[0], pos[1]
		if canStandAt(w, col, bx, cy, bz, cfg) {
			continue
		}
		recoverable := false
		for d := 1; d <= maxDiagGapDepth; d++ {
			if canStandAt(w, col, bx, cy-d, bz, cfg) {
				recoverable = true
				break
			}
		}
		if !recoverable {
			return false
		}
	}

	// verify AABB fits at the diagonal midpoint, at the player's standing surface
	midX := float64(cx) + float64(ox)*0.5 + 0.5
	midZ := float64(cz) + float64(oz)*0.5 + 0.5
	return col.CanFitAt(midX, nodeSurfaceY(col, cx, cy, cz, cfg), midZ, cfg.width, cfg.height)
}

// FindReachablePosition finds the standable position closest to (fromX, fromY, fromZ)
// that has line-of-sight to (bx, by, bz) within reach distance.
func FindReachablePosition(col *collisions.Module, fromX, fromY, fromZ float64, bx, by, bz int, reach float64) (int, int, int, bool) {
	standX, standY, standZ, _, found := FindBestReachPosition(col, fromX, fromY, fromZ, [][3]int{{bx, by, bz}}, reach)
	if !found {
		return 0, 0, 0, false
	}
	return standX, standY, standZ, true
}

// FindBestReachPosition finds the standable position from which the most targets
// are reachable (within reach distance with line-of-sight). Among positions covering
// the same number of targets, prefers the one closest to (fromX, fromY, fromZ).
// Returns the stand position and the subset of targets reachable from it.
func FindBestReachPosition(col *collisions.Module,
	fromX, fromY, fromZ float64,
	targets [][3]int,
	reach float64,
) (standX, standY, standZ int, reachable [][3]int, found bool) {
	cfg := defaultSimulationConfig()
	if len(targets) == 0 {
		return 0, 0, 0, nil, false
	}

	r := int(math.Ceil(reach))

	// collect unique candidate standable positions around all targets
	candidates := make(map[[3]int]bool)
	for _, t := range targets {
		for dx := -r; dx <= r; dx++ {
			for dz := -r; dz <= r; dz++ {
				for dy := -r; dy <= r; dy++ {
					pos := [3]int{t[0] + dx, t[1] + dy, t[2] + dz}
					if candidates[pos] {
						continue
					}
					if canStandAtHeight(col, pos[0], pos[1], pos[2], cfg.height, cfg) {
						candidates[pos] = true
					}
				}
			}
		}
	}

	bestCount := 0
	bestFromDist := math.MaxFloat64

	for pos := range candidates {
		eyeX := float64(pos[0]) + 0.5
		eyeY := nodeSurfaceY(col, pos[0], pos[1], pos[2], cfg) + eyeHeight
		eyeZ := float64(pos[2]) + 0.5

		// count reachable targets from this position
		count := 0
		for _, t := range targets {
			if canReachBlock(col, eyeX, eyeY, eyeZ, t[0], t[1], t[2], reach) {
				count++
			}
		}
		if count == 0 {
			continue
		}

		// prefer more targets, then closer to the bot
		fdx, fdy, fdz := fromX-eyeX, fromY-eyeY, fromZ-eyeZ
		fromDist := fdx*fdx + fdy*fdy + fdz*fdz
		if count > bestCount || (count == bestCount && fromDist < bestFromDist) {
			bestCount = count
			bestFromDist = fromDist
			standX, standY, standZ = pos[0], pos[1], pos[2]
			found = true
		}
	}

	if !found {
		return 0, 0, 0, nil, false
	}

	// collect which targets are reachable from the chosen position
	eyeX := float64(standX) + 0.5
	eyeY := nodeSurfaceY(col, standX, standY, standZ, cfg) + eyeHeight
	eyeZ := float64(standZ) + 0.5
	for _, t := range targets {
		if canReachBlock(col, eyeX, eyeY, eyeZ, t[0], t[1], t[2], reach) {
			reachable = append(reachable, t)
		}
	}
	return standX, standY, standZ, reachable, true
}

// canReachBlock checks if a position (eye coords) can interact with a block
// at (bx,by,bz) — within reach distance and with clear line of sight.
func canReachBlock(col *collisions.Module, eyeX, eyeY, eyeZ float64, bx, by, bz int, reach float64) bool {
	tx := float64(bx) + 0.5
	ty := float64(by) + 0.5
	tz := float64(bz) + 0.5
	dx, dy, dz := eyeX-tx, eyeY-ty, eyeZ-tz
	if dx*dx+dy*dy+dz*dz > reach*reach {
		return false
	}
	if col != nil {
		hit, _, _, _ := col.RaycastBlocks(eyeX, eyeY, eyeZ, tx, ty, tz)
		if hit {
			return false
		}
	}
	return true
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
