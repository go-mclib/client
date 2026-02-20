package collisions

import "math"

const (
	PlayerWidth  = 0.6
	PlayerHeight = 1.8
	StepUpHeight = 0.6
	Epsilon      = 1.0e-7
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

// PlayerAABB creates a player-sized AABB at the given position.
func PlayerAABB(x, y, z float64) AABB {
	return EntityAABB(x, y, z, PlayerWidth, PlayerHeight)
}

// Intersects returns true if the two AABBs overlap.
func (a AABB) Intersects(b AABB) bool {
	return a.MinX < b.MaxX && a.MaxX > b.MinX &&
		a.MinY < b.MaxY && a.MaxY > b.MinY &&
		a.MinZ < b.MaxZ && a.MaxZ > b.MinZ
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

// Center returns the center point of the AABB.
func (a AABB) Center() (x, y, z float64) {
	return (a.MinX + a.MaxX) / 2, (a.MinY + a.MaxY) / 2, (a.MinZ + a.MaxZ) / 2
}

// Size returns the dimensions of the AABB.
func (a AABB) Size() (x, y, z float64) {
	return a.MaxX - a.MinX, a.MaxY - a.MinY, a.MaxZ - a.MinZ
}

// clipXCollide clips movement on the X axis against another AABB.
// Matches VoxelShape.collideX: epsilon tolerance on perpendicular overlap and clip distance.
func (a AABB) clipXCollide(other AABB, dx float64) float64 {
	if math.Abs(dx) < Epsilon {
		return 0
	}
	// perpendicular overlap with epsilon shrink (vanilla: entity bounds ± 1e-7)
	if other.MaxY <= a.MinY+Epsilon || other.MinY >= a.MaxY-Epsilon ||
		other.MaxZ <= a.MinZ+Epsilon || other.MinZ >= a.MaxZ-Epsilon {
		return dx
	}
	if dx > 0 {
		d := other.MinX - a.MaxX
		if d >= -Epsilon && d < dx {
			dx = d
		}
	} else {
		d := other.MaxX - a.MinX
		if d <= Epsilon && d > dx {
			dx = d
		}
	}
	return dx
}

// clipYCollide clips movement on the Y axis against another AABB.
func (a AABB) clipYCollide(other AABB, dy float64) float64 {
	if math.Abs(dy) < Epsilon {
		return 0
	}
	if other.MaxX <= a.MinX+Epsilon || other.MinX >= a.MaxX-Epsilon ||
		other.MaxZ <= a.MinZ+Epsilon || other.MinZ >= a.MaxZ-Epsilon {
		return dy
	}
	if dy > 0 {
		d := other.MinY - a.MaxY
		if d >= -Epsilon && d < dy {
			dy = d
		}
	} else {
		d := other.MaxY - a.MinY
		if d <= Epsilon && d > dy {
			dy = d
		}
	}
	return dy
}

// clipZCollide clips movement on the Z axis against another AABB.
func (a AABB) clipZCollide(other AABB, dz float64) float64 {
	if math.Abs(dz) < Epsilon {
		return 0
	}
	if other.MaxX <= a.MinX+Epsilon || other.MinX >= a.MaxX-Epsilon ||
		other.MaxY <= a.MinY+Epsilon || other.MinY >= a.MaxY-Epsilon {
		return dz
	}
	if dz > 0 {
		d := other.MinZ - a.MaxZ
		if d >= -Epsilon && d < dz {
			dz = d
		}
	} else {
		d := other.MaxZ - a.MinZ
		if d <= Epsilon && d > dz {
			dz = d
		}
	}
	return dz
}

// ClosestPoint returns the closest point on the AABB to the given point.
func (a AABB) ClosestPoint(x, y, z float64) (cx, cy, cz float64) {
	cx = math.Max(a.MinX, math.Min(x, a.MaxX))
	cy = math.Max(a.MinY, math.Min(y, a.MaxY))
	cz = math.Max(a.MinZ, math.Min(z, a.MaxZ))
	return
}
