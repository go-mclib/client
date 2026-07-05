package self

import (
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func (m *Module) EntityID() int32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.entityID
}
func (m *Module) IsHardcore() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.isHardcore
}
func (m *Module) DimensionNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dimensionNames
}
func (m *Module) MaxPlayers() int32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.maxPlayers
}
func (m *Module) ViewDistance() int32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.viewDistance
}
func (m *Module) SimulationDistance() int32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.simulationDistance
}
func (m *Module) ReducedDebugInfo() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.reducedDebugInfo
}
func (m *Module) EnableRespawnScreen() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.enableRespawnScreen
}
func (m *Module) DoLimitedCrafting() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.doLimitedCrafting
}
func (m *Module) DimensionType() int32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dimensionType
}
func (m *Module) DimensionName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dimensionName
}
func (m *Module) HashedSeed() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.hashedSeed
}
func (m *Module) Gamemode() uint8 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.gamemode
}
func (m *Module) PreviousGameMode() int8 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.previousGameMode
}
func (m *Module) IsDebug() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.isDebug
}
func (m *Module) IsFlat() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.isFlat
}
func (m *Module) PortalCooldown() int32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.portalCooldown
}
func (m *Module) SeaLevel() int32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.seaLevel
}
func (m *Module) EnforcesSecureChat() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.enforcesSecureChat
}
func (m *Module) Health() float32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.health
}
func (m *Module) Food() int32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.food
}
func (m *Module) FoodSaturation() float32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.foodSaturation
}
func (m *Module) ExperienceBar() float32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.experienceBar
}
func (m *Module) Level() int32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.level
}
func (m *Module) TotalExperience() int32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.totalExperience
}
func (m *Module) Difficulty() uint8 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.difficulty
}
func (m *Module) DifficultyLocked() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.difficultyLocked
}
func (m *Module) AbilityFlags() int8 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.abilityFlags
}
func (m *Module) FlyingSpeed() float32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.flyingSpeed
}
func (m *Module) FOVModifier() float32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.fovModifier
}
func (m *Module) WorldAge() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.worldAge
}
func (m *Module) TimeOfDay() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.timeOfDay
}
func (m *Module) TimeIncreasing() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.timeIncreasing
}
func (m *Module) OpLevel() int8 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.opLevel
}
func (m *Module) IsDead() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.health <= 0
}

// IsCreative reports whether the player has the instant-build ability (creative mode).
func (m *Module) IsCreative() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.abilityFlags&0x08 != 0
}

// CanUseGameMasterBlocks reports whether the player may edit command blocks:
// instant-build (creative) plus operator permission level >= 2. Mirrors
// Player.canUseGameMasterBlocks in the vanilla source.
func (m *Module) CanUseGameMasterBlocks() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.abilityFlags&0x08 != 0 && m.opLevel >= 2
}

func (m *Module) DeathLocation() ns.PrefixedOptional[ns.GlobalPos] {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.deathLocation
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
