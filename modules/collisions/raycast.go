package collisions

import (
	"math"

	"github.com/go-mclib/data/blocks"
)

// RaycastBlocks checks if a ray from (fromX,fromY,fromZ) to (toX,toY,toZ) is
// blocked by any block collision shape. Uses DDA grid traversal.
// Returns whether a hit occurred and the hit point coordinates.
func (m *Module) RaycastBlocks(fromX, fromY, fromZ, toX, toY, toZ float64) (hit bool, hitX, hitY, hitZ float64) {
	w := m.world

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
		stateID, _ := w.GetLoadedBlock(bx, by, bz)
		if stateID != 0 {
			shapes := blocks.CollisionShape(stateID)
			for _, s := range shapes {
				// offset to world coords
				box := AABB(s).Move(float64(bx), float64(by), float64(bz))
				if t, ok := box.RayIntersect(fromX, fromY, fromZ, dirX, dirY, dirZ, dist); ok {
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
