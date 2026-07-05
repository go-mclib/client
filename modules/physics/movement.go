package physics

import (
	"math"

	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/client/modules/world"
)

// MoveRelative computes the input vector rotated by yaw (Entity.getInputVector).
// Exported for the pathfinding jump simulation.
func MoveRelative(speed, forward, strafe, yaw float64) (dx, dy, dz float64) {
	lengthSq := forward*forward + strafe*strafe
	if lengthSq < 1e-7 {
		return 0, 0, 0
	}
	if lengthSq > 1 {
		invLen := 1.0 / math.Sqrt(lengthSq)
		forward *= invLen
		strafe *= invLen
	}
	forward *= speed
	strafe *= speed
	sinYaw := math.Sin(yaw * math.Pi / 180.0)
	cosYaw := math.Cos(yaw * math.Pi / 180.0)
	dx = strafe*cosYaw - forward*sinYaw
	dz = forward*cosYaw + strafe*sinYaw
	return dx, 0, dz
}

// GetJumpPower returns current jump power from the tracked attribute.
func (m *Module) GetJumpPower() float64 {
	return m.getJumpPower(m.self)
}

// GetEffectiveSpeed returns current movement speed from the tracked attribute.
func (m *Module) GetEffectiveSpeed() float64 {
	return m.getEffectiveSpeed(m.self)
}

// applyAirInputScaled adds movement input to velocity (pre-collision) with pre-scaled impulses.
// Returns the block friction for use in post-collision physics.
//
// Vanilla travelInAir passes raw blockFriction to getFrictionInfluencedSpeed (NOT blockFriction * 0.91).
// The 0.91 multiplier only applies to post-move velocity friction (applyAirPhysics).
func (m *Module) applyAirInputScaled(s *self.Module, x, y, z, yaw float64, w *world.Module, forward, strafe float64) float64 {
	belowBlock := w.GetBlock(int(math.Floor(x)), int(math.Floor(y-0.5)), int(math.Floor(z)))
	var blockFriction float64
	if m.onGround {
		blockFriction = GetBlockFriction(belowBlock)
	} else {
		blockFriction = 1.0
	}

	// vanilla getFrictionInfluencedSpeed: entire calculation in float32
	var speed float64
	if m.onGround {
		baseSpeed := m.getEffectiveSpeed(s)
		if s.Sprinting() {
			baseSpeed *= (1.0 + SprintModifier)
		}
		// vanilla: this.getSpeed() * (0.21600002F / (blockFriction * blockFriction * blockFriction))
		// getSpeed() returns float, all arithmetic is float32
		bf := float32(blockFriction)
		speed = float64(float32(baseSpeed) * (float32(FrictionSpeedFactor) / (bf * bf * bf)))
	} else {
		speed = FlyingSpeed
	}

	dx, _, dz := MoveRelative(speed, forward, strafe, yaw)
	m.velX += dx
	m.velZ += dz

	return blockFriction
}

// applyAirPhysics applies gravity/levitation and friction after collision (post-move).
// Matches LivingEntity.travelInAir: levitation replaces gravity, slow falling caps it.
func (m *Module) applyAirPhysics(s *self.Module, blockFriction float64) {
	// vanilla: float friction = blockFriction * 0.91F (float32 arithmetic)
	friction := float64(float32(blockFriction) * float32(AirFrictionMul))

	levAmp := s.EffectAmplifier(effectLevitation)
	if levAmp >= 0 {
		// levitation replaces gravity: lerp toward target upward velocity
		target := levitationPerLevel * float64(levAmp+1)
		m.velY += (target - m.velY) * levitationLerpFactor
	} else {
		m.velY -= m.getEffectiveGravity(s)
	}

	m.velX *= friction
	m.velZ *= friction
	m.velY *= VerticalAirFriction
}

// applyWaterInputScaled adds movement input to velocity in water (pre-collision).
func (m *Module) applyWaterInputScaled(yaw, forward, strafe float64) {
	dx, _, dz := MoveRelative(waterAcceleration, forward, strafe, yaw)
	m.velX += dx
	m.velZ += dz
}

// applyWaterPhysics applies water drag and gravity after collision (post-move).
func (m *Module) applyWaterPhysics(s *self.Module) {
	slowDown := waterSlowdown
	if s.Sprinting() {
		slowDown = waterSprintSlowdown
	}
	m.velX *= slowDown
	m.velY *= waterVerticalDrag
	m.velZ *= slowDown
	m.velY -= Gravity
}

// applyLavaInputScaled adds movement input to velocity in lava (pre-collision).
func (m *Module) applyLavaInputScaled(yaw, forward, strafe float64) {
	dx, _, dz := MoveRelative(waterAcceleration, forward, strafe, yaw)
	m.velX += dx
	m.velZ += dz
}

// applyLavaPhysics applies lava drag and gravity after collision (post-move).
func (m *Module) applyLavaPhysics() {
	m.velX *= lavaSlowdown
	m.velY *= lavaVerticalDrag
	m.velZ *= lavaSlowdown
	m.velY -= Gravity * lavaGravityFactor
}

// jump applies jump velocity (LivingEntity.jumpFromGround).
// Does NOT set onGround = false — vanilla sets it later inside Entity.move()
// after collision resolution. Keeping onGround = true ensures the jump tick
// uses ground friction/speed for input, matching vanilla behavior.
func (m *Module) jump(s *self.Module, yaw float64) {
	jp := m.getJumpPower(s)
	m.velY = max(jp, m.velY)
	if s.Sprinting() {
		angle := yaw * math.Pi / 180.0
		m.velX += -math.Sin(angle) * SprintJumpBoost
		m.velZ += math.Cos(angle) * SprintJumpBoost
	}
}

// getJumpPower returns jump velocity from the server-tracked attribute.
// The server sends jump_strength with jump boost modifiers already applied.
func (m *Module) getJumpPower(s *self.Module) float64 {
	return s.AttributeValue("minecraft:jump_strength", JumpPower)
}

// getEffectiveGravity returns gravity from the tracked attribute,
// capped to SlowFallingGravity when slow falling and descending (client-side logic).
func (m *Module) getEffectiveGravity(s *self.Module) float64 {
	g := s.AttributeValue("minecraft:gravity", Gravity)
	if m.velY <= 0 && s.HasEffect(effectSlowFalling) {
		return min(g, slowFallingGravity)
	}
	return g
}

// getEffectiveSpeed returns movement speed from the server-tracked attribute.
// The server sends movement_speed with speed/slowness modifiers already applied.
func (m *Module) getEffectiveSpeed(s *self.Module) float64 {
	return max(s.AttributeValue("minecraft:movement_speed", PlayerSpeed), 0)
}

// moveTowardsClosestSpace nudges the player out of a block it is penetrating at
// the given (x,z) body corner — a port of LocalPlayer.moveTowardsClosestSpace.
// If the corner's block column is solid, it sets horizontal velocity 0.1 toward
// the nearest non-solid neighbour so the player slides free instead of snagging.
func (m *Module) moveTowardsClosestSpace(col *collisions.Module, x, z, minY, maxY float64) {
	bx := int(math.Floor(x))
	bz := int(math.Floor(z))
	if !suffocatesAt(col, bx, bz, minY, maxY) {
		return
	}

	xd := x - float64(bx)
	zd := z - float64(bz)
	closest := math.MaxFloat64
	pushAxisX := false
	pushStep := 0.0
	found := false
	consider := func(axisX bool, step, edgeDist float64, nbx, nbz int) {
		if edgeDist < closest && !suffocatesAt(col, nbx, nbz, minY, maxY) {
			closest, pushAxisX, pushStep, found = edgeDist, axisX, step, true
		}
	}
	consider(true, -1, xd, bx-1, bz)     // west (-X)
	consider(true, 1, 1.0-xd, bx+1, bz)  // east (+X)
	consider(false, -1, zd, bx, bz-1)    // north (-Z)
	consider(false, 1, 1.0-zd, bx, bz+1) // south (+Z)
	if !found {
		return
	}
	if pushAxisX {
		m.velX = 0.1 * pushStep
	} else {
		m.velZ = 0.1 * pushStep
	}
}

// suffocatesAt reports whether the player's body column at block (bx,bz) is
// inside a colliding block (penetrating it), matching LocalPlayer.suffocatesAt.
func suffocatesAt(col *collisions.Module, bx, bz int, minY, maxY float64) bool {
	test := collisions.NewAABB(float64(bx), minY, float64(bz), float64(bx)+1, maxY, float64(bz)+1).
		Inflate(-collisions.Epsilon, -collisions.Epsilon, -collisions.Epsilon)
	return col.CollidesWith(test)
}

// modifyInput processes raw movement input matching vanilla LocalPlayer.modifyInput:
// 1. scale by InputFriction (0.98)
// 2. scale by sneakingSpeedFactor if sneaking
// 3. normalize diagonal to unit square distance (modifyInputSpeedForSquareMovement)
func modifyInput(forward, strafe float64, sneaking bool) (float64, float64) {
	if forward == 0 && strafe == 0 {
		return 0, 0
	}

	forward *= InputFriction
	strafe *= InputFriction

	if sneaking {
		forward *= sneakingSpeedFactor
		strafe *= sneakingSpeedFactor
	}

	// modifyInputSpeedForSquareMovement: clamp magnitude to distance-to-unit-square
	length := math.Sqrt(forward*forward + strafe*strafe)
	if length > 0 {
		dirF := forward / length
		dirS := strafe / length
		absF := math.Abs(dirF)
		absS := math.Abs(dirS)
		// distanceToUnitSquare: 1 / cos(atan(min/max))
		var tan float64
		if absS > absF {
			tan = absF / absS
		} else {
			tan = absS / absF
		}
		distToSquare := math.Sqrt(1 + tan*tan)
		modifiedLength := min(length*distToSquare, 1.0)
		forward = dirF * modifiedLength
		strafe = dirS * modifiedLength
	}

	return forward, strafe
}

// notEqual returns true if two float64 values differ by more than 1e-5.
// Matches vanilla Mth.equal (Math.abs(b - a) < 1.0E-5F).
func notEqual(a, b float64) bool {
	return math.Abs(a-b) >= 1e-5
}
