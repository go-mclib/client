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
	SprintJumpBoost = 0.2 // double literal in jumpFromGround
	SprintModifier  = 0.3 // attribute modifier (double)

	// float in vanilla (getJumpPower returns float, JUMP_STRENGTH attribute is 0.42F)
	JumpPower = float64(float32(0.42))

	// float: getSpeed() returns (float)getAttributeValue(), Player adds 0.1F
	PlayerSpeed = float64(float32(0.1))

	// float: getFlyingSpeed returns 0.02F
	FlyingSpeed = float64(float32(0.02))

	// float: literal 0.91F in travelInAir
	AirFrictionMul = float64(float32(0.91))

	// float: literal 0.98F in travelInAir
	VerticalAirFriction = float64(float32(0.98))

	// float: xxa *= 0.98F in LivingEntity.applyInput
	InputFriction = float64(float32(0.98))

	// float: 0.21600002F literal in getFrictionInfluencedSpeed
	FrictionSpeedFactor = float64(float32(0.21600002))

	// float: Block.getFriction() returns float, default 0.6F
	defaultBlockFriction = float64(float32(0.6))

	// float: getWaterSlowDown returns 0.8F
	waterSlowdown       = float64(float32(0.8))
	waterSprintSlowdown = float64(float32(0.9))
	waterVerticalDrag   = float64(float32(0.8))

	// float: 0.02F literal in travelInWater
	waterAcceleration = float64(float32(0.02))

	// double in vanilla (scale(0.5) uses double)
	lavaSlowdown      = 0.5
	lavaVerticalDrag  = 0.8
	lavaGravityFactor = 0.25

	entityPushStrength = 0.05
	entityPushMinDist  = 0.01

	// double: Attributes.SNEAKING_SPEED default is 0.3 (double in attribute system)
	sneakingSpeedFactor = 0.3

	positionThresholdSq = 4e-8 // (2e-4)²
	positionReminderMax = 20
	tickDuration        = 50 * time.Millisecond

	// double: literals in travelInAir / getEffectiveGravity
	slowFallingGravity   = 0.01
	levitationPerLevel   = 0.05
	levitationLerpFactor = 0.2
)

// cached effect protocol IDs from the mob_effect registry
var (
	effectLevitation  = registries.MobEffect.Get("minecraft:levitation")
	effectSlowFalling = registries.MobEffect.Get("minecraft:slow_falling")
)
