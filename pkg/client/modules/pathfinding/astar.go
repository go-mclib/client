package pathfinding

import (
	"container/heap"
	"fmt"
	"math"

	"github.com/go-mclib/client/pkg/client/modules/collisions"
	"github.com/go-mclib/client/pkg/client/modules/entities"
	"github.com/go-mclib/client/pkg/client/modules/world"
)

const (
	DefaultMaxNodes = 10000
)

// neighbors: 4 cardinal + 4 diagonal + up/down variants
var neighborOffsets = [][3]int{
	// cardinal
	{1, 0, 0}, {-1, 0, 0}, {0, 0, 1}, {0, 0, -1},
	// diagonal
	{1, 0, 1}, {1, 0, -1}, {-1, 0, 1}, {-1, 0, -1},
}

func findPath(w *world.Module, col *collisions.Module, ents *entities.Module, startX, startY, startZ, goalX, goalY, goalZ, maxNodes int) ([]PathNode, error) {
	start := &PathNode{X: startX, Y: startY, Z: startZ}
	start.H = heuristic(startX, startY, startZ, goalX, goalY, goalZ)
	start.F = start.H

	openSet := &nodeHeap{start}
	heap.Init(openSet)

	closed := make(map[[3]int]bool)
	explored := 0

	for openSet.Len() > 0 {
		current := heap.Pop(openSet).(*PathNode)
		cx, cy, cz := current.X, current.Y, current.Z

		if cx == goalX && cy == goalY && cz == goalZ {
			return reconstructPath(current), nil
		}

		key := [3]int{cx, cy, cz}
		if closed[key] {
			continue
		}
		closed[key] = true
		explored++

		if explored >= maxNodes {
			return nil, fmt.Errorf("pathfinding: max nodes (%d) reached", maxNodes)
		}

		// try each neighbor
		for _, off := range neighborOffsets {
			nx, nz := cx+off[0], cz+off[2]

			// diagonal: check that both cardinal components are passable (no corner cutting)
			if off[0] != 0 && off[2] != 0 {
				if !canStandAt(w, col, cx+off[0], cy, cz) || !canStandAt(w, col, cx, cy, cz+off[2]) {
					continue
				}
			}

			// try same level, one above (step up), and one below (step down)
			for _, dy := range []int{0, 1, -1} {
				ny := cy + dy
				nKey := [3]int{nx, ny, nz}
				if closed[nKey] {
					continue
				}

				isGoal := nx == goalX && ny == goalY && nz == goalZ

				cost, sneaking := moveCost(w, col, ents, nx, ny, nz)
				if cost < 0 && !isGoal {
					continue
				}
				if cost < 0 {
					cost = 1.0 // goal is always reachable
				}

				// check that the player can physically pass between the blocks
				// at the destination height (catches doors, fence gates, etc.)
				height := playerHeight
				if sneaking {
					height = playerSneakingHeight
				}
				if !isGoal && !canPassBetween(col, cx, cz, nx, ny, nz, height) {
					continue
				}

				// diagonal costs √2, vertical step costs extra
				edgeCost := cost
				if off[0] != 0 && off[2] != 0 {
					edgeCost *= math.Sqrt2
				}
				if dy != 0 {
					edgeCost += 0.5 // slight penalty for vertical movement
				}

				g := current.G + edgeCost
				h := heuristic(nx, ny, nz, goalX, goalY, goalZ)

				node := &PathNode{
					X: nx, Y: ny, Z: nz,
					G: g, H: h, F: g + h,
					Sneaking: sneaking,
					Parent:   current,
				}
				heap.Push(openSet, node)
			}
		}
	}

	return nil, fmt.Errorf("pathfinding: no path found")
}

func heuristic(x1, y1, z1, x2, y2, z2 int) float64 {
	dx := float64(x1 - x2)
	dy := float64(y1 - y2)
	dz := float64(z1 - z2)
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
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
