package collisions

import "math"

// authoritative player dimensions, referenced by the physics and pathfinding modules
const (
	PlayerWidth          = float64(float32(0.6))
	PlayerHeight         = float64(float32(1.8))
	PlayerSneakingHeight = 1.5
	StepUpHeight         = float64(float32(0.6))
	Epsilon              = 1.0e-7
)

// AABB is an axis-aligned bounding box.
type AABB struct {
	MinX, MinY, MinZ float64
	MaxX, MaxY, MaxZ float64
}

func NewAABB(minX, minY, minZ, maxX, maxY, maxZ float64) AABB {
	return AABB{minX, minY, minZ, maxX, maxY, maxZ}
}

// EntityAABB creates an AABB centered on (x, z) with y as feet.
func EntityAABB(x, y, z, width, height float64) AABB {
	hw := width / 2
	return AABB{
		MinX: x - hw, MinY: y, MinZ: z - hw,
		MaxX: x + hw, MaxY: y + height, MaxZ: z + hw,
	}
}

// Intersects returns true if the two AABBs overlap.
func (a AABB) Intersects(b AABB) bool {
	return a.MinX < b.MaxX && a.MaxX > b.MinX &&
		a.MinY < b.MaxY && a.MaxY > b.MinY &&
		a.MinZ < b.MaxZ && a.MaxZ > b.MinZ
}

// intersectsVoxel uses the coordinate-merger boundary of Shapes.joinIsNotEmpty.
// Unlike the full-block fast path, voxel intervals at most 1e-7 wide collapse.
func (a AABB) intersectsVoxel(b AABB) bool {
	return a.MinX < b.MaxX-Epsilon && a.MaxX > b.MinX+Epsilon &&
		a.MinY < b.MaxY-Epsilon && a.MaxY > b.MinY+Epsilon &&
		a.MinZ < b.MaxZ-Epsilon && a.MaxZ > b.MinZ+Epsilon
}

// Move returns a new AABB translated by (dx, dy, dz).
func (a AABB) Move(dx, dy, dz float64) AABB {
	return AABB{
		MinX: a.MinX + dx, MinY: a.MinY + dy, MinZ: a.MinZ + dz,
		MaxX: a.MaxX + dx, MaxY: a.MaxY + dy, MaxZ: a.MaxZ + dz,
	}
}

// ExpandTowards expands the AABB in the direction of movement.
// Positive values expand the max, negative expand the min.
func (a AABB) ExpandTowards(dx, dy, dz float64) AABB {
	minX, maxX := a.MinX, a.MaxX
	minY, maxY := a.MinY, a.MaxY
	minZ, maxZ := a.MinZ, a.MaxZ
	if dx < 0 {
		minX += dx
	} else {
		maxX += dx
	}
	if dy < 0 {
		minY += dy
	} else {
		maxY += dy
	}
	if dz < 0 {
		minZ += dz
	} else {
		maxZ += dz
	}
	return AABB{minX, minY, minZ, maxX, maxY, maxZ}
}

// Inflate grows the AABB by the given amount in all directions.
func (a AABB) Inflate(x, y, z float64) AABB {
	return AABB{
		MinX: a.MinX - x, MinY: a.MinY - y, MinZ: a.MinZ - z,
		MaxX: a.MaxX + x, MaxY: a.MaxY + y, MaxZ: a.MaxZ + z,
	}
}

// clipCollide clips movement d along axis (0 X, 1 Y, 2 Z) against another AABB.
// Matches VoxelShape.collideX/Y/Z: epsilon tolerance on perpendicular overlap
// (vanilla: entity bounds ± 1e-7) and on the clip distance.
func (a AABB) clipCollide(other AABB, axis int, d float64) float64 {
	if math.Abs(d) < Epsilon {
		return 0
	}
	for _, perpendicular := range [2]int{(axis + 1) % 3, (axis + 2) % 3} {
		lo, hi := bounds(a, perpendicular)
		otherLo, otherHi := bounds(other, perpendicular)
		if otherHi <= lo+Epsilon || otherLo > hi-Epsilon {
			return d
		}
	}
	lo, hi := bounds(a, axis)
	otherLo, otherHi := bounds(other, axis)
	if d > 0 {
		if gap := otherLo - hi; otherLo > hi-Epsilon && gap >= -Epsilon && gap < d {
			d = gap
		}
	} else {
		if gap := otherHi - lo; otherHi <= lo+Epsilon && gap <= Epsilon && gap > d {
			d = gap
		}
	}
	return d
}

// RayIntersect performs a ray-AABB slab test from (ox, oy, oz) along (dx, dy, dz).
// It returns the ray parameter t of the entry point when the hit lies within
// [0, maxT]; with a unit direction t and maxT are distances.
func (a AABB) RayIntersect(ox, oy, oz, dx, dy, dz, maxT float64) (float64, bool) {
	tMin, tMax := 0.0, maxT
	origin, direction := [3]float64{ox, oy, oz}, [3]float64{dx, dy, dz}
	for axis := range 3 {
		lo, hi := bounds(a, axis)
		o, dir := origin[axis], direction[axis]
		if math.Abs(dir) <= Epsilon {
			if o < lo || o > hi {
				return 0, false
			}
			continue
		}
		t1, t2 := (lo-o)/dir, (hi-o)/dir
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		tMin, tMax = max(tMin, t1), min(tMax, t2)
		if tMin > tMax {
			return 0, false
		}
	}
	return tMin, true
}

// ClosestPoint returns the closest point on the AABB to the given point.
func (a AABB) ClosestPoint(x, y, z float64) (cx, cy, cz float64) {
	cx = max(a.MinX, min(x, a.MaxX))
	cy = max(a.MinY, min(y, a.MaxY))
	cz = max(a.MinZ, min(z, a.MaxZ))
	return
}
