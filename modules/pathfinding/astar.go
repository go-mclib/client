package pathfinding

import (
	"container/heap"
	"context"
	"fmt"
	"math"
	"time"

	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/entities"
	"github.com/go-mclib/client/modules/world"
)

const DefaultMaxNodes = 10000

// cardinal neighbor offsets
var cardinalOffsets = [4][2]int{
	{1, 0}, {-1, 0}, {0, 1}, {0, -1},
}

// diagonal neighbor offsets
var diagonalOffsets = [4][2]int{
	{1, 1}, {1, -1}, {-1, 1}, {-1, -1},
}

func findPath(ctx context.Context, w *world.Module, col *collisions.Module, ents *entities.Module,
	startX, startY, startZ, goalX, goalY, goalZ, maxNodes int,
	cfg simulationConfig,
) ([]PathNode, error) {
	return searchPath(ctx, w, col, ents, startX, startY, startZ, goalX, goalY, goalZ, maxNodes, cfg, false)
}

// searchPath returns either the goal or a safe bounded segment. A segment's
// endpoint is not a navigation success; the controller retains the ultimate goal.
func searchPath(ctx context.Context, w *world.Module, col *collisions.Module, ents *entities.Module,
	startX, startY, startZ, goalX, goalY, goalZ, maxNodes int,
	cfg simulationConfig, partial bool,
) ([]PathNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !nodeLoaded(w, startX, startY, startZ, cfg) {
		return nil, fmt.Errorf("pathfinding: start terrain is unloaded")
	}
	if !partial {
		if !nodeLoaded(w, goalX, goalY, goalZ, cfg) {
			return nil, fmt.Errorf("pathfinding: goal terrain is unloaded")
		}
		if _, ok := standSurface(col, goalX, goalY, goalZ, cfg.sneakingHeight, cfg); !ok {
			return nil, fmt.Errorf("pathfinding: goal has no support or headroom")
		}
	}
	if _, ok := standSurface(col, startX, startY, startZ, cfg.sneakingHeight, cfg); !ok {
		return nil, fmt.Errorf("pathfinding: start has no support or headroom")
	}
	start := &PathNode{X: startX, Y: startY, Z: startZ}
	start.H = heuristic(startX, startY, startZ, goalX, goalY, goalZ)
	start.F = start.H

	openSet := &nodeHeap{start}
	heap.Init(openSet)

	// track best known g-cost to each position for proper A* deduplication
	gScore := map[[3]int]float64{
		{startX, startY, startZ}: 0,
	}

	explored := 0
	var best, frontier *PathNode
	bestScore, frontierScore := math.Inf(1), math.Inf(1)
	deadline := time.Now().Add(200 * time.Millisecond)

	for openSet.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current := heap.Pop(openSet).(*PathNode)
		cx, cy, cz := current.X, current.Y, current.Z
		if partial && !navigationLoaded(w, float64(cx)+0.5, nodeSurfaceY(col, cx, cy, cz, cfg), float64(cz)+0.5, cfg) {
			continue
		}

		if cx == goalX && cy == goalY && cz == goalZ {
			return reconstructPath(current), nil
		}

		// skip if this node has been superseded by a cheaper path
		key := [3]int{cx, cy, cz}
		if best, ok := gScore[key]; ok && current.G > best {
			continue
		}
		explored++
		if partial && current.Parent != nil {
			score := current.H + current.G*0.1 + float64(cfg.frontiers[key])*32
			if current.H < start.H-0.5 && score < bestScore {
				best, bestScore = current, score
			}
			boundary := iabs(cx-startX) >= 24 || iabs(cz-startZ) >= 24
			for _, off := range cardinalOffsets {
				if !navigationLoaded(w, float64(cx+off[0])+0.5, nodeSurfaceY(col, cx, cy, cz, cfg), float64(cz+off[1])+0.5, cfg) {
					boundary = true
				}
			}
			if boundary && cfg.frontiers[key] < 2 && score < frontierScore {
				frontier, frontierScore = current, score
			}
			if iabs(cx-startX) >= 24 || iabs(cz-startZ) >= 24 {
				continue
			}
		}
		if explored >= maxNodes || partial && time.Now().After(deadline) {
			break
		}

		// generate all movement types
		tryCardinalMoves(w, col, ents, current, goalX, goalY, goalZ, gScore, openSet, cfg)
		tryDiagonalMoves(w, col, ents, current, goalX, goalY, goalZ, gScore, openSet, cfg)
		tryParkourMoves(ctx, w, col, current, goalX, goalY, goalZ, gScore, openSet, cfg)
	}
	if partial {
		// A cheap boundary behind us must not outrank closer supported progress
		// (notably slow crouch corridors) merely because it is a frontier.
		if best != nil && cfg.frontiers[[3]int{best.X, best.Y, best.Z}] < 2 &&
			(frontier == nil || bestScore <= frontierScore) {
			return reconstructPath(best), nil
		}
		if frontier != nil {
			return reconstructPath(frontier), nil
		}
	}

	return nil, fmt.Errorf("pathfinding: no path found")
}

// tryCardinalMoves generates walk, step-up, descend, fall, and door moves in 4 cardinal directions.
func tryCardinalMoves(w *world.Module, col *collisions.Module, ents *entities.Module,
	current *PathNode, goalX, goalY, goalZ int,
	gScore map[[3]int]float64, openSet *nodeHeap,
	cfg simulationConfig,
) {
	cx, cy, cz := current.X, current.Y, current.Z
	srcFy := nodeSurfaceY(col, cx, cy, cz, cfg)

	for _, off := range cardinalOffsets {
		nx, nz := cx+off[0], cz+off[1]

		// 1. walk (dy=0)
		tryMove(w, col, ents, current, srcFy, nx, cy, nz, goalX, goalY, goalZ, gScore, openSet, cfg)

		// 2. rise into the next node band, only if the collision sweep can step it
		for rise := 1; rise <= max(1, int(math.Ceil(cfg.maxStep))); rise++ {
			tryMove(w, col, ents, current, srcFy, nx, cy+rise, nz, goalX, goalY, goalZ, gScore, openSet, cfg)
		}

		// 3. descend/fall (dy=-1 to -safeFall)
		foundLanding := false
		for dy := -1; dy >= -safeFallDistance; dy-- {
			if dy <= -2 && foundLanding {
				break // only use shallowest fall
			}

			ny := cy + dy

			if edgeBlocked(cfg, current, nx, ny, nz) {
				continue
			}
			cost, sneaking, _ := moveCost(w, col, ents, nx, ny, nz, cfg)
			if cost < 0 {
				continue
			}

			height := cfg.height
			if sneaking {
				height = cfg.sneakingHeight
			}

			// Falling requires a clear, loaded departure and descent column.
			if !canPassBetween(w, col, cx, cz, nx, nz, srcFy, srcFy, height, cfg) {
				break
			}
			clear := true
			for checkY := cy - 1; checkY > ny; checkY-- {
				if !nodeLoaded(w, nx, checkY, nz, cfg) || !col.CanFitAt(float64(nx)+0.5, float64(checkY), float64(nz)+0.5, cfg.width, height) {
					clear = false
					break
				}
			}
			if !clear {
				break
			}

			// calculate edge cost
			var edgeCost float64
			switch {
			case dy == -1:
				edgeCost = cost + descendCost()
			default: // dy <= -2
				edgeCost = cost + walkOffBlockCost + fallCost(-dy)
			}

			tentativeG := current.G + edgeCost
			nKey := [3]int{nx, ny, nz}
			if best, ok := gScore[nKey]; ok && tentativeG >= best {
				continue
			}
			gScore[nKey] = tentativeG

			h := heuristic(nx, ny, nz, goalX, goalY, goalZ)
			node := &PathNode{
				X: nx, Y: ny, Z: nz,
				G: tentativeG, H: h, F: tentativeG + h,
				Sneaking: sneaking,
				Parent:   current,
			}
			heap.Push(openSet, node)

			foundLanding = true
		}

		// 4. door traversal: check if a closed wooden door blocks walk at (nx, cy, nz)
		if _, _, _, hasDoor := findClosedWoodenDoor(w, nx, cy, nz); hasDoor {
			tryDoorMove(w, col, ents, current, nx, cy, nz, goalX, goalY, goalZ, gScore, openSet, cfg)
		}
	}
}

// tryMove attempts to add a walk or step-up node.
func tryMove(w *world.Module, col *collisions.Module, ents *entities.Module,
	current *PathNode, srcFy float64, nx, ny, nz int,
	goalX, goalY, goalZ int,
	gScore map[[3]int]float64, openSet *nodeHeap,
	cfg simulationConfig,
) {
	if edgeBlocked(cfg, current, nx, ny, nz) {
		return
	}
	cx, cz := current.X, current.Z

	cost, sneaking, destFy := moveCost(w, col, ents, nx, ny, nz, cfg)
	if cost < 0 {
		return
	}

	height := cfg.height
	if sneaking {
		height = cfg.sneakingHeight
	}

	if math.Abs(srcFy-destFy) < 0.08 {
		// Level endpoints are not proof of a level edge: a diagonal bed/slab
		// corner can step the sweep up and leave it floating over the goal.
		if !walkCorridor(w, col, float64(cx)+0.5, srcFy, float64(cz)+0.5, float64(nx)+0.5, destFy, float64(nz)+0.5, sneaking, cfg) {
			return
		}
	} else if !canPassBetween(w, col, cx, cz, nx, nz, srcFy, destFy, height, cfg) {
		return
	}

	edgeCost := cost

	tentativeG := current.G + edgeCost
	nKey := [3]int{nx, ny, nz}
	if best, ok := gScore[nKey]; ok && tentativeG >= best {
		return
	}
	gScore[nKey] = tentativeG

	h := heuristic(nx, ny, nz, goalX, goalY, goalZ)
	node := &PathNode{
		X: nx, Y: ny, Z: nz,
		G: tentativeG, H: h, F: tentativeG + h,
		Sneaking: sneaking,
		Parent:   current,
	}
	heap.Push(openSet, node)
}

// tryDoorMove adds a node that goes through a closed wooden door.
func tryDoorMove(w *world.Module, col *collisions.Module, _ *entities.Module,
	current *PathNode, nx, ny, nz int,
	goalX, goalY, goalZ int,
	gScore map[[3]int]float64, openSet *nodeHeap,
	cfg simulationConfig,
) {
	if edgeBlocked(cfg, current, nx, ny, nz) {
		return
	}
	doorX, doorY, doorZ, found := findClosedWoodenDoor(w, nx, ny, nz)
	if !found || !nodeLoaded(w, nx, ny, nz, cfg) {
		return
	}
	if _, ok := standSurface(col, current.X, current.Y, current.Z, cfg.height, cfg); !ok {
		return
	}
	if _, supported := col.SupportSurfaceY(nx, ny, nz, cfg.width); !supported || moveCostInner(w, nil, nx, ny, nz, false) < 0 {
		return
	}

	// the door blocks passage, but we can open it
	// cost = base walk + door interaction penalty
	cost := sprintOneBlockCost + doorInteractCost

	tentativeG := current.G + cost
	nKey := [3]int{nx, ny, nz}
	if best, ok := gScore[nKey]; ok && tentativeG >= best {
		return
	}
	gScore[nKey] = tentativeG

	h := heuristic(nx, ny, nz, goalX, goalY, goalZ)
	node := &PathNode{
		X: nx, Y: ny, Z: nz,
		G: tentativeG, H: h, F: tentativeG + h,
		InteractDoor: true,
		DoorX:        doorX,
		DoorY:        doorY,
		DoorZ:        doorZ,
		Parent:       current,
	}
	heap.Push(openSet, node)
}

// tryDiagonalMoves generates diagonal movement neighbors.
func tryDiagonalMoves(w *world.Module, col *collisions.Module, ents *entities.Module,
	current *PathNode, goalX, goalY, goalZ int,
	gScore map[[3]int]float64, openSet *nodeHeap,
	cfg simulationConfig,
) {
	cx, cy, cz := current.X, current.Y, current.Z
	srcFy := nodeSurfaceY(col, cx, cy, cz, cfg)

	for _, off := range diagonalOffsets {
		nx, nz := cx+off[0], cz+off[1]

		if !canDiagonalTraverse(w, col, cx, cy, cz, off[0], off[1], cfg) {
			continue
		}

		// try same level and one below
		for _, dy := range [2]int{0, -1} {
			ny := cy + dy
			if edgeBlocked(cfg, current, nx, ny, nz) {
				continue
			}

			cost, sneaking, destFy := moveCost(w, col, ents, nx, ny, nz, cfg)
			if cost < 0 {
				continue
			}

			height := cfg.height
			if sneaking {
				height = cfg.sneakingHeight
			}

			traverseY := destFy
			if dy < 0 {
				traverseY = srcFy
			}
			if dy == 0 && math.Abs(srcFy-destFy) < 0.08 {
				if !walkCorridor(w, col, float64(cx)+0.5, srcFy, float64(cz)+0.5, float64(nx)+0.5, destFy, float64(nz)+0.5, sneaking, cfg) {
					continue
				}
			} else if !canPassBetween(w, col, cx, cz, nx, nz, srcFy, traverseY, height, cfg) {
				continue
			}

			edgeCost := cost * math.Sqrt2
			if dy == -1 {
				edgeCost += descendCost()
			}

			tentativeG := current.G + edgeCost
			nKey := [3]int{nx, ny, nz}
			if best, ok := gScore[nKey]; ok && tentativeG >= best {
				continue
			}
			gScore[nKey] = tentativeG

			h := heuristic(nx, ny, nz, goalX, goalY, goalZ)
			node := &PathNode{
				X: nx, Y: ny, Z: nz,
				G: tentativeG, H: h, F: tentativeG + h,
				Sneaking: sneaking,
				Parent:   current,
			}
			heap.Push(openSet, node)
		}
	}
}

// tryParkourMoves generates sprint-jump moves using physics simulation.
func tryParkourMoves(ctx context.Context, w *world.Module, col *collisions.Module,
	current *PathNode, goalX, goalY, goalZ int,
	gScore map[[3]int]float64, openSet *nodeHeap,
	cfg simulationConfig,
) {
	cx, cy, cz := current.X, current.Y, current.Z

	landings := simulateJumps(ctx, col, w, cx, cy, cz, cfg)
	for _, landing := range landings {
		nx, ny, nz := landing.X, landing.Y, landing.Z
		if edgeBlocked(cfg, current, nx, ny, nz) {
			continue
		}

		// verify destination is standable
		if !canStandAt(w, col, nx, ny, nz, cfg) {
			continue
		}
		// Parkour solves discontinuities, not ordinary ground travel. A jump
		// omits surface/crouch/descent costs and otherwise becomes a spurious
		// shortcut on ice or down a perfectly walkable slope.
		if ordinaryJumpCorridor(ctx, w, col, current, landing, cfg) {
			continue
		}

		// cost based on simulation ticks
		edgeCost := float64(landing.Ticks) + 1.0 // +1 for the jump action penalty

		tentativeG := current.G + edgeCost
		nKey := [3]int{nx, ny, nz}
		if best, ok := gScore[nKey]; ok && tentativeG >= best {
			continue
		}
		gScore[nKey] = tentativeG

		h := heuristic(nx, ny, nz, goalX, goalY, goalZ)
		yaw := yawBetween(cx, cz, nx, nz)
		node := &PathNode{
			X: nx, Y: ny, Z: nz,
			G: tentativeG, H: h, F: tentativeG + h,
			Jump:    true,
			JumpYaw: yaw,
			Parent:  current,
		}
		heap.Push(openSet, node)
	}
}

// ordinaryJumpCorridor reuses the same cardinal edge generator as A*, including
// its support, step-height, descent-column, sneak and failed-edge checks.
func ordinaryJumpCorridor(ctx context.Context, w *world.Module, col *collisions.Module, from *PathNode, landing jumpLanding, cfg simulationConfig) bool {
	dx, dz := landing.X-from.X, landing.Z-from.Z
	if dx != 0 && dz != 0 {
		return false
	}
	count := iabs(dx) + iabs(dz)
	if count == 0 || count > simMaxTicks {
		return false
	}
	ox, oz := sign(dx), sign(dz)
	current := from
	open := make(nodeHeap, 0, 24)
	scores := make(map[[3]int]float64, 24)
	for range count {
		if ctx.Err() != nil {
			return false
		}
		clear(scores)
		open = open[:0]
		tryCardinalMoves(w, col, nil, current, landing.X, landing.Y, landing.Z, scores, &open, cfg)
		var next *PathNode
		for _, candidate := range open {
			if candidate.X == current.X+ox && candidate.Z == current.Z+oz && !candidate.InteractDoor &&
				(next == nil || candidate.G < next.G) {
				next = candidate
			}
		}
		if next == nil {
			return false
		}
		current = next
	}
	return current.Y == landing.Y
}

// heuristic uses Euclidean distance scaled by best-case speed (sprint cost).
func heuristic(x1, y1, z1, x2, y2, z2 int) float64 {
	dx := float64(x1 - x2)
	dy := float64(y1 - y2)
	dz := float64(z1 - z2)
	return math.Sqrt(dx*dx+dy*dy+dz*dz) * sprintOneBlockCost
}

func reconstructPath(node *PathNode) []PathNode {
	var path []PathNode
	for n := node; n != nil; n = n.Parent {
		path = append(path, *n)
	}
	// reverse
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

func edgeBlocked(cfg simulationConfig, from *PathNode, x, y, z int) bool {
	_, blocked := cfg.blockedEdges[[6]int{from.X, from.Y, from.Z, x, y, z}]
	return blocked
}

// nodeHeap implements heap.Interface for PathNode priority queue.
type nodeHeap []*PathNode

func (h nodeHeap) Len() int           { return len(h) }
func (h nodeHeap) Less(i, j int) bool { return h[i].F < h[j].F }
func (h nodeHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }

func (h *nodeHeap) Push(x any) {
	n := x.(*PathNode)
	n.index = len(*h)
	*h = append(*h, n)
}

func (h *nodeHeap) Pop() any {
	old := *h
	n := len(old)
	node := old[n-1]
	old[n-1] = nil
	node.index = -1
	*h = old[:n-1]
	return node
}
