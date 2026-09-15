package pathfinding

import (
	"container/heap"
	"context"
	"fmt"
	"math"

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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !nodeLoaded(w, startX, startY, startZ, cfg) || !nodeLoaded(w, goalX, goalY, goalZ, cfg) {
		return nil, fmt.Errorf("pathfinding: start or goal terrain is unloaded")
	}
	if _, ok := standSurface(col, goalX, goalY, goalZ, cfg.sneakingHeight, cfg); !ok {
		return nil, fmt.Errorf("pathfinding: goal has no support or headroom")
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

	for openSet.Len() > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current := heap.Pop(openSet).(*PathNode)
		cx, cy, cz := current.X, current.Y, current.Z

		if cx == goalX && cy == goalY && cz == goalZ {
			return reconstructPath(current), nil
		}

		explored++
		if explored >= maxNodes {
			return nil, fmt.Errorf("pathfinding: max nodes (%d) reached", maxNodes)
		}

		// skip if this node has been superseded by a cheaper path
		key := [3]int{cx, cy, cz}
		if best, ok := gScore[key]; ok && current.G > best {
			continue
		}

		// generate all movement types
		tryCardinalMoves(w, col, ents, current, goalX, goalY, goalZ, gScore, openSet, cfg)
		tryDiagonalMoves(w, col, ents, current, goalX, goalY, goalZ, gScore, openSet, cfg)
		tryParkourMoves(ctx, w, col, current, goalX, goalY, goalZ, gScore, openSet, cfg)
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
	cx, cz := current.X, current.Z

	cost, sneaking, destFy := moveCost(w, col, ents, nx, ny, nz, cfg)
	if cost < 0 {
		return
	}

	height := cfg.height
	if sneaking {
		height = cfg.sneakingHeight
	}

	if !canPassBetween(w, col, cx, cz, nx, nz, srcFy, destFy, height, cfg) {
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
	doorX, doorY, doorZ, found := findClosedWoodenDoor(w, nx, ny, nz)
	if !found || !nodeLoaded(w, nx, ny, nz, cfg) {
		return
	}
	if _, ok := standSurface(col, current.X, current.Y, current.Z, cfg.height, cfg); !ok {
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
			if !canPassBetween(w, col, cx, cz, nx, nz, srcFy, traverseY, height, cfg) {
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

		// verify destination is standable
		if !canStandAt(w, col, nx, ny, nz, cfg) {
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
