package physics

import (
	"math"
)

const insideBlockInset = float64(float32(1e-5))

// Entity.checkInsideBlocks follows the collision axis order, not the diagonal
// joining its endpoints. A diagonal swept enclosing box would hit untouched webs.
func visitMovementBlocks(from, to [3]float64, width, height, originalX, originalZ float64, scratch map[[3]int]struct{}, visit func([3]int, bool, float64)) {
	axes := [3]int{1, 0, 2}
	if math.Abs(originalX) < math.Abs(originalZ) {
		axes = [3]int{1, 2, 0}
	}
	remaining := 16
	moved := false
	stationary := func(pos [3]int, precise bool) { visit(pos, precise, to[1]) }
	for _, axis := range axes {
		if from[axis] == to[axis] {
			continue
		}
		moved = true
		next := from
		next[axis] = to[axis]
		minY := min(from[1], next[1])
		segment := func(pos [3]int, precise bool) { visit(pos, precise, minY) }
		remaining -= visitAxisBlocks(from, next, width, height, axis, remaining, scratch, segment)
		from = next
		if remaining <= 0 {
			visitStationaryBlocks(to, width, height, stationary)
		}
	}
	if !moved {
		visitStationaryBlocks(to, width, height, stationary)
	}
}

func insideBounds(pos [3]float64, width, height float64) (low, high [3]float64) {
	half := width / 2
	return [3]float64{pos[0] - half + insideBlockInset, pos[1] + insideBlockInset, pos[2] - half + insideBlockInset},
		[3]float64{pos[0] + half - insideBlockInset, pos[1] + height - insideBlockInset, pos[2] + half - insideBlockInset}
}

func visitStationaryBlocks(pos [3]float64, width, height float64, visit func([3]int, bool)) {
	low, high := insideBounds(pos, width, height)
	// BlockPos.betweenClosed: X fastest, then Y, then Z.
	for z := int(math.Floor(low[2])); z <= int(math.Floor(high[2])); z++ {
		for y := int(math.Floor(low[1])); y <= int(math.Floor(high[1])); y++ {
			for x := int(math.Floor(low[0])); x <= int(math.Floor(high[0])); x++ {
				visit([3]int{x, y, z}, true)
			}
		}
	}
}

// Specialized BlockGetter.forEachBlockIntersectedBetween for a single collision
// axis. Keeping its corner crossings, iteration limit and directional ordering
// matters when several different stuck blocks or bubble cells are crossed.
func visitAxisBlocks(from, to [3]float64, width, height float64, axis, limit int, visited map[[3]int]struct{}, visit func([3]int, bool)) int {
	delta := to[axis] - from[axis]
	if delta*delta < insideBlockInset*insideBlockInset {
		if limit > 0 {
			visitStationaryBlocks(to, width, height, visit)
		}
		return 1
	}
	low, high := insideBounds(to, width, height)
	startLow, startHigh := low, high
	startLow[axis] -= delta
	startHigh[axis] -= delta
	// Reuse scratch across axes/ticks; BlockGetter deduplicates air cells too.
	clear(visited)
	lastIteration := 0
	emit := func(pos [3]int, iteration int) bool {
		if _, seen := visited[pos]; seen {
			return true
		}
		if iteration >= limit {
			return false
		}
		visited[pos] = struct{}{}
		lastIteration = iteration
		precise := delta*delta > (1-insideBlockInset)*(1-insideBlockInset)
		if !precise {
			precise = high[0] > float64(pos[0]) && low[0] < float64(pos[0]+1) &&
				high[1] > float64(pos[1]) && low[1] < float64(pos[1]+1) &&
				high[2] > float64(pos[2]) && low[2] < float64(pos[2]+1)
		}
		visit(pos, precise)
		return true
	}
	if !visitDirectionalCells(startLow, startHigh, axis, delta, 0, emit) {
		return lastIteration + 1
	}
	// BlockGetter.getFurthestCorner for axis-aligned travel: Y uses the
	// negative Y corner; X/Z use their positive corners, independent of sign.
	corner := startHigh
	if axis == 1 {
		corner = startLow
	}
	coord := corner[axis]
	cell := int(math.Floor(coord))
	sign := 1
	boundary := float64(cell + 1)
	if delta < 0 {
		sign, boundary = -1, float64(cell)
	}
	iteration := 0
	for t := (boundary - coord) / delta; t <= 1; t = (boundary - coord) / delta {
		cell += sign
		boundary += float64(sign)
		// AABB.clip ignores an intersection exactly at either endpoint.
		if t <= 0 || t >= 1 {
			continue
		}
		iteration++
		hit := max(float64(cell)+insideBlockInset, min(float64(cell+1)-insideBlockInset, coord+t*delta))
		crossLow, crossHigh := startLow, startHigh
		if axis == 1 {
			crossLow[axis], crossHigh[axis] = hit, hit+(high[axis]-low[axis])
		} else {
			crossLow[axis], crossHigh[axis] = hit-(high[axis]-low[axis]), hit
		}
		if !visitDirectionalCells(crossLow, crossHigh, axis, delta, iteration, emit) {
			return lastIteration + 1
		}
	}
	visitDirectionalCells(low, high, axis, delta, iteration+1, emit)
	return lastIteration + 1
}

func visitDirectionalCells(low, high [3]float64, movingAxis int, delta float64, iteration int, visit func([3]int, int) bool) bool {
	axes := [3]int{1, 0, 2}
	if movingAxis == 2 {
		axes = [3]int{1, 2, 0}
	}
	var start, count, step [3]int
	for axis := range 3 {
		start[axis] = int(math.Floor(low[axis]))
		count[axis] = int(math.Floor(high[axis])) - start[axis]
		step[axis] = 1
		if axis == movingAxis && delta < 0 {
			start[axis] += count[axis]
			step[axis] = -1
		}
	}
	for first := 0; first <= count[axes[0]]; first++ {
		for second := 0; second <= count[axes[1]]; second++ {
			for third := 0; third <= count[axes[2]]; third++ {
				pos := start
				pos[axes[0]] += first * step[axes[0]]
				pos[axes[1]] += second * step[axes[1]]
				pos[axes[2]] += third * step[axes[2]]
				if !visit(pos, iteration) {
					return false
				}
			}
		}
	}
	return true
}
