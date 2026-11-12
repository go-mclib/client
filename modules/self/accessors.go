package self

import (
	"slices"

	"github.com/go-mclib/data/packets"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// Vitals is the S2CSetHealth state.
type Vitals struct {
	Health         float32
	Food           int32
	FoodSaturation float32
}

// Experience is the S2CSetExperience state.
type Experience struct {
	Bar   float32 // progress to the next level, 0..1
	Level int32
	Total int32
}

// Abilities is the S2CPlayerAbilities state.
type Abilities struct {
	Flags       int8
	FlyingSpeed float32
	FOVModifier float32
}

// WorldTime is the S2CSetTime state. TimeOfDay and TimeIncreasing come from
// the first clock update (the overworld clock).
type WorldTime struct {
	WorldAge       int64
	TimeOfDay      int64
	TimeIncreasing bool
}

// Difficulty is the S2CChangeDifficulty state.
type Difficulty struct {
	Difficulty uint8
	Locked     bool
}

// LoginPacket returns a copy of the last S2CLogin, with the spawn fields
// (dimension, game modes, seed, death location, portal cooldown, sea level)
// updated by later respawns and game-mode changes. It is the zero value before
// login.
func (m *Module) LoginPacket() packets.S2CLogin {
	m.mu.RLock()
	defer m.mu.RUnlock()
	login := m.login
	login.DimensionNames = slices.Clone(login.DimensionNames)
	return login
}

func (m *Module) EntityID() int32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return int32(m.login.EntityID)
}
func (m *Module) DimensionType() int32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return int32(m.login.DimensionType)
}
func (m *Module) DimensionName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return string(m.login.DimensionName)
}
func (m *Module) Gamemode() uint8 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return uint8(m.login.GameMode)
}
func (m *Module) Vitals() Vitals {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.vitals
}
func (m *Module) Experience() Experience {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.experience
}
func (m *Module) Abilities() Abilities {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.abilities
}
func (m *Module) WorldTime() WorldTime {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.time
}
func (m *Module) DifficultyState() Difficulty {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.difficulty
}
func (m *Module) OpLevel() int8 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.opLevel
}
func (m *Module) IsDead() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.vitals.Health <= 0
}

// Player ability flags (S2CPlayerAbilities) and the spectator game mode.
const (
	abilityFlying     = 0x02
	abilityMayFly     = 0x04
	abilityInstabuild = 0x08

	gamemodeSpectator = 3
)

// IsCreative reports whether the player has the instant-build ability (creative mode).
func (m *Module) IsCreative() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.abilities.Flags&abilityInstabuild != 0
}

// IsFlying reports the synchronized flying ability flag.
func (m *Module) IsFlying() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.abilities.Flags&abilityFlying != 0
}

// MayFly reports whether the player is allowed to fly (creative/spectator).
func (m *Module) MayFly() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.abilities.Flags&abilityMayFly != 0
}

// IsSpectator reports whether the player is in spectator mode.
func (m *Module) IsSpectator() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.login.GameMode == gamemodeSpectator
}

// CanUseGameMasterBlocks reports whether the player may edit command blocks:
// instant-build (creative) plus operator permission level >= 2. Mirrors
// Player.canUseGameMasterBlocks in the vanilla source.
func (m *Module) CanUseGameMasterBlocks() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.abilities.Flags&abilityInstabuild != 0 && m.opLevel >= 2
}

func (m *Module) Position() (x, y, z float64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.x, m.y, m.z
}

func (m *Module) Rotation() (yaw, pitch float32) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.yaw, m.pitch
}

func (m *Module) SpawnPoint() (dim string, pos ns.Position, yaw, pitch float32) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.spawnDimension, m.spawnPosition, m.spawnYaw, m.spawnPitch
}

func (m *Module) AutoRespawn() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.autoRespawn
}
func (m *Module) SetAutoRespawn(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.autoRespawn = v
}
func (m *Module) Sprinting() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sprinting
}
func (m *Module) SetSprinting(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sprinting = v
}
func (m *Module) Sneaking() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sneaking
}
func (m *Module) SetSneaking(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sneaking = v
}
func (m *Module) SuppressPositionEcho() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.suppressPositionEcho
}
func (m *Module) SetSuppressPositionEcho(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.suppressPositionEcho = v
}

// SetPosition updates the player's position directly (used by physics module).
func (m *Module) SetPosition(x, y, z float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.x = x
	m.y = y
	m.z = z
}

// SetEyeHeight publishes the eye height of the current pose (already scaled).
// The physics module calls it every tick; 0 restores the fallback estimate.
func (m *Module) SetEyeHeight(height float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eyeHeight = height
}

// CurrentEyeHeight returns the eye height above the feet for the current pose.
// It is the value published by physics, or, without physics, an estimate from
// the sleeping/sneaking state and the clamped minecraft:scale attribute.
// EyeHeight is the unscaled standing constant.
func (m *Module) CurrentEyeHeight() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.eyeHeightLocked()
}

// EyePosition returns the world position of the player's eyes.
func (m *Module) EyePosition() (x, y, z float64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.x, m.y + m.eyeHeightLocked(), m.z
}

func (m *Module) eyeHeightLocked() float64 {
	if m.eyeHeight > 0 {
		return m.eyeHeight
	}
	if m.sleeping {
		return float64(float32(0.2))
	}
	height := float32(EyeHeight)
	if m.sneaking {
		height = 1.27
	}
	return float64(height * m.scaleLocked())
}
