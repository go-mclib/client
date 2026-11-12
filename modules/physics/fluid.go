package physics

import (
	"math"

	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/data/blocks"
	"github.com/go-mclib/data/registries"
)

const (
	waterFlowScale          = 0.014
	lavaFlowScale           = 0.0023333333333333335
	fastLavaFlowScale       = 0.007
	fluidWater        uint8 = 1
	fluidLava         uint8 = 2
)

var (
	effectDolphinsGrace    = registries.MobEffect.Get("minecraft:dolphins_grace")
	fluidIceBlockID        = blocks.ID("minecraft:ice")
	fluidFrostedIceBlockID = blocks.ID("minecraft:frosted_ice")
)

// The pose eye height and dimension fast-lava flag are supplied by the tick
// before the baseTick scan. Travel coefficients are captured before move(),
// because landing and inside-block effects can change velocity and onGround.
type fluidState struct {
	waterHeight, lavaHeight float64
	eyeInWater              bool
	eyeHeight               float64
	fastLava                bool
	falling                 bool
	gravity                 float64
	waterDrag               float32
}

type fluidBlockReader interface {
	GetBlock(x, y, z int) int32
}

type fluidTracker struct {
	height     float64
	eyesInside bool
	x, y, z    float64
	count      int
}

func (t *fluidTracker) impulse(velX, velZ, scale float64) (x, y, z float64) {
	if t.count == 0 || t.x*t.x+t.y*t.y+t.z*t.z < float64(float32(1e-5)) {
		return 0, 0, 0
	}
	// Vanilla first averages, then scales; do not fold the two operations.
	inverseCount := 1 / float64(t.count)
	x, y, z = t.x*inverseCount, t.y*inverseCount, t.z*inverseCount
	x, y, z = x*scale, y*scale, z*scale
	if math.Abs(velX) < 0.003 && math.Abs(velZ) < 0.003 && math.Sqrt(x*x+y*y+z*z) < 0.0045000000000000005 {
		x, y, z = normalizeFluidVector(x, y, z)
		x, y, z = x*0.0045000000000000005, y*0.0045000000000000005, z*0.0045000000000000005
	}
	return
}

// scanFluids follows EntityFluidInteraction.update, including iteration order,
// running maximum shallow depth, and stationary cells in the current divisor.
func scanFluids(w fluidBlockReader, x, y, z, width, height, eyeHeight float64) (water, lava fluidTracker) {
	halfWidth := width / 2
	minX, minY, minZ := x-halfWidth+0.001, y+0.001, z-halfWidth+0.001
	maxX, maxY, maxZ := x+halfWidth-0.001, y+height-0.001, z+halfWidth-0.001
	eyeX, eyeZ := int(math.Floor(x)), int(math.Floor(z))
	eyeY := y + eyeHeight
	for bx := int(math.Floor(minX)); bx < int(math.Ceil(maxX)); bx++ {
		for by := int(math.Floor(minY)); by < int(math.Ceil(maxY)); by++ {
			for bz := int(math.Floor(minZ)); bz < int(math.Ceil(maxZ)); bz++ {
				kind, _, _ := blocks.FluidState(w.GetBlock(bx, by, bz))
				if kind == 0 {
					continue
				}
				top := float64(by) + fluidHeightAt(w, bx, by, bz)
				if top < minY {
					continue
				}
				t := &water
				if kind == fluidLava {
					t = &lava
				}
				if bx == eyeX && bz == eyeZ && eyeY >= float64(by) && eyeY <= top {
					t.eyesInside = true
				}
				t.height = max(t.height, top-y)
				fx, fy, fz := getFluidFlow(w, kind, bx, by, bz)
				if t.height < 0.4 {
					fx, fy, fz = fx*t.height, fy*t.height, fz*t.height
				}
				t.x, t.y, t.z = t.x+fx, t.y+fy, t.z+fz
				t.count++
			}
		}
	}
	return
}

func (m *Module) updateFluidState(x, y, z, height float64) {
	m.fluid.waterHeight, m.fluid.lavaHeight, m.fluid.eyeInWater = 0, 0, false
	width := m.bodyWidth()
	halfWidth := width / 2
	// Vanilla declines the entire scan if any surrounding chunk is unavailable.
	minX := int(math.Floor(x-halfWidth+0.001)) - 1
	maxX := int(math.Ceil(x + halfWidth - 0.001))
	minZ := int(math.Floor(z-halfWidth+0.001)) - 1
	maxZ := int(math.Ceil(z + halfWidth - 0.001))
	for cz := minZ >> 4; cz <= maxZ>>4; cz++ {
		for cx := minX >> 4; cx <= maxX>>4; cx++ {
			if !m.world.IsChunkLoaded(int32(cx), int32(cz)) {
				return
			}
		}
	}
	water, lava := scanFluids(m.world, x, y, z, width, height, m.fluid.eyeHeight)
	m.fluid.waterHeight, m.fluid.lavaHeight = water.height, lava.height
	m.fluid.eyeInWater = water.eyesInside
	// Player.isPushedByFluid excludes creative flight, but still tracks immersion.
	if m.self.IsFlying() {
		return
	}
	fx, fy, fz := water.impulse(m.velX, m.velZ, waterFlowScale)
	m.velX, m.velY, m.velZ = m.velX+fx, m.velY+fy, m.velZ+fz
	scale := lavaFlowScale
	if m.fluid.fastLava {
		scale = fastLavaFlowScale
	}
	fx, fy, fz = lava.impulse(m.velX, m.velZ, scale)
	m.velX, m.velY, m.velZ = m.velX+fx, m.velY+fy, m.velZ+fz
}

func fluidHeightAt(w fluidBlockReader, x, y, z int) float64 {
	kind, amount, _ := blocks.FluidState(w.GetBlock(x, y, z))
	if kind == 0 {
		return 0
	}
	above, _, _ := blocks.FluidState(w.GetBlock(x, y+1, z))
	if above == kind {
		return 1
	}
	return float64(float32(amount) / float32(9))
}

// Horizontal Direction.Plane order is NORTH, EAST, SOUTH, WEST. The face
// queried on the neighboring block is that same direction, not its opposite.
var fluidDirections = [...]struct{ x, z, face int }{{0, -1, 2}, {1, 0, 5}, {0, 1, 3}, {-1, 0, 4}}

func getFluidFlow(w fluidBlockReader, kind uint8, bx, by, bz int) (flowX, flowY, flowZ float64) {
	_, amount, falling := blocks.FluidState(w.GetBlock(bx, by, bz))
	ownHeight := float32(amount) / float32(9)
	for _, dir := range fluidDirections {
		nx, nz := bx+dir.x, bz+dir.z
		state := w.GetBlock(nx, by, nz)
		neighborKind, neighborAmount, _ := blocks.FluidState(state)
		if neighborKind != 0 && neighborKind != kind {
			continue
		}
		neighborHeight := float32(neighborAmount) / float32(9)
		var distance float32
		if neighborHeight == 0 {
			if !blocks.BlocksFluidFlow(state) {
				belowKind, belowAmount, _ := blocks.FluidState(w.GetBlock(nx, by-1, nz))
				if belowKind == kind && belowAmount > 0 {
					neighborHeight = float32(belowAmount) / float32(9)
					distance = ownHeight - (neighborHeight - float32(0.8888889))
				}
			}
		} else {
			distance = ownHeight - neighborHeight
		}
		flowX += float64(float32(dir.x) * distance)
		flowZ += float64(float32(dir.z) * distance)
	}
	if falling {
		for _, dir := range fluidDirections {
			if fluidSolidFace(w.GetBlock(bx+dir.x, by, bz+dir.z), kind, dir.face) || fluidSolidFace(w.GetBlock(bx+dir.x, by+1, bz+dir.z), kind, dir.face) {
				flowX, flowY, flowZ = normalizeFluidVector(flowX, 0, flowZ)
				flowY -= 6
				break
			}
		}
	}
	return normalizeFluidVector(flowX, flowY, flowZ)
}

func fluidSolidFace(state int32, kind uint8, face int) bool {
	neighborKind, _, _ := blocks.FluidState(state)
	blockID := blocks.StateBlockID(state)
	return neighborKind != kind && blockID != fluidIceBlockID && blockID != fluidFrostedIceBlockID && blocks.IsFaceSturdy(state, face)
}

func normalizeFluidVector(x, y, z float64) (float64, float64, float64) {
	length := math.Sqrt(x*x + y*y + z*z)
	if length < float64(float32(1e-5)) {
		return 0, 0, 0
	}
	return x / length, y / length, z / length
}

func waterTravelCoefficients(sprinting, onGround, dolphinsGrace bool, efficiency, movementSpeed float32) (drag, acceleration float32) {
	drag, acceleration = 0.8, 0.02
	if sprinting {
		drag = 0.9
	}
	if !onGround {
		efficiency *= 0.5
	}
	if efficiency > 0 {
		drag += (float32(0.54600006) - drag) * efficiency
		acceleration += (movementSpeed - acceleration) * efficiency
	}
	if dolphinsGrace {
		drag = 0.96
	}
	return
}

func (m *Module) applyWaterInputScaled(s *self.Module, yaw, forward, strafe float64) {
	m.fluid.falling = m.velY <= 0
	m.fluid.gravity = m.getEffectiveGravity(s)
	drag, acceleration := waterTravelCoefficients(s.Sprinting(), m.onGround, s.HasEffect(effectDolphinsGrace), float32(s.AttributeValue("minecraft:water_movement_efficiency", 0)), float32(s.MovementSpeed(s.Sprinting())))
	m.fluid.waterDrag = drag
	dx, _, dz := MoveRelative(float64(acceleration), forward, strafe, yaw)
	m.velX, m.velZ = m.velX+dx, m.velZ+dz
}

func (m *Module) applyLavaInputScaled(s *self.Module, yaw, forward, strafe float64) {
	m.fluid.falling = m.velY <= 0
	m.fluid.gravity = m.getEffectiveGravity(s)
	dx, _, dz := MoveRelative(float64(float32(0.02)), forward, strafe, yaw)
	m.velX, m.velZ = m.velX+dx, m.velZ+dz
}

func fluidFallingAdjustedY(y, gravity float64, falling, sprinting bool) float64 {
	if gravity == 0 || sprinting {
		return y
	}
	if falling && math.Abs(y-0.005) >= 0.003 && math.Abs(y-gravity/16) < 0.003 {
		return -0.003
	}
	return y - gravity/16
}

func (m *Module) applyWaterPhysics(s *self.Module, x, y, z, height, startY float64) {
	if m.horizontalCollision && m.onClimbableAt(x, y, z) {
		m.velY = 0.2
	}
	m.velX *= float64(m.fluid.waterDrag)
	m.velY *= float64(float32(0.8))
	m.velZ *= float64(m.fluid.waterDrag)
	m.velY = fluidFallingAdjustedY(m.velY, m.fluid.gravity, m.fluid.falling, s.Sprinting())
	m.jumpOutOfFluid(x, y, z, height, startY)
}

func (m *Module) applyLavaPhysics(s *self.Module, x, y, z, height, startY float64) {
	m.velX *= 0.5
	m.velZ *= 0.5
	if m.fluid.lavaHeight <= m.fluid.jumpThreshold() {
		m.velY *= float64(float32(0.8))
		m.velY = fluidFallingAdjustedY(m.velY, m.fluid.gravity, m.fluid.falling, s.Sprinting())
	} else {
		m.velY *= 0.5
	}
	m.velY -= m.fluid.gravity / 4
	m.jumpOutOfFluid(x, y, z, height, startY)
}

func (f *fluidState) jumpThreshold() float64 {
	if f.eyeHeight < 0.4 {
		return 0
	}
	return 0.4
}

func (m *Module) jumpOutOfFluid(x, y, z, height, startY float64) {
	if !m.horizontalCollision {
		return
	}
	box := collisions.EntityAABB(x, y, z, m.bodyWidth(), height).Move(m.velX, m.velY+float64(float32(0.6))-y+startY, m.velZ)
	if m.collisions.NoCollision(box) && !containsFluid(m.world, box) {
		m.velY = float64(float32(0.3))
	}
}

// isFree excludes every occupied fluid cell, even above the cell's surface.
func containsFluid(w fluidBlockReader, box collisions.AABB) bool {
	for x := int(math.Floor(box.MinX)); x < int(math.Ceil(box.MaxX)); x++ {
		for y := int(math.Floor(box.MinY)); y < int(math.Ceil(box.MaxY)); y++ {
			for z := int(math.Floor(box.MinZ)); z < int(math.Ceil(box.MaxZ)); z++ {
				kind, _, _ := blocks.FluidState(w.GetBlock(x, y, z))
				if kind != 0 {
					return true
				}
			}
		}
	}
	return false
}
