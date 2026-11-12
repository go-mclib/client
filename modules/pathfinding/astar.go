package pathfinding

import (
	"container/heap"
	"context"
	"errors"
	"math"
	"slices"
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

// search holds one A* run: the terrain sources, the goal, the open set and the
// best known cost to each position. The try* generators push neighbours via
// relax.
type search struct {
	ctx                 context.Context
	world               *world.Module
	col                 *collisions.Module
	ents                *entities.Module // optional entity-avoidance costs
	goalX, goalY, goalZ int
	cfg                 simulationConfig
	gScore              map[[3]int]float64
	open                nodeHeap
}

var (
	errStartUnloaded = errors.New("pathfinding: start terrain is unloaded")
	errGoalUnloaded  = errors.New("pathfinding: goal terrain is unloaded")
	errStartBlocked  = errors.New("pathfinding: start has no support or headroom")
	errGoalBlocked   = errors.New("pathfinding: goal has no support or headroom")
	errNoPath        = errors.New("pathfinding: no path found")
	errInvalidGoal   = errors.New("pathfinding: invalid goal")
	errRouteUnloaded = errors.New("pathfinding: route terrain unloaded")
)

func newSearch(ctx context.Context, w *world.Module, col *collisions.Module, ents *entities.Module,
	goalX, goalY, goalZ int, cfg simulationConfig,
) *search {
	return &search{
		ctx: ctx, world: w, col: col, ents: ents,
		goalX: goalX, goalY: goalY, goalZ: goalZ,
		cfg:    cfg,
		gScore: make(map[[3]int]float64),
	}
}

// run returns either the goal or, when partial, a safe bounded segment. A
// segment's endpoint is not a navigation success; the controller retains the
// ultimate goal.
func (s *search) run(startX, startY, startZ, maxNodes int, partial bool) ([]Waypoint, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	w, col, cfg := s.world, s.col, s.cfg
	if !nodeLoaded(w, startX, startY, startZ, cfg) {
		return nil, errStartUnloaded
	}
	if !partial {
		if !nodeLoaded(w, s.goalX, s.goalY, s.goalZ, cfg) {
			return nil, errGoalUnloaded
		}
		if _, ok := standSurface(col, s.goalX, s.goalY, s.goalZ, cfg.sneakingHeight, cfg); !ok {
			return nil, errGoalBlocked
		}
	}
	if _, ok := standSurface(col, startX, startY, startZ, cfg.sneakingHeight, cfg); !ok {
		return nil, errStartBlocked
	}
	start := &node{Waypoint: Waypoint{X: startX, Y: startY, Z: startZ}}
	start.h = s.heuristic(startX, startY, startZ)
	start.f = start.h

	s.open = nodeHeap{start}
	heap.Init(&s.open)
	// track best known g-cost to each position for proper A* deduplication
	s.gScore[[3]int{startX, startY, startZ}] = 0

	explored := 0
	var best, frontier *node
	bestScore, frontierScore := math.Inf(1), math.Inf(1)
	deadline := time.Now().Add(200 * time.Millisecond)

	for s.open.Len() > 0 {
		if err := s.ctx.Err(); err != nil {
			return nil, err
		}
		current := heap.Pop(&s.open).(*node)
		cx, cy, cz := current.X, current.Y, current.Z
		if partial && !navigationLoaded(w, float64(cx)+0.5, nodeSurfaceY(col, cx, cy, cz, cfg), float64(cz)+0.5, cfg) {
			continue
		}

		if cx == s.goalX && cy == s.goalY && cz == s.goalZ {
			return reconstructPath(current), nil
		}

		// skip if this node has been superseded by a cheaper path
		key := [3]int{cx, cy, cz}
		if g, ok := s.gScore[key]; ok && current.g > g {
			continue
		}
		explored++
		if partial && current.parent != nil {
			score := current.h + current.g*0.1 + float64(cfg.frontiers[key])*32
			if current.h < start.h-0.5 && score < bestScore {
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
		s.tryCardinalMoves(current)
		s.tryDiagonalMoves(current)
		s.tryParkourMoves(current)
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

	return nil, errNoPath
}

// relax records the edge from -> wp when it improves the best known cost to
// wp, pushing a new open node. It reports whether the edge was accepted.
func (s *search) relax(from *node, wp Waypoint, cost float64) bool {
	g := from.g + cost
	key := [3]int{wp.X, wp.Y, wp.Z}
	if best, ok := s.gScore[key]; ok && g >= best {
		return false
	}
	s.gScore[key] = g
	h := s.heuristic(wp.X, wp.Y, wp.Z)
	heap.Push(&s.open, &node{Waypoint: wp, g: g, h: h, f: g + h, parent: from})
	return true
}

func (s *search) heuristic(x, y, z int) float64 {
	return heuristic(x, y, z, s.goalX, s.goalY, s.goalZ)
}

// tryCardinalMoves generates walk, step-up, descend, fall, and door moves in 4 cardinal directions.
func (s *search) tryCardinalMoves(current *node) {
	w, col, cfg := s.world, s.col, s.cfg
	cx, cy, cz := current.X, current.Y, current.Z
	srcFy := nodeSurfaceY(col, cx, cy, cz, cfg)

	for _, off := range cardinalOffsets {
		nx, nz := cx+off[0], cz+off[1]

		// 1. walk (dy=0)
		s.tryMove(current, srcFy, nx, cy, nz)

		// 2. rise into the next node band, only if the collision sweep can step it
		for rise := 1; rise <= max(1, int(math.Ceil(cfg.maxStep))); rise++ {
			s.tryMove(current, srcFy, nx, cy+rise, nz)
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
			cost, sneaking, _ := moveCost(w, col, s.ents, nx, ny, nz, cfg)
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
			columnClear := true
			for checkY := cy - 1; checkY > ny; checkY-- {
				if !nodeLoaded(w, nx, checkY, nz, cfg) || !col.CanFitAt(float64(nx)+0.5, float64(checkY), float64(nz)+0.5, cfg.width, height) {
					columnClear = false
					break
				}
			}
			if !columnClear {
				break
			}

			var edgeCost float64
			if dy == -1 {
				edgeCost = cost + descendCost()
			} else {
				edgeCost = cost + walkOffBlockCost + fallCost(-dy)
			}
			if s.relax(current, Waypoint{X: nx, Y: ny, Z: nz, Sneaking: sneaking}, edgeCost) {
				foundLanding = true
			}
		}

		// 4. door traversal: check if a closed wooden door blocks walk at (nx, cy, nz)
		if _, _, _, hasDoor := findClosedWoodenDoor(w, nx, cy, nz); hasDoor {
			s.tryDoorMove(current, nx, cy, nz)
		}
	}
}

// tryMove attempts to add a walk or step-up node.
func (s *search) tryMove(current *node, srcFy float64, nx, ny, nz int) {
	w, col, cfg := s.world, s.col, s.cfg
	if edgeBlocked(cfg, current, nx, ny, nz) {
		return
	}
	cx, cz := current.X, current.Z

	cost, sneaking, destFy := moveCost(w, col, s.ents, nx, ny, nz, cfg)
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

	s.relax(current, Waypoint{X: nx, Y: ny, Z: nz, Sneaking: sneaking}, cost)
}

// tryDoorMove adds a node that goes through a closed wooden door.
func (s *search) tryDoorMove(current *node, nx, ny, nz int) {
	w, col, cfg := s.world, s.col, s.cfg
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
	s.relax(current, Waypoint{
		X: nx, Y: ny, Z: nz,
		InteractDoor: true,
		DoorX:        doorX,
		DoorY:        doorY,
		DoorZ:        doorZ,
	}, sprintOneBlockCost+doorInteractCost)
}

// tryDiagonalMoves generates diagonal movement neighbors.
func (s *search) tryDiagonalMoves(current *node) {
	w, col, cfg := s.world, s.col, s.cfg
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

			cost, sneaking, destFy := moveCost(w, col, s.ents, nx, ny, nz, cfg)
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
			s.relax(current, Waypoint{X: nx, Y: ny, Z: nz, Sneaking: sneaking}, edgeCost)
		}
	}
}

// tryParkourMoves generates sprint-jump moves using physics simulation.
func (s *search) tryParkourMoves(current *node) {
	w, col, cfg := s.world, s.col, s.cfg
	cx, cy, cz := current.X, current.Y, current.Z

	for _, landing := range simulateJumps(s.ctx, col, w, cx, cy, cz, cfg) {
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
		if s.ordinaryJumpCorridor(current, landing) {
			continue
		}

		// cost based on simulation ticks, +1 for the jump action penalty
		s.relax(current, Waypoint{
			X: nx, Y: ny, Z: nz,
			Jump:    true,
			JumpYaw: yawBetween(cx, cz, nx, nz),
		}, float64(landing.Ticks)+1.0)
	}
}

// ordinaryJumpCorridor reuses the same cardinal edge generator as A*, including
// its support, step-height, descent-column, sneak and failed-edge checks.
func (s *search) ordinaryJumpCorridor(from *node, landing jumpLanding) bool {
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
	walk := newSearch(s.ctx, s.world, s.col, nil, landing.X, landing.Y, landing.Z, s.cfg)
	walk.open = make(nodeHeap, 0, 24)
	for range count {
		if s.ctx.Err() != nil {
			return false
		}
		clear(walk.gScore)
		walk.open = walk.open[:0]
		walk.tryCardinalMoves(current)
		var next *node
		for _, candidate := range walk.open {
			if candidate.X == current.X+ox && candidate.Z == current.Z+oz && !candidate.InteractDoor &&
				(next == nil || candidate.g < next.g) {
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

func reconstructPath(end *node) []Waypoint {
	var path []Waypoint
	for n := end; n != nil; n = n.parent {
		path = append(path, n.Waypoint)
	}
	slices.Reverse(path)
	return path
}

func edgeBlocked(cfg simulationConfig, from *node, x, y, z int) bool {
	_, blocked := cfg.blockedEdges[[6]int{from.X, from.Y, from.Z, x, y, z}]
	return blocked
}

// nodeHeap implements heap.Interface for the A* open set.
type nodeHeap []*node

func (h nodeHeap) Len() int           { return len(h) }
func (h nodeHeap) Less(i, j int) bool { return h[i].f < h[j].f }
func (h nodeHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }

func (h *nodeHeap) Push(x any) {
	n := x.(*node)
	n.index = len(*h)
	*h = append(*h, n)
}

func (h *nodeHeap) Pop() any {
	old := *h
	n := len(old)
	last := old[n-1]
	old[n-1] = nil
	last.index = -1
	*h = old[:n-1]
	return last
}
