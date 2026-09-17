package self

import (
	"encoding/binary"

	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

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
	m.entityFlags, m.livingFlags = 0, 0
	m.noGravity, m.sleeping, m.isVehicle = false, false, false
	m.vehicleID = 0
	m.sprinting, m.sneaking = false, false

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
	oldYaw, oldPitch := m.yaw, m.pitch
	if m.vehicleID == 0 {
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
		m.pitch = max(float32(-90), min(float32(90), m.pitch))
	}

	suppress := m.suppressPositionEcho
	x, y, z := m.x, m.y, m.z
	yaw, pitch := m.yaw, m.pitch
	m.mu.Unlock()

	for _, cb := range m.onTeleport {
		cb(&d, oldYaw, oldPitch)
	}

	if !suppress {
		_ = m.client.WritePacket(&packets.C2SAcceptTeleportation{
			TeleportId: d.TeleportId,
			X:          ns.Float64(x), Y: ns.Float64(y), Z: ns.Float64(z),
			Yaw: ns.Float32(yaw), Pitch: ns.Float32(pitch),
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
