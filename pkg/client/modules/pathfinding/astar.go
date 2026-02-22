package pathfinding

import (
	"container/heap"
	"fmt"
	"math"

	"github.com/go-mclib/client/pkg/client/modules/collisions"
	"github.com/go-mclib/client/pkg/client/modules/entities"
	"github.com/go-mclib/client/pkg/client/modules/world"
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

func findPath(w *world.Module, col *collisions.Module, ents *entities.Module,
	startX, startY, startZ, goalX, goalY, goalZ, maxNodes int,
	jumpPower, effectiveSpeed float64,
) ([]PathNode, error) {
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
		tryCardinalMoves(w, col, ents, current, goalX, goalY, goalZ, gScore, openSet)
		tryDiagonalMoves(w, col, ents, current, goalX, goalY, goalZ, gScore, openSet)
		tryParkourMoves(w, col, current, goalX, goalY, goalZ, gScore, openSet, jumpPower, effectiveSpeed)
	}

	return nil, fmt.Errorf("pathfinding: no path found")
}

// tryCardinalMoves generates walk, step-up, descend, fall, and door moves in 4 cardinal directions.
func tryCardinalMoves(w *world.Module, col *collisions.Module, ents *entities.Module,
	current *PathNode, goalX, goalY, goalZ int,
	gScore map[[3]int]float64, openSet *nodeHeap,
) {
	cx, cy, cz := current.X, current.Y, current.Z

	for _, off := range cardinalOffsets {
		nx, nz := cx+off[0], cz+off[1]

		// 1. walk (dy=0)
		tryMove(w, col, ents, current, nx, cy, nz, 0, goalX, goalY, goalZ, gScore, openSet)

		// 2. step-up (dy=+1)
		if canStepUp(w, col, nx, cy, nz) {
			tryMove(w, col, ents, current, nx, cy+1, nz, 1, goalX, goalY, goalZ, gScore, openSet)
		}

		// 3. descend/fall (dy=-1 to -safeFall)
		foundLanding := false
		for dy := -1; dy >= -safeFallDistance; dy-- {
			if dy <= -2 && foundLanding {
				break // only use shallowest fall
			}

			ny := cy + dy
			isGoal := nx == goalX && ny == goalY && nz == goalZ

			cost, sneaking := moveCost(w, col, ents, nx, ny, nz)
			if cost < 0 && !isGoal {
				if dy == -1 {
					continue // -1 might just be impassable, try deeper
				}
				break // deeper falls need contiguous passable space
			}
			if cost < 0 {
				cost = SprintOneBlockCost
			}

			height := playerHeight
			if sneaking {
				height = playerSneakingHeight
			}

			if !isGoal {
				// check passability at source height
				if !canPassBetween(col, cx, cz, nx, cy, nz, height) {
					break
				}
				if dy <= -2 {
					// verify intermediate Y levels are clear
					clear := true
					for checkY := cy - 1; checkY > ny; checkY-- {
						if !col.CanFitAt(float64(nx)+0.5, float64(checkY), float64(nz)+0.5, playerWidth, height) {
							clear = false
							break
						}
					}
					if !clear {
						break
					}
				}
			}

			// calculate edge cost
			var edgeCost float64
			switch {
			case dy == -1:
				edgeCost = cost + descendCost()
			default: // dy <= -2
				edgeCost = cost + WalkOffBlockCost + fallCost(-dy)
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
			tryDoorMove(w, col, ents, current, nx, cy, nz, goalX, goalY, goalZ, gScore, openSet)
		}
	}
}

// tryMove attempts to add a walk or step-up node.
func tryMove(w *world.Module, col *collisions.Module, ents *entities.Module,
	current *PathNode, nx, ny, nz, dy int,
	goalX, goalY, goalZ int,
	gScore map[[3]int]float64, openSet *nodeHeap,
) {
	cx, cz := current.X, current.Z
	isGoal := nx == goalX && ny == goalY && nz == goalZ

	cost, sneaking := moveCost(w, col, ents, nx, ny, nz)
	if cost < 0 && !isGoal {
		return
	}
	if cost < 0 {
		cost = SprintOneBlockCost
	}

	height := playerHeight
	if sneaking {
		height = playerSneakingHeight
	}

	if !isGoal {
		if !canPassBetween(col, cx, cz, nx, ny, nz, height) {
			return
		}
	}

	edgeCost := cost
	if dy == 1 {
		edgeCost += JumpOneBlockCost
	}

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
func tryDoorMove(w *world.Module, _ *collisions.Module, _ *entities.Module,
	current *PathNode, nx, ny, nz int,
	goalX, goalY, goalZ int,
	gScore map[[3]int]float64, openSet *nodeHeap,
) {
	doorX, doorY, doorZ, found := findClosedWoodenDoor(w, nx, ny, nz)
	if !found {
		return
	}

	// the door blocks passage, but we can open it
	// cost = base walk + door interaction penalty
	cost := SprintOneBlockCost + DoorInteractCost

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
) {
	cx, cy, cz := current.X, current.Y, current.Z

	for _, off := range diagonalOffsets {
		nx, nz := cx+off[0], cz+off[1]

		if !canDiagonalTraverse(w, col, cx, cy, cz, off[0], off[1]) {
			continue
		}

		// try same level and one below
		for _, dy := range [2]int{0, -1} {
			ny := cy + dy

			isGoal := nx == goalX && ny == goalY && nz == goalZ

			cost, sneaking := moveCost(w, col, ents, nx, ny, nz)
			if cost < 0 && !isGoal {
				continue
			}
			if cost < 0 {
				cost = SprintOneBlockCost
			}

			height := playerHeight
			if sneaking {
				height = playerSneakingHeight
			}

			if !isGoal && dy == 0 {
				if !canPassBetween(col, cx, cz, nx, ny, nz, height) {
					continue
				}
			} else if !isGoal && dy == -1 {
				// check passability at source height
				if !canPassBetween(col, cx, cz, nx, cy, nz, height) {
					continue
				}
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
func tryParkourMoves(w *world.Module, col *collisions.Module,
	current *PathNode, goalX, goalY, goalZ int,
	gScore map[[3]int]float64, openSet *nodeHeap,
	jumpPower, effectiveSpeed float64,
) {
	cx, cy, cz := current.X, current.Y, current.Z

	// like Baritone: don't parkour if we could just traverse
	// check if any cardinal neighbor at same level is walkable
	for _, off := range cardinalOffsets {
		nx, nz := cx+off[0], cz+off[1]
		if canStandAt(w, col, nx, cy, nz) {
			// passable adjacent block exists in this direction — no need to jump this way
			// but other directions might still need jumps, so continue checking
		}
	}

	// simulate jumps from current position
	landings := SimulateJumps(col, w, cx, cy, cz, jumpPower, effectiveSpeed)
	for _, landing := range landings {
		nx, ny, nz := landing.X, landing.Y, landing.Z

		// skip if same block or adjacent (should use walk instead)
		dist := iabs(nx-cx) + iabs(nz-cz)
		if dist < 2 {
			continue
		}

		// skip if there's a walkable path in this direction (don't parkour when you can walk)
		dirX := sign(nx - cx)
		dirZ := sign(nz - cz)
		if dirX != 0 && dirZ == 0 {
			adjX := cx + dirX
			if canStandAt(w, col, adjX, cy, cz) {
				continue
			}
		} else if dirZ != 0 && dirX == 0 {
			adjZ := cz + dirZ
			if canStandAt(w, col, cx, cy, adjZ) {
				continue
			}
		}

		isGoal := nx == goalX && ny == goalY && nz == goalZ

		// verify destination is standable
		if !isGoal && !canStandAt(w, col, nx, ny, nz) {
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
	return math.Sqrt(dx*dx+dy*dy+dz*dz) * SprintOneBlockCost
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
