package physics

import (
	"time"

	"github.com/go-mclib/data/registries"
)

// physics constants from the Minecraft Java Edition source code.
//
// Java uses float (32-bit) for many physics values. When widened to double (64-bit),
// the bit patterns differ from Go's native float64. Constants that are float in vanilla
// use float64(float32(x)) to match Java's float→double widening exactly.
// This is critical for anticheat prediction (e.g., Vulcan Motion Type C checks that
// jump Y velocity == 0.41999998688697815, not 0.42).
//
// Exported constants are consumed by the pathfinding jump simulation, which must
// use the exact same float math as the physics tick.
const (
	// attribute-based (double in vanilla — float64 is correct)
	Gravity         = 0.08
	sprintJumpBoost = 0.2 // double literal in jumpFromGround
	SprintModifier  = float64(float32(0.3))

	// float in vanilla (getJumpPower returns float, JUMP_STRENGTH attribute is 0.42F)
	JumpPower = float64(float32(0.42))

	// float: getSpeed() returns (float)getAttributeValue(), Player adds 0.1F
	PlayerSpeed = float64(float32(0.1))

	// float: getFlyingSpeed returns 0.02F
	FlyingSpeed       = float64(float32(0.02))
	SprintFlyingSpeed = float64(float32(0.025999999))

	// float: literal 0.91F in travelInAir
	AirFrictionMul = float64(float32(0.91))

	// float: literal 0.98F in travelInAir
	VerticalAirFriction = float64(float32(0.98))

	// float: xxa *= 0.98F in LivingEntity.applyInput
	inputFriction = float64(float32(0.98))

	// float: 0.21600002F literal in getFrictionInfluencedSpeed
	frictionSpeedFactor = float64(float32(0.21600002))

	// float: Block.getFriction() returns float, default 0.6F
	defaultBlockFriction = float64(float32(0.6))

	// double: Attributes.SNEAKING_SPEED default is 0.3 (double in attribute system)
	sneakingSpeedFactor = 0.3

	positionThresholdSq = 4e-8 // (2e-4)²
	positionReminderMax = 20
	tickDuration        = 50 * time.Millisecond

	// double: literals in travelInAir / getEffectiveGravity
	slowFallingGravity   = 0.01
	levitationPerLevel   = 0.05
	levitationLerpFactor = 0.2

	// not vanilla: cap on server-authored velocity length in blocks/tick (see setVelocity)
	maxServerVelocity = 64.0
)

// cached effect protocol IDs from the mob_effect registry
var (
	effectLevitation  = registries.MobEffect.Get("minecraft:levitation")
	effectSlowFalling = registries.MobEffect.Get("minecraft:slow_falling")
	effectJumpBoost   = registries.MobEffect.Get("minecraft:jump_boost")
)
