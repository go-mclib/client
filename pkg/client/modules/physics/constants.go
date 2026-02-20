package physics

import (
	"time"

	"github.com/go-mclib/data/pkg/data/registries"
)

// physics constants from the Minecraft Java Edition source code (1.21.11).
//
// Java uses float (32-bit) for many physics values. When widened to double (64-bit),
// the bit patterns differ from Go's native float64. Constants that are float in vanilla
// use float64(float32(x)) to match Java's float→double widening exactly.
// This is critical for anticheat prediction (e.g., Vulcan Motion Type C checks that
// jump Y velocity == 0.41999998688697815, not 0.42).
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

	// float: Block.getFriction() returns float, default 0.6F
	DefaultBlockFriction = float64(float32(0.6))

	// float: literal 0.91F in travelInAir
	AirFrictionMul = float64(float32(0.91))

	// float: literal 0.98F in travelInAir
	VerticalAirFriction = float64(float32(0.98))

	// float: xxa *= 0.98F in LivingEntity.applyInput
	InputFriction = float64(float32(0.98))

	// float: 0.21600002F literal in getFrictionInfluencedSpeed
	FrictionSpeedFactor = float64(float32(0.21600002))

	// float: getWaterSlowDown returns 0.8F
	WaterSlowdown       = float64(float32(0.8))
	WaterSprintSlowdown = float64(float32(0.9))
	WaterVerticalDrag   = float64(float32(0.8))

	// float: 0.02F literal in travelInWater
	WaterAcceleration = float64(float32(0.02))

	// double in vanilla (scale(0.5) uses double)
	LavaSlowdown      = 0.5
	LavaVerticalDrag   = 0.8
	LavaGravityFactor  = 0.25

	EntityPushStrength = 0.05
	EntityPushMinDist  = 0.01
	PlayerWidth        = 0.6
	PlayerHeight       = 1.8
	PlayerSneakingHeight = 1.5

	// double: Attributes.SNEAKING_SPEED default is 0.3 (double in attribute system)
	SneakingSpeedFactor = 0.3

	PositionThresholdSq = 4e-8 // (2e-4)²
	PositionReminderMax = 20
	TicksPerSecond      = 20
	TickDuration        = 50 * time.Millisecond

	// effect modifiers
	// float: 0.1F * (amp + 1.0F) in getJumpBoostPower, returns float
	JumpBoostPerLevel = float64(float32(0.1))

	// double: attribute modifiers
	SpeedPerLevel    = 0.2
	SlownessPerLevel = 0.15

	// double: literals in travelInAir / getEffectiveGravity
	SlowFallingGravity   = 0.01
	LevitationPerLevel   = 0.05
	LevitationLerpFactor = 0.2
)

// cached effect protocol IDs from the mob_effect registry
var (
	effectSpeed       = registries.MobEffect.Get("minecraft:speed")
	effectSlowness    = registries.MobEffect.Get("minecraft:slowness")
	effectJumpBoost   = registries.MobEffect.Get("minecraft:jump_boost")
	effectLevitation  = registries.MobEffect.Get("minecraft:levitation")
	effectSlowFalling = registries.MobEffect.Get("minecraft:slow_falling")
)
