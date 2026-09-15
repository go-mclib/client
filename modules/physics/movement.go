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
	speed = float64(float32(speed))
	forward *= speed
	strafe *= speed
	sinYaw, cosYaw := sinCosYaw(yaw)
	dx = float64(strafe*cosYaw) - float64(forward*sinYaw)
	dz = float64(forward*cosYaw) + float64(strafe*sinYaw)
	return dx, 0, dz
}

// Mth uses a float sine table, indexed by a double angle in Minecraft 26.2.
var sineTable = func() [65536]float32 {
	var table [65536]float32
	for i := range table {
		table[i] = float32(math.Sin(float64(i) / 10430.378350470453))
	}
	return table
}()

func sinCos(angle float64) (float64, float64) {
	return float64(sineTable[uint64(int64(angle*10430.378350470453))&65535]),
		float64(sineTable[uint64(int64(angle*10430.378350470453+16384.0))&65535])
}

func sinCosYaw(yaw float64) (float64, float64) {
	return sinCos(float64(float32(yaw) * float32(math.Pi/180)))
}

// ModifiedFriction applies the server's friction/air-drag attribute in float arithmetic.
func ModifiedFriction(base, modifier float64) float64 {
	return float64(max(float32(0), min(float32(1), float32(1)-float32(float32(1)-float32(base))*float32(modifier))))
}

// GroundAcceleration is LivingEntity.getFrictionInfluencedSpeed on the ground.
// blockFriction is the material friction after its attribute modifier, before air drag.
func GroundAcceleration(speed, blockFriction float64) float64 {
	bf := float32(blockFriction)
	if float64(bf) > 0.6 {
		return float64(float32(speed) * (float32(FrictionSpeedFactor) / (bf * bf * bf)))
	}
	return float64(float32(speed))
}

// AirVelocity applies post-move gravity and air drag. Callers apply levitation
// to vy first and pass zero gravity when it replaces normal falling.
func AirVelocity(vx, vy, vz, blockFriction, gravity, airDragModifier float64) (float64, float64, float64) {
	friction := float64(float32(blockFriction) * float32(ModifiedFriction(AirFrictionMul, airDragModifier)))
	return vx * friction, (vy - gravity) * ModifiedFriction(VerticalAirFriction, airDragModifier), vz * friction
}

// JumpImpulse returns the horizontal sprint impulse and minimum upward velocity.
func JumpImpulse(yaw, jumpPower float64, sprinting bool) (float64, float64, float64) {
	jumpPower = float64(float32(jumpPower))
	if jumpPower <= float64(float32(1e-5)) {
		return 0, 0, 0
	}
	if sprinting {
		sinYaw, cosYaw := sinCosYaw(yaw)
		return -sinYaw * SprintJumpBoost, jumpPower, cosYaw * SprintJumpBoost
	}
	return 0, jumpPower, 0
}

// GetBlockFrictionAt uses the collision support beneath the player's footprint,
// including an overhanging fence or the edge of a block, not only the center cell.
func GetBlockFrictionAt(w *world.Module, x, y, z, width float64) float64 {
	pos := blockOnPos(w, x, y, z, width, float64(float32(0.500001)))
	return GetBlockFriction(w.GetBlock(pos[0], pos[1], pos[2]))
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
	blockFriction := 1.0
	speed := FlyingSpeed
	if s.Sprinting() {
		speed = SprintFlyingSpeed
	}
	if s.AbilityFlags()&2 != 0 {
		speed = float64(s.FlyingSpeed())
		if s.Sprinting() {
			speed = float64(s.FlyingSpeed() * 2)
		}
	}
	if m.onGround {
		blockFriction = ModifiedFriction(GetBlockFrictionAt(w, x, y, z, m.bodyWidth()), s.AttributeValue("minecraft:friction_modifier", 1))
		speed = GroundAcceleration(m.getEffectiveSpeed(s), blockFriction)
	}
	dx, _, dz := MoveRelative(speed, forward, strafe, yaw)
	m.velX += dx
	m.velZ += dz
	if m.onClimbableAt(x, y, z) {
		m.fallDistance = 0
		limit := float64(float32(0.15))
		m.velX = max(-limit, min(limit, m.velX))
		m.velZ = max(-limit, min(limit, m.velZ))
		m.velY = max(-limit, m.velY)
		if m.velY < 0 && s.Sneaking() && !isScaffolding(w.GetBlock(int(math.Floor(x)), int(math.Floor(y)), int(math.Floor(z)))) {
			m.velY = 0
		}
	}
	return blockFriction
}

// applyAirPhysics applies gravity/levitation and friction after collision (post-move).
// Matches LivingEntity.travelInAir: levitation replaces gravity, slow falling caps it.
func (m *Module) applyAirPhysics(s *self.Module, blockFriction float64) {
	x, y, z := s.Position()
	if (m.horizontalCollision || m.jumping) && (m.onClimbableAt(x, y, z) || m.blocks.inPowderSnow && m.canWalkOnPowderSnow()) {
		m.velY = 0.2
	}
	gravity := m.getEffectiveGravity(s)
	if levAmp := s.EffectAmplifier(effectLevitation); levAmp >= 0 {
		target := levitationPerLevel * float64(levAmp+1)
		m.velY += (target - m.velY) * levitationLerpFactor
		gravity = 0
	}
	m.velX, m.velY, m.velZ = AirVelocity(m.velX, m.velY, m.velZ, blockFriction, gravity, s.AttributeValue("minecraft:air_drag_modifier", 1))
}

// jump applies jump velocity (LivingEntity.jumpFromGround).
// Does NOT set onGround = false — vanilla sets it later inside Entity.move()
// after collision resolution. Keeping onGround = true ensures the jump tick
// uses ground friction/speed for input, matching vanilla behavior.
func (m *Module) jump(s *self.Module, yaw float64) {
	dx, jumpY, dz := JumpImpulse(yaw, m.getJumpPower(s), s.Sprinting())
	if jumpY == 0 {
		return
	}
	m.velX += dx
	m.velY = max(jumpY, m.velY)
	m.velZ += dz
}

// getJumpPower includes the block jump factor and the separate Jump Boost effect.
func (m *Module) getJumpPower(s *self.Module) float64 {
	factor := float32(1)
	if m.world != nil {
		x, y, z := s.Position()
		factor = float32(blockJumpFactorAt(m.world, x, y, z, m.bodyWidth()))
	}
	power := float32(s.AttributeValue("minecraft:jump_strength", JumpPower)) * factor
	if amp := s.EffectAmplifier(effectJumpBoost); amp >= 0 {
		power += float32(0.1) * (float32(amp) + 1)
	}
	return float64(power)
}

// getEffectiveGravity returns gravity from the tracked attribute,
// capped to SlowFallingGravity when slow falling and descending (client-side logic).
func (m *Module) getEffectiveGravity(s *self.Module) float64 {
	if s.NoGravity() {
		return 0
	}
	g := s.AttributeValue("minecraft:gravity", Gravity)
	if m.velY <= 0 && s.HasEffect(effectSlowFalling) {
		return min(g, slowFallingGravity)
	}
	return g
}

// getEffectiveSpeed projects the current local sprint state onto server attributes.
func (m *Module) getEffectiveSpeed(s *self.Module) float64 {
	return s.MovementSpeed(s.Sprinting())
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
	return col.CollidesWithSuffocatingBlock(test)
}

// ProcessInput follows KeyboardInput normalization and LocalPlayer.modifyInput.
// itemFactor and slowFactor are applied separately, retaining Java float rounding.
func ProcessInput(forward, strafe, itemFactor, slowFactor float64) (float64, float64) {
	f, s := float32(forward), float32(strafe)
	length := float32(math.Sqrt(float64(f*f + s*s)))
	if length == 0 {
		return 0, 0
	}
	if length > 1 {
		inverse := float32(1) / length
		f, s = f*inverse, s*inverse
	}
	f, s = f*float32(InputFriction), s*float32(InputFriction)
	f, s = f*float32(itemFactor), s*float32(itemFactor)
	f, s = f*float32(slowFactor), s*float32(slowFactor)
	length = float32(math.Sqrt(float64(f*f + s*s)))
	if length <= 0 {
		return 0, 0
	}
	inverse := float32(1) / length
	dirF, dirS := f*inverse, s*inverse
	absF, absS := float32(math.Abs(float64(dirF))), float32(math.Abs(float64(dirS)))
	tan := min(absF, absS) / max(absF, absS)
	distanceToSquare := float32(math.Sqrt(float64(float32(1) + tan*tan)))
	modifiedLength := min(length*distanceToSquare, float32(1))
	return float64(dirF * modifiedLength), float64(dirS * modifiedLength)
}

// notEqual returns true if two float64 values differ by more than 1e-5.
// Matches vanilla Mth.equal (Math.abs(b - a) < 1.0E-5F).
func notEqual(a, b float64) bool {
	return math.Abs(a-b) >= float64(float32(1e-5))
}
