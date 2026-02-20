package physics

import (
	"time"

	"github.com/go-mclib/data/pkg/data/registries"
)

// physics constants from the Minecraft Java Edition source code (1.21.11)
const (
	Gravity              = 0.08
	JumpPower            = 0.42
	SprintJumpBoost      = 0.2
	PlayerSpeed          = 0.1
	FlyingSpeed          = 0.02
	SprintModifier       = 0.3
	DefaultBlockFriction = 0.6
	AirFrictionMul       = 0.91
	VerticalAirFriction  = 0.98
	WaterSlowdown        = 0.8
	WaterSprintSlowdown  = 0.9
	WaterVerticalDrag    = 0.8
	WaterAcceleration    = 0.02
	LavaSlowdown         = 0.5
	LavaVerticalDrag     = 0.8
	LavaGravityFactor    = 0.25 // gravity / 4
	EntityPushStrength   = 0.05
	EntityPushMinDist    = 0.01
	PlayerWidth          = 0.6
	PlayerHeight         = 1.8
	PlayerSneakingHeight = 1.5
	SneakingSpeedFactor  = 0.3  // Attributes.SNEAKING_SPEED default
	PositionThresholdSq  = 4e-8 // (2e-4)²
	PositionReminderMax  = 20
	TicksPerSecond       = 20
	TickDuration         = 50 * time.Millisecond
	FrictionSpeedFactor  = 0.21600002 // 0.216 = (0.6^3 * 0.91^3) normalization factor

	// effect modifiers (MobEffects.java + LivingEntity.java)
	JumpBoostPerLevel    = 0.1  // added to jump velocity per amplifier+1
	SpeedPerLevel        = 0.2  // ADD_MULTIPLIED_TOTAL per amplifier+1
	SlownessPerLevel     = 0.15 // ADD_MULTIPLIED_TOTAL (negative) per amplifier+1
	SlowFallingGravity   = 0.01 // gravity cap when slow falling and descending
	LevitationPerLevel   = 0.05 // target upward velocity per amplifier+1
	LevitationLerpFactor = 0.2  // lerp speed toward target velocity
)

// cached effect protocol IDs from the mob_effect registry
var (
	effectSpeed       = registries.MobEffect.Get("minecraft:speed")
	effectSlowness    = registries.MobEffect.Get("minecraft:slowness")
	effectJumpBoost   = registries.MobEffect.Get("minecraft:jump_boost")
	effectLevitation  = registries.MobEffect.Get("minecraft:levitation")
	effectSlowFalling = registries.MobEffect.Get("minecraft:slow_falling")
)
