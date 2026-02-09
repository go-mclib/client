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

// neighbors: 4 cardinal + 4 diagonal
var neighborOffsets = [][3]int{
	// cardinal
	{1, 0, 0}, {-1, 0, 0}, {0, 0, 1}, {0, 0, -1},
	// diagonal
	{1, 0, 1}, {1, 0, -1}, {-1, 0, 1}, {-1, 0, -1},
}

// jump offsets: cardinal-only at distances 2-4 for sprint-jumping across gaps
var jumpOffsets = [][3]int{
	{2, 0, 0}, {-2, 0, 0}, {0, 0, 2}, {0, 0, -2},
	{3, 0, 0}, {-3, 0, 0}, {0, 0, 3}, {0, 0, -3},
	{4, 0, 0}, {-4, 0, 0}, {0, 0, 4}, {0, 0, -4},
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

		// try each adjacent neighbor (walk, step up/down, fall)
		for _, off := range neighborOffsets {
			nx, nz := cx+off[0], cz+off[2]
			isDiag := off[0] != 0 && off[2] != 0

			// diagonal: check traversability (relaxed for shallow gaps)
			if isDiag {
				if !canDiagonalTraverse(w, col, cx, cy, cz, off[0], off[2]) {
					continue
				}
			}

			// build vertical offsets: 0, +1, then -1 through -safeFallDistance
			// for diagonals, limit falls to -1 (deep diagonal falls are unreliable)
			maxFall := safeFallDistance
			if isDiag {
				maxFall = 1
			}

			foundFallLanding := false
			for dy := range verticalOffsets(maxFall) {
				if dy <= -2 && foundFallLanding {
					break // only use the shallowest fall landing
				}

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

				height := playerHeight
				if sneaking {
					height = playerSneakingHeight
				}

				if !isGoal {
					switch {
					case dy == 0:
						if !canPassBetween(col, cx, cz, nx, ny, nz, height) {
							continue
						}
					case dy == 1:
						if !canStepUp(w, nx, cy, nz) {
							continue
						}
						if !canPassBetween(col, cx, cz, nx, ny, nz, height) {
							continue
						}
					case dy == -1:
						if !canPassBetween(col, cx, cz, nx, cy, nz, height) {
							continue
						}
					case dy <= -2:
						// deep fall: check edge passability at source height
						if !canPassBetween(col, cx, cz, nx, cy, nz, height) {
							continue
						}
						// verify intermediate Y levels are clear (player must fall unobstructed)
						clear := true
						for checkY := cy - 1; checkY > ny; checkY-- {
							if !col.CanFitAt(float64(nx)+0.5, float64(checkY), float64(nz)+0.5, playerWidth, height) {
								clear = false
								break
							}
						}
						if !clear {
							continue
						}
					}
				}

				edgeCost := cost
				if isDiag {
					edgeCost *= math.Sqrt2
				}
				switch {
				case dy == 1 || dy == -1:
					edgeCost += 0.5
				case dy <= -2:
					edgeCost += float64(-dy) * 1.0 // scale with fall distance
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

				if dy <= -1 {
					foundFallLanding = true
				}
			}
		}

		// jump neighbors: sprint-jump across gaps (cardinal only, distance 2-4)
		for _, joff := range jumpOffsets {
			nx, nz := cx+joff[0], cz+joff[2]
			dist := iabs(joff[0]) + iabs(joff[2])

			// try same level, one below, and one above
			// dy=+1 only for dist<=2: at dist=3 the player barely reaches y=1.02
			// at the destination (peak is ~1.25 blocks) which is too marginal
			dyValues := []int{0, -1}
			if dist <= 2 {
				dyValues = append(dyValues, 1)
			}

			for _, dy := range dyValues {
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
					cost = 1.0
				}
				// can't sprint-jump while sneaking
				if sneaking {
					continue
				}

				if !isGoal {
					if !canJumpTo(w, col, cx, cy, cz, nx, ny, nz) {
						continue
					}
				}

				edgeCost := cost + float64(dist)*2.0
				if dy != 0 {
					edgeCost += 0.5
				}

				g := current.G + edgeCost
				h := heuristic(nx, ny, nz, goalX, goalY, goalZ)

				node := &PathNode{
					X: nx, Y: ny, Z: nz,
					G: g, H: h, F: g + h,
					Jump:   true,
					Parent: current,
				}
				heap.Push(openSet, node)
			}
		}
	}

	return nil, fmt.Errorf("pathfinding: no path found")
}

// verticalOffsets yields dy values: 0, +1, then -1 through -maxFall.
func verticalOffsets(maxFall int) func(func(int) bool) {
	return func(yield func(int) bool) {
		if !yield(0) {
			return
		}
		if !yield(1) {
			return
		}
		for d := 1; d <= maxFall; d++ {
			if !yield(-d) {
				return
			}
		}
	}
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
