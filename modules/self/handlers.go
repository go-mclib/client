package self

import (
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

func (m *Module) handleLogin(pkt *jp.WirePacket) {
	var d packets.S2CLogin
	if err := pkt.ReadInto(&d); err != nil {
		m.client.Logger.Println("failed to parse login play data:", err)
		return
	}

	m.mu.Lock()
	m.login = d
	autoRespawn := m.autoRespawn
	m.mu.Unlock()

	m.client.Logger.Println("spawned; ready")

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
	oldDim, oldGamemode := m.login.DimensionName, m.login.GameMode
	m.login.DimensionType = d.DimensionType
	m.login.DimensionName = d.DimensionName
	m.login.HashedSeed = d.HashedSeed
	m.login.GameMode = d.GameMode
	m.login.PreviousGameMode = d.PreviousGameMode
	m.login.IsDebug = d.IsDebug
	m.login.IsFlat = d.IsFlat
	m.login.DeathLocation = d.DeathLocation
	m.login.PortalCooldown = d.PortalCooldown
	m.login.SeaLevel = d.SeaLevel

	m.x, m.y, m.z = 0, 0, 0
	oldVehicle := m.vehicleID
	m.resetEntityStateLocked()

	if d.DataKept&0x01 == 0 {
		m.vitals = defaultVitals
	}
	m.mu.Unlock()

	m.effectsMu.Lock()
	clear(m.activeEffects)
	m.effectsMu.Unlock()

	m.client.Logger.Printf("respawned in %s", d.DimensionName)

	m.notifyVehicleChange(oldVehicle, 0)
	for _, cb := range m.onRespawn {
		cb()
	}
	if d.DimensionName != oldDim {
		for _, cb := range m.onDimensionChange {
			cb(string(d.DimensionName))
		}
	}
	if d.GameMode != oldGamemode {
		for _, cb := range m.onGamemodeChange {
			cb(uint8(d.GameMode))
		}
	}
}

func (m *Module) handleSetHealth(pkt *jp.WirePacket) {
	var d packets.S2CSetHealth
	if !m.client.Decode(pkt, &d) {
		return
	}

	m.mu.Lock()
	wasDead := m.vitals.Health <= 0
	m.vitals = Vitals{Health: float32(d.Health), Food: int32(d.Food), FoodSaturation: float32(d.FoodSaturation)}
	isDead := m.vitals.Health <= 0
	health, food := m.vitals.Health, float32(m.vitals.Food)
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
	if !m.client.Decode(pkt, &d) {
		return
	}

	m.mu.Lock()
	m.experience = Experience{Bar: float32(d.ExperienceBar), Level: int32(d.Level), Total: int32(d.TotalExperience)}
	bar, level, total := m.experience.Bar, m.experience.Level, m.experience.Total
	m.mu.Unlock()

	for _, cb := range m.onExperienceChange {
		cb(bar, level, total)
	}
}

func (m *Module) handlePlayerPosition(pkt *jp.WirePacket) {
	var d packets.S2CPlayerPosition
	if !m.client.Decode(pkt, &d) {
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
			TeleportID: d.TeleportID,
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
	if !m.client.Decode(pkt, &d) {
		return
	}

	event := uint8(d.Event)
	value := float32(d.Value)

	var gamemodeChanged bool
	var newMode uint8
	if d.Event == packets.GameEventChangeGameMode {
		newMode = uint8(d.Value)
		m.mu.Lock()
		if newMode != uint8(m.login.GameMode) {
			m.login.PreviousGameMode = m.login.GameMode
			m.login.GameMode = ns.VarInt(newMode)
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

	m.mu.Lock()
	isUs := int32(d.PlayerID) == int32(m.login.EntityID)
	alreadyDead := m.vitals.Health <= 0
	if isUs {
		// Vanilla follows the kill packet with S2CSetHealth(0); recording the
		// death here keeps that packet from firing onDeath a second time.
		m.vitals.Health = 0
	}
	autoRespawn := m.autoRespawn
	m.mu.Unlock()

	if !isUs {
		return
	}
	m.client.Logger.Printf("died: %s", d.Message)
	if !alreadyDead {
		for _, cb := range m.onDeath {
			cb()
		}
	}
	if autoRespawn {
		m.Respawn()
	}
}

func (m *Module) handleChangeDifficulty(pkt *jp.WirePacket) {
	var d packets.S2CChangeDifficulty
	if !m.client.Decode(pkt, &d) {
		return
	}

	m.mu.Lock()
	m.difficulty = Difficulty{Difficulty: uint8(d.Difficulty), Locked: bool(d.DifficultyLocked)}
	diff, locked := m.difficulty.Difficulty, m.difficulty.Locked
	m.mu.Unlock()

	for _, cb := range m.onDifficultyChange {
		cb(diff, locked)
	}
}

func (m *Module) handlePlayerAbilities(pkt *jp.WirePacket) {
	var d packets.S2CPlayerAbilities
	if !m.client.Decode(pkt, &d) {
		return
	}

	m.mu.Lock()
	m.abilities = Abilities{Flags: int8(d.Flags), FlyingSpeed: float32(d.FlyingSpeed), FOVModifier: float32(d.FieldOfViewModifier)}
	flags, flySpeed, fovMod := m.abilities.Flags, m.abilities.FlyingSpeed, m.abilities.FOVModifier
	m.mu.Unlock()

	for _, cb := range m.onAbilitiesChange {
		cb(flags, flySpeed, fovMod)
	}
}

func (m *Module) handleSetDefaultSpawnPosition(pkt *jp.WirePacket) {
	var d packets.S2CSetDefaultSpawnPosition
	if !m.client.Decode(pkt, &d) {
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
	if !m.client.Decode(pkt, &d) {
		return
	}

	m.mu.Lock()
	m.time.WorldAge = int64(d.WorldAge)
	// extract day time from the first clock update (overworld)
	if len(d.ClockUpdates) > 0 {
		m.time.TimeOfDay = int64(d.ClockUpdates[0].TotalTicks)
		m.time.TimeIncreasing = d.ClockUpdates[0].Rate > 0
	}
	age, tod := m.time.WorldAge, m.time.TimeOfDay
	m.mu.Unlock()

	for _, cb := range m.onTimeUpdate {
		cb(age, tod)
	}
}

func (m *Module) handleEntityEvent(pkt *jp.WirePacket) {
	var d packets.S2CEntityEvent
	if !m.client.Decode(pkt, &d) {
		return
	}
	eid, status := int32(d.EntityID), int8(d.EntityStatus)

	m.mu.Lock()
	if eid == int32(m.login.EntityID) && status >= 24 && status <= 28 {
		m.opLevel = status - 24
	}
	m.mu.Unlock()
}
