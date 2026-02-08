package physics

import "time"

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
	PlayerEyeHeight      = 1.62
	PositionThresholdSq  = 4e-8 // (2e-4)²
	PositionReminderMax  = 20
	TicksPerSecond       = 20
	TickDuration         = 50 * time.Millisecond
	FrictionSpeedFactor  = 0.21600002 // 0.216 = (0.6^3 * 0.91^3) normalization factor
)
