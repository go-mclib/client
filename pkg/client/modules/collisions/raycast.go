package collisions

import (
	"math"

	"github.com/go-mclib/client/pkg/client/modules/world"
	block_shapes "github.com/go-mclib/data/pkg/data/hitboxes/blocks"
)

// RaycastBlocks checks if a ray from (fromX,fromY,fromZ) to (toX,toY,toZ) is
// blocked by any block collision shape. Uses DDA grid traversal.
// Returns whether a hit occurred and the hit point coordinates.
func (m *Module) RaycastBlocks(fromX, fromY, fromZ, toX, toY, toZ float64) (hit bool, hitX, hitY, hitZ float64) {
	w := world.From(m.client)
	if w == nil {
		return false, toX, toY, toZ
	}

	dx := toX - fromX
	dy := toY - fromY
	dz := toZ - fromZ
	dist := math.Sqrt(dx*dx + dy*dy + dz*dz)
	if dist < Epsilon {
		return false, toX, toY, toZ
	}

	// normalize direction
	dirX := dx / dist
	dirY := dy / dist
	dirZ := dz / dist

	// current block position
	bx := int(math.Floor(fromX))
	by := int(math.Floor(fromY))
	bz := int(math.Floor(fromZ))

	// target block
	endBX := int(math.Floor(toX))
	endBY := int(math.Floor(toY))
	endBZ := int(math.Floor(toZ))

	// step direction (+1 or -1)
	stepX, stepY, stepZ := 1, 1, 1
	if dirX < 0 {
		stepX = -1
	}
	if dirY < 0 {
		stepY = -1
	}
	if dirZ < 0 {
		stepZ = -1
	}

	// tMax: distance (in t) to next block boundary on each axis
	// tDelta: distance (in t) to traverse a full block on each axis
	tMaxX, tMaxY, tMaxZ := math.Inf(1), math.Inf(1), math.Inf(1)
	tDeltaX, tDeltaY, tDeltaZ := math.Inf(1), math.Inf(1), math.Inf(1)

	if math.Abs(dirX) > Epsilon {
		var boundary float64
		if stepX > 0 {
			boundary = float64(bx + 1)
		} else {
			boundary = float64(bx)
		}
		tMaxX = (boundary - fromX) / dirX
		tDeltaX = float64(stepX) / dirX
	}
	if math.Abs(dirY) > Epsilon {
		var boundary float64
		if stepY > 0 {
			boundary = float64(by + 1)
		} else {
			boundary = float64(by)
		}
		tMaxY = (boundary - fromY) / dirY
		tDeltaY = float64(stepY) / dirY
	}
	if math.Abs(dirZ) > Epsilon {
		var boundary float64
		if stepZ > 0 {
			boundary = float64(bz + 1)
		} else {
			boundary = float64(bz)
		}
		tMaxZ = (boundary - fromZ) / dirZ
		tDeltaZ = float64(stepZ) / dirZ
	}

	// traverse blocks along the ray
	maxSteps := int(dist*2) + 3
	for range maxSteps {
		// skip the target block — we only care about obstructions between start and target
		if bx == endBX && by == endBY && bz == endBZ {
			break
		}

		// check current block for collision shapes
		stateID := w.GetBlock(bx, by, bz)
		if stateID != 0 {
			shapes := block_shapes.CollisionShape(stateID)
			for _, s := range shapes {
				// offset to world coords
				box := AABB{
					MinX: s.MinX + float64(bx), MinY: s.MinY + float64(by), MinZ: s.MinZ + float64(bz),
					MaxX: s.MaxX + float64(bx), MaxY: s.MaxY + float64(by), MaxZ: s.MaxZ + float64(bz),
				}
				if t, ok := rayAABBIntersect(fromX, fromY, fromZ, dirX, dirY, dirZ, dist, box); ok {
					return true, fromX + dirX*t, fromY + dirY*t, fromZ + dirZ*t
				}
			}
		}

		// advance to next block boundary
		if tMaxX < tMaxY {
			if tMaxX < tMaxZ {
				bx += stepX
				tMaxX += tDeltaX
			} else {
				bz += stepZ
				tMaxZ += tDeltaZ
			}
		} else {
			if tMaxY < tMaxZ {
				by += stepY
				tMaxY += tDeltaY
			} else {
				bz += stepZ
				tMaxZ += tDeltaZ
			}
		}
	}

	return false, toX, toY, toZ
}

// rayAABBIntersect performs ray-AABB slab intersection.
// Returns the t parameter of the closest intersection if it occurs within [0, maxT].
func rayAABBIntersect(ox, oy, oz, dx, dy, dz, maxT float64, box AABB) (float64, bool) {
	tMin := 0.0
	tMax := maxT

	// X slab
	if math.Abs(dx) > Epsilon {
		invD := 1.0 / dx
		t1 := (box.MinX - ox) * invD
		t2 := (box.MaxX - ox) * invD
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		tMin = math.Max(tMin, t1)
		tMax = math.Min(tMax, t2)
		if tMin > tMax {
			return 0, false
		}
	} else if ox < box.MinX || ox > box.MaxX {
		return 0, false
	}

	// Y slab
	if math.Abs(dy) > Epsilon {
		invD := 1.0 / dy
		t1 := (box.MinY - oy) * invD
		t2 := (box.MaxY - oy) * invD
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		tMin = math.Max(tMin, t1)
		tMax = math.Min(tMax, t2)
		if tMin > tMax {
			return 0, false
		}
	} else if oy < box.MinY || oy > box.MaxY {
		return 0, false
	}

	// Z slab
	if math.Abs(dz) > Epsilon {
		invD := 1.0 / dz
		t1 := (box.MinZ - oz) * invD
		t2 := (box.MaxZ - oz) * invD
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		tMin = math.Max(tMin, t1)
		tMax = math.Min(tMax, t2)
		if tMin > tMax {
			return 0, false
		}
	} else if oz < box.MinZ || oz > box.MaxZ {
		return 0, false
	}

	return tMin, true
}
