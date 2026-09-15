package collisions

import (
	"math"

	"github.com/go-mclib/data/block_shapes"
	"github.com/go-mclib/data/blocks"
	"github.com/go-mclib/data/hitboxes"
)

// A voxel shape keeps its original grid, not just its merged AABBs. Internal
// grid planes affect VoxelShape.collideX when the moving entity starts inside it.
// The parts/grid slices reference immutable generated data; collection allocates
// only the collider list, never a world-coordinate copy of each block's boxes.
type voxelShape struct {
	parts                        []hitboxes.AABB
	coords                       [3][]float64
	x, y, z                      float64
	offsetBase, offsetX, offsetZ float64
	box                          AABB // a single entity or contextual cuboid when parts is nil
}

func (s voxelShape) size() int {
	if s.parts == nil {
		return 1
	}
	return len(s.parts)
}

func (s voxelShape) part(i int) AABB {
	if s.parts == nil {
		return s.box
	}
	part := s.parts[i]
	return AABB{
		MinX: s.coordinate(0, part.MinX), MinY: s.coordinate(1, part.MinY), MinZ: s.coordinate(2, part.MinZ),
		MaxX: s.coordinate(0, part.MaxX), MaxY: s.coordinate(1, part.MaxY), MaxZ: s.coordinate(2, part.MaxZ),
	}
}

func (s voxelShape) coordinate(axis int, value float64) float64 {
	switch axis {
	case 0:
		return ((value + s.offsetBase) + s.offsetX) + s.x
	case 1:
		return value + s.y
	default:
		return ((value + s.offsetBase) + s.offsetZ) + s.z
	}
}

func bounds(box AABB, axis int) (float64, float64) {
	switch axis {
	case 0:
		return box.MinX, box.MaxX
	case 1:
		return box.MinY, box.MaxY
	default:
		return box.MinZ, box.MaxZ
	}
}

func (s voxelShape) clip(box AABB, axis int, distance float64) float64 {
	if math.Abs(distance) < Epsilon {
		return 0
	}
	coords := s.coords[axis]
	if len(coords) == 0 {
		switch axis {
		case 0:
			return box.clipXCollide(s.box, distance)
		case 1:
			return box.clipYCollide(s.box, distance)
		default:
			return box.clipZCollide(s.box, distance)
		}
	}
	minA, maxA := bounds(box, axis)
	minB, maxB := bounds(box, (axis+1)%3)
	minC, maxC := bounds(box, (axis+2)%3)
	start, end, step := 0, len(coords)-1, 1
	if distance < 0 {
		start, end, step = len(coords)-1, 0, -1
	}
	for i := start; i != end; i += step {
		plane := s.coordinate(axis, coords[i])
		if distance > 0 && plane <= maxA-Epsilon || distance < 0 && plane > minA+Epsilon {
			continue
		}
		for j := range s.size() {
			part := s.part(j)
			lo, hi := bounds(part, axis)
			if distance > 0 && (plane < lo || plane >= hi) || distance < 0 && (plane <= lo || plane > hi) {
				continue
			}
			loB, hiB := bounds(part, (axis+1)%3)
			loC, hiC := bounds(part, (axis+2)%3)
			if hiB <= minB+Epsilon || loB > maxB-Epsilon || hiC <= minC+Epsilon || loC > maxC-Epsilon {
				continue
			}
			if distance > 0 {
				if gap := plane - maxA; gap >= -Epsilon {
					return math.Min(distance, gap)
				}
			} else {
				if gap := plane - minA; gap <= Epsilon {
					return math.Max(distance, gap)
				}
			}
			return distance
		}
	}
	return distance
}

func (m *Module) collisionVoxel(state int32, x, y, z int, bottom float64) (voxelShape, bool) {
	parts := m.collisionShape(state, y, bottom)
	if len(parts) == 0 {
		return voxelShape{}, false
	}
	shape := voxelShape{parts: parts, x: float64(x), y: float64(y), z: float64(z)}
	_, scaffold := scaffoldingBottom[state]
	if state == powderSnowState || scaffold && len(parts) == 1 {
		return voxelShape{box: localBox(parts[0]).Move(float64(x), float64(y), float64(z))}, true
	}
	for axis := range 3 {
		shape.coords[axis] = block_shapes.CollisionCoordinates(state, axis)
	}
	if block_shapes.HasLargeCollisionShape(state) {
		if limit, offset := positionalCollisionBlocks[blocks.StateBlockID(state)]; offset {
			shape.offsetBase = limit // undo the dump's offset at BlockPos.ZERO
			shape.offsetX, shape.offsetZ = blockPositionOffset(x, z, limit)
		}
	}
	return shape, true
}

var positionalCollisionBlocks = map[int32]float64{
	blocks.BlockID("minecraft:bamboo"):            0.25,
	blocks.BlockID("minecraft:pointed_dripstone"): 0.125,
	blocks.BlockID("minecraft:sulfur_spike"):      0.125,
}

func blockPositionOffset(x, z int, limit float64) (float64, float64) {
	// Mth.getSeed deliberately multiplies X in 32 bits before widening.
	seed := int64(int32(x)*int32(3129871)) ^ int64(z)*116129781
	seed = seed*seed*42317861 + seed*11
	seed >>= 16
	offset := func(bits int64) float64 {
		fraction := float32(bits&15) / float32(15)
		return math.Max(-limit, math.Min(limit, (float64(fraction)-0.5)*0.5))
	}
	return offset(seed), offset(seed >> 8)
}
