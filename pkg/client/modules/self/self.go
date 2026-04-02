package self

import (
	"encoding/binary"
	"sync"

	"github.com/go-mclib/client/pkg/client"
	"github.com/go-mclib/data/pkg/data/packet_ids"
	"github.com/go-mclib/data/pkg/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

const (
	ModuleName = "self"
	EyeHeight  = 1.62
)

type Module struct {
	client *client.Client
	mu     sync.RWMutex

	autoRespawn bool

	// login state
	entityID            int32
	isHardcore          bool
	dimensionNames      []string
	maxPlayers          int32
	viewDistance        int32
	simulationDistance  int32
	reducedDebugInfo    bool
	enableRespawnScreen bool
	doLimitedCrafting   bool
	dimensionType       int32
	dimensionName       string
	hashedSeed          int64
	gamemode            uint8
	previousGameMode    int8
	isDebug             bool
	isFlat              bool
	deathLocation       ns.PrefixedOptional[ns.GlobalPos]
	portalCooldown      int32
	seaLevel            int32
	enforcesSecureChat  bool

	// health & experience
	health          float32
	food            int32
	foodSaturation  float32
	experienceBar   float32
	level           int32
	totalExperience int32

	// position & rotation
	x, y, z float64
	yaw     float32
	pitch   float32

	// difficulty
	difficulty       uint8
	difficultyLocked bool

	// abilities
	abilityFlags int8
	flyingSpeed  float32
	fovModifier  float32

	// spawn position
	spawnDimension string
	spawnPosition  ns.Position
	spawnYaw       float32
	spawnPitch     float32

	// time
	worldAge       int64
	timeOfDay      int64
	timeIncreasing bool

	// op level (0-4, derived from entity event status 24-28)
	opLevel int8

	// when true, an external controller (e.g. pproxy) handles teleport confirms
	suppressPositionEcho bool

	// movement state flags
	sprinting bool
	sneaking  bool

	attributes map[string]*Attribute

	effectsMu     sync.Mutex
	activeEffects map[int32]*EffectInstance

	onDeath            []func()
	onSpawn            []func()
	onRespawn          []func()
	onHealthSet        []func(health, food float32)
	onPosition         []func(x, y, z float64)
	onGameEvent        []func(event uint8, value float32)
	onGamemodeChange   []func(gamemode uint8)
	onDimensionChange  []func(dimensionName string)
	onEffectAdded      []func(effectID, amplifier, duration int32)
	onEffectRemoved    []func(effectID int32)
	onDifficultyChange []func(difficulty uint8, locked bool)
	onAbilitiesChange  []func(flags int8, flySpeed, fovMod float32)
	onTimeUpdate       []func(worldAge, timeOfDay int64)
	onExperienceChange []func(bar float32, level, total int32)
	onAttributeUpdate  []func(name string, value float64)
}

func New() *Module {
	return &Module{
		autoRespawn:    true,
		health:         20,
		food:           20,
		foodSaturation: 5,
		flyingSpeed:    0.05,
		fovModifier:    0.1,
		activeEffects:  make(map[int32]*EffectInstance),
		attributes:     make(map[string]*Attribute),
	}
}

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c
	c.OnTransfer(m.Reset)

	// clear world and entity state on dimension change/respawn
	m.OnRespawn(func() {
		if w := c.Module("world"); w != nil {
			type chunkClearer interface{ ClearChunks() }
			if cc, ok := w.(chunkClearer); ok {
				cc.ClearChunks()
			}
		}
		if e := c.Module("entities"); e != nil {
			e.Reset()
		}
	})
}

func (m *Module) Reset() {
	m.mu.Lock()
	m.health = 20
	m.food = 20
	m.foodSaturation = 5
	m.experienceBar = 0
	m.level = 0
	m.totalExperience = 0
	m.x = 0
	m.y = 0
	m.z = 0
	m.yaw = 0
	m.pitch = 0
	m.sprinting = false
	m.sneaking = false
	m.difficulty = 0
	m.difficultyLocked = false
	m.abilityFlags = 0
	m.flyingSpeed = 0.05
	m.fovModifier = 0.1
	m.spawnDimension = ""
	m.spawnPosition = ns.Position{}
	m.spawnYaw = 0
	m.spawnPitch = 0
	m.worldAge = 0
	m.timeOfDay = 0
	m.timeIncreasing = false
	m.opLevel = 0
	clear(m.attributes)
	m.mu.Unlock()
	m.effectsMu.Lock()
	clear(m.activeEffects)
	m.effectsMu.Unlock()
}

// From retrieves the self module from a client.
func From(c *client.Client) *Module {
	mod := c.Module(ModuleName)
	if mod == nil {
		return nil
	}
	return mod.(*Module)
}

// --- getters (read-only) ---

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

// --- getters + setters (external write) ---

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

// --- events ---

func (m *Module) OnDeath(cb func())   { m.onDeath = append(m.onDeath, cb) }
func (m *Module) OnSpawn(cb func())   { m.onSpawn = append(m.onSpawn, cb) }
func (m *Module) OnRespawn(cb func()) { m.onRespawn = append(m.onRespawn, cb) }
func (m *Module) OnHealthSet(cb func(health, food float32)) {
	m.onHealthSet = append(m.onHealthSet, cb)
}
func (m *Module) OnPosition(cb func(x, y, z float64)) { m.onPosition = append(m.onPosition, cb) }
func (m *Module) OnGameEvent(cb func(event uint8, value float32)) {
	m.onGameEvent = append(m.onGameEvent, cb)
}
func (m *Module) OnGamemodeChange(cb func(gamemode uint8)) {
	m.onGamemodeChange = append(m.onGamemodeChange, cb)
}
func (m *Module) OnDimensionChange(cb func(dimensionName string)) {
	m.onDimensionChange = append(m.onDimensionChange, cb)
}
func (m *Module) OnEffectAdded(cb func(effectID, amplifier, duration int32)) {
	m.onEffectAdded = append(m.onEffectAdded, cb)
}
func (m *Module) OnEffectRemoved(cb func(effectID int32)) {
	m.onEffectRemoved = append(m.onEffectRemoved, cb)
}
func (m *Module) OnDifficultyChange(cb func(difficulty uint8, locked bool)) {
	m.onDifficultyChange = append(m.onDifficultyChange, cb)
}
func (m *Module) OnAbilitiesChange(cb func(flags int8, flySpeed, fovMod float32)) {
	m.onAbilitiesChange = append(m.onAbilitiesChange, cb)
}
func (m *Module) OnTimeUpdate(cb func(worldAge, timeOfDay int64)) {
	m.onTimeUpdate = append(m.onTimeUpdate, cb)
}
func (m *Module) OnExperienceChange(cb func(bar float32, level, total int32)) {
	m.onExperienceChange = append(m.onExperienceChange, cb)
}
func (m *Module) OnAttributeUpdate(cb func(name string, value float64)) {
	m.onAttributeUpdate = append(m.onAttributeUpdate, cb)
}

// --- packet handlers ---

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	if m.client.State() != jp.StatePlay {
		return
	}
	switch pkt.PacketID {
	case packet_ids.S2CLoginID:
		m.handleLogin(pkt)
	case packet_ids.S2CSetHealthID:
		m.handleSetHealth(pkt)
	case packet_ids.S2CSetExperienceID:
		m.handleSetExperience(pkt)
	case packet_ids.S2CPlayerPositionID:
		m.handlePlayerPosition(pkt)
	case packet_ids.S2CPlayerCombatKillID:
		m.handleCombatKill(pkt)
	case packet_ids.S2CGameEventID:
		m.handleGameEvent(pkt)
	case packet_ids.S2CUpdateMobEffectID:
		m.handleUpdateMobEffect(pkt)
	case packet_ids.S2CRemoveMobEffectID:
		m.handleRemoveMobEffect(pkt)
	case packet_ids.S2CChangeDifficultyID:
		m.handleChangeDifficulty(pkt)
	case packet_ids.S2CPlayerAbilitiesID:
		m.handlePlayerAbilities(pkt)
	case packet_ids.S2CSetDefaultSpawnPositionID:
		m.handleSetDefaultSpawnPosition(pkt)
	case packet_ids.S2CSetTimeID:
		m.handleSetTime(pkt)
	case packet_ids.S2CEntityEventID:
		m.handleEntityEvent(pkt)
	case packet_ids.S2CRespawnID:
		m.handleRespawn(pkt)
	case packet_ids.S2CUpdateAttributesID:
		m.handleUpdateAttributes(pkt)
	}
}

func (m *Module) handleLogin(pkt *jp.WirePacket) {
	var d packets.S2CLogin
	if err := pkt.ReadInto(&d); err != nil {
		m.client.Logger.Println("failed to parse login play data:", err)
		return
	}

	m.mu.Lock()
	m.entityID = int32(d.EntityId)
	m.isHardcore = bool(d.IsHardcore)
	m.dimensionNames = make([]string, len(d.DimensionNames))
	for i, name := range d.DimensionNames {
		m.dimensionNames[i] = string(name)
	}
	m.maxPlayers = int32(d.MaxPlayers)
	m.viewDistance = int32(d.ViewDistance)
	m.simulationDistance = int32(d.SimulationDistance)
	m.reducedDebugInfo = bool(d.ReducedDebugInfo)
	m.enableRespawnScreen = bool(d.EnableRespawnScreen)
	m.doLimitedCrafting = bool(d.DoLimitedCrafting)
	m.dimensionType = int32(d.DimensionType)
	m.dimensionName = string(d.DimensionName)
	m.hashedSeed = int64(d.HashedSeed)
	m.gamemode = uint8(d.GameMode)
	m.previousGameMode = int8(d.PreviousGameMode)
	m.isDebug = bool(d.IsDebug)
	m.isFlat = bool(d.IsFlat)
	m.deathLocation = d.DeathLocation
	m.portalCooldown = int32(d.PortalCooldown)
	m.seaLevel = int32(d.SeaLevel)
	m.enforcesSecureChat = bool(d.EnforcesSecureChat)
	autoRespawn := m.autoRespawn
	m.mu.Unlock()

	m.client.Logger.Println("spawned; ready")

	if m.client.Interactive {
		m.client.EnableInput()
	}

	_ = m.client.WritePacket(&packets.C2SPlayerLoaded{})

	if autoRespawn {
		m.Respawn()
	}

	for _, cb := range m.onSpawn {
		cb()
	}
}

func (m *Module) handleRespawn(pkt *jp.WirePacket) {
	var d packets.S2CRespawn
	if err := pkt.ReadInto(&d); err != nil {
		m.client.Logger.Println("failed to parse respawn data:", err)
		return
	}

	m.mu.Lock()
	oldDim := m.dimensionName
	oldGamemode := m.gamemode

	m.dimensionType = int32(d.DimensionType)
	m.dimensionName = string(d.DimensionName)
	m.hashedSeed = int64(d.HashedSeed)
	m.gamemode = uint8(d.GameMode)
	m.previousGameMode = int8(d.PreviousGameMode)
	m.isDebug = bool(d.IsDebug)
	m.isFlat = bool(d.IsFlat)
	m.portalCooldown = int32(d.PortalCooldown)
	m.seaLevel = int32(d.SeaLevel)

	m.x = 0
	m.y = 0
	m.z = 0

	if d.DataKept&0x01 == 0 {
		m.health = 20
		m.food = 20
		m.foodSaturation = 5
	}

	newDim := m.dimensionName
	newGamemode := m.gamemode
	m.mu.Unlock()

	m.effectsMu.Lock()
	clear(m.activeEffects)
	m.effectsMu.Unlock()

	m.client.Logger.Printf("respawned in %s", d.DimensionName)

	for _, cb := range m.onRespawn {
		cb()
	}
	if newDim != oldDim {
		for _, cb := range m.onDimensionChange {
			cb(newDim)
		}
	}
	if newGamemode != oldGamemode {
		for _, cb := range m.onGamemodeChange {
			cb(newGamemode)
		}
	}
}

func (m *Module) handleSetHealth(pkt *jp.WirePacket) {
	var d packets.S2CSetHealth
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	m.mu.Lock()
	wasDead := m.health <= 0
	m.health = float32(d.Health)
	m.food = int32(d.Food)
	m.foodSaturation = float32(d.FoodSaturation)
	isDead := m.health <= 0
	health, food := m.health, float32(m.food)
	m.mu.Unlock()

	for _, cb := range m.onHealthSet {
		cb(health, food)
	}

	if isDead && !wasDead {
		for _, cb := range m.onDeath {
			cb()
		}
	}
}

func (m *Module) handleSetExperience(pkt *jp.WirePacket) {
	var d packets.S2CSetExperience
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	m.mu.Lock()
	m.experienceBar = float32(d.ExperienceBar)
	m.level = int32(d.Level)
	m.totalExperience = int32(d.TotalExperience)
	bar, level, total := m.experienceBar, m.level, m.totalExperience
	m.mu.Unlock()

	for _, cb := range m.onExperienceChange {
		cb(bar, level, total)
	}
}

func (m *Module) handlePlayerPosition(pkt *jp.WirePacket) {
	var d packets.S2CPlayerPosition
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	flags := int32(d.Flags)

	m.mu.Lock()
	if flags&0x01 != 0 {
		m.x += float64(d.X)
	} else {
		m.x = float64(d.X)
	}
	if flags&0x02 != 0 {
		m.y += float64(d.Y)
	} else {
		m.y = float64(d.Y)
	}
	if flags&0x04 != 0 {
		m.z += float64(d.Z)
	} else {
		m.z = float64(d.Z)
	}
	if flags&0x08 != 0 {
		m.yaw += float32(d.Yaw)
	} else {
		m.yaw = float32(d.Yaw)
	}
	if flags&0x10 != 0 {
		m.pitch += float32(d.Pitch)
	} else {
		m.pitch = float32(d.Pitch)
	}

	suppress := m.suppressPositionEcho
	x, y, z := m.x, m.y, m.z
	yaw, pitch := m.yaw, m.pitch
	m.mu.Unlock()

	if !suppress {
		_ = m.client.WritePacket(&packets.C2SAcceptTeleportation{
			TeleportId: d.TeleportId,
		})
		_ = m.client.WritePacket(&packets.C2SMovePlayerPosRot{
			X: ns.Float64(x), FeetY: ns.Float64(y), Z: ns.Float64(z),
			Yaw: ns.Float32(yaw), Pitch: ns.Float32(pitch),
			Flags: 0,
		})
	}

	for _, cb := range m.onPosition {
		cb(x, y, z)
	}
}

func (m *Module) handleGameEvent(pkt *jp.WirePacket) {
	var d packets.S2CGameEvent
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	event := uint8(d.Event)
	value := float32(d.Value)

	// game mode change (event 3)
	var gamemodeChanged bool
	var newMode uint8
	if event == 3 {
		newMode = uint8(d.Value)
		m.mu.Lock()
		if newMode != m.gamemode {
			m.gamemode = newMode
			gamemodeChanged = true
		}
		m.mu.Unlock()
	}

	if gamemodeChanged {
		for _, cb := range m.onGamemodeChange {
			cb(newMode)
		}
	}

	for _, cb := range m.onGameEvent {
		cb(event, value)
	}
}

func (m *Module) handleCombatKill(pkt *jp.WirePacket) {
	var d packets.S2CPlayerCombatKill
	if err := pkt.ReadInto(&d); err != nil {
		m.client.Logger.Printf("failed to parse player combat kill data: %s", err)
		return
	}

	m.mu.RLock()
	isUs := int32(d.PlayerId) == m.entityID
	autoRespawn := m.autoRespawn
	m.mu.RUnlock()

	if isUs {
		m.client.Logger.Printf("died: %++v", d.Message)
		for _, cb := range m.onDeath {
			cb()
		}
		if autoRespawn {
			m.Respawn()
		}
	}
}

func (m *Module) handleChangeDifficulty(pkt *jp.WirePacket) {
	var d packets.S2CChangeDifficulty
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	m.mu.Lock()
	m.difficulty = uint8(d.Difficulty)
	m.difficultyLocked = bool(d.DifficultyLocked)
	diff, locked := m.difficulty, m.difficultyLocked
	m.mu.Unlock()

	for _, cb := range m.onDifficultyChange {
		cb(diff, locked)
	}
}

func (m *Module) handlePlayerAbilities(pkt *jp.WirePacket) {
	var d packets.S2CPlayerAbilities
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	m.mu.Lock()
	m.abilityFlags = int8(d.Flags)
	m.flyingSpeed = float32(d.FlyingSpeed)
	m.fovModifier = float32(d.FieldOfViewModifier)
	flags, flySpeed, fovMod := m.abilityFlags, m.flyingSpeed, m.fovModifier
	m.mu.Unlock()

	for _, cb := range m.onAbilitiesChange {
		cb(flags, flySpeed, fovMod)
	}
}

func (m *Module) handleSetDefaultSpawnPosition(pkt *jp.WirePacket) {
	var d packets.S2CSetDefaultSpawnPosition
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	m.mu.Lock()
	m.spawnDimension = string(d.DimensionName)
	m.spawnPosition = d.Location
	m.spawnYaw = float32(d.Yaw)
	m.spawnPitch = float32(d.Pitch)
	m.mu.Unlock()
}

func (m *Module) handleSetTime(pkt *jp.WirePacket) {
	var d packets.S2CSetTime
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	m.mu.Lock()
	m.worldAge = int64(d.WorldAge)
	// extract day time from the first clock update (overworld)
	if len(d.ClockUpdates) > 0 {
		m.timeOfDay = int64(d.ClockUpdates[0].TotalTicks)
		m.timeIncreasing = d.ClockUpdates[0].Rate > 0
	}
	age, tod := m.worldAge, m.timeOfDay
	m.mu.Unlock()

	for _, cb := range m.onTimeUpdate {
		cb(age, tod)
	}
}

func (m *Module) handleEntityEvent(pkt *jp.WirePacket) {
	if len(pkt.Data) < 5 {
		return
	}
	eid := int32(binary.BigEndian.Uint32(pkt.Data[0:4]))
	status := int8(pkt.Data[4])

	m.mu.Lock()
	if eid == m.entityID && status >= 24 && status <= 28 {
		m.opLevel = status - 24
	}
	m.mu.Unlock()
}
