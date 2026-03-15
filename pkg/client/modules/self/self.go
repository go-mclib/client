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

	// AutoRespawn automatically respawns on death (default: true).
	AutoRespawn bool

	// login state (full S2CLogin fields)
	EntityID            ns.VarInt
	IsHardcore          bool
	DimensionNames      []string
	MaxPlayers          int32
	ViewDistance        int32
	SimulationDistance  int32
	ReducedDebugInfo    bool
	EnableRespawnScreen bool
	DoLimitedCrafting   bool
	DimensionType       int32
	DimensionName       string
	HashedSeed          int64
	Gamemode            ns.Uint8
	PreviousGameMode    int8
	IsDebug             bool
	IsFlat              bool
	DeathLocation       ns.PrefixedOptional[ns.GlobalPos]
	PortalCooldown      int32
	SeaLevel            int32
	EnforcesSecureChat  bool

	// health & experience
	Health          ns.Float32
	Food            ns.VarInt
	FoodSaturation  ns.Float32
	ExperienceBar   ns.Float32
	Level           ns.VarInt
	TotalExperience ns.VarInt

	// position & rotation
	X, Y, Z ns.Float64
	Yaw     ns.Float32
	Pitch   ns.Float32

	// difficulty
	Difficulty       uint8
	DifficultyLocked bool

	// abilities
	AbilityFlags int8
	FlyingSpeed  float32
	FOVModifier  float32

	// spawn position
	SpawnDimension string
	SpawnPosition  ns.Position
	SpawnYaw       float32
	SpawnPitch     float32

	// time
	WorldAge       int64
	TimeOfDay      int64
	TimeIncreasing bool

	// op level (0-4, derived from entity event status 24-28)
	OpLevel int8

	// SuppressPositionEcho prevents the module from echoing position
	// back to the server after receiving S2CPlayerPosition. Useful when
	// an external controller (e.g. pproxy) is handling movement.
	// The teleport confirm is still sent.
	SuppressPositionEcho bool

	// movement state flags (readable/settable by any module or user code)
	Sprinting bool
	Sneaking  bool

	effectsMu     sync.Mutex
	activeEffects map[int32]*EffectInstance

	onDeath     []func()
	onSpawn     []func()
	onHealthSet []func(health, food float32)
	onPosition  []func(x, y, z float64)
	onGameEvent []func(event uint8, value float32)
}

func New() *Module {
	return &Module{
		AutoRespawn:    true,
		Health:         20,
		Food:           20,
		FoodSaturation: 5,
		FlyingSpeed:    0.05,
		FOVModifier:    0.1,
		activeEffects:  make(map[int32]*EffectInstance),
	}
}

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c
	c.OnTransfer(m.Reset)
}

func (m *Module) Reset() {
	m.Health = 20
	m.Food = 20
	m.FoodSaturation = 5
	m.ExperienceBar = 0
	m.Level = 0
	m.TotalExperience = 0
	m.X = 0
	m.Y = 0
	m.Z = 0
	m.Yaw = 0
	m.Pitch = 0
	m.Sprinting = false
	m.Sneaking = false
	m.Difficulty = 0
	m.DifficultyLocked = false
	m.AbilityFlags = 0
	m.FlyingSpeed = 0.05
	m.FOVModifier = 0.1
	m.SpawnDimension = ""
	m.SpawnPosition = ns.Position{}
	m.SpawnYaw = 0
	m.SpawnPitch = 0
	m.WorldAge = 0
	m.TimeOfDay = 0
	m.TimeIncreasing = false
	m.OpLevel = 0
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

func (m *Module) IsDead() bool { return m.Health <= 0 }

// LoginPacket reconstructs the full S2CLogin packet from stored state.
func (m *Module) LoginPacket() *packets.S2CLogin {
	dims := make(ns.PrefixedArray[ns.Identifier], len(m.DimensionNames))
	for i, name := range m.DimensionNames {
		dims[i] = ns.Identifier(name)
	}

	return &packets.S2CLogin{
		EntityId:            ns.Int32(m.EntityID),
		IsHardcore:          ns.Boolean(m.IsHardcore),
		DimensionNames:      dims,
		MaxPlayers:          ns.VarInt(m.MaxPlayers),
		ViewDistance:        ns.VarInt(m.ViewDistance),
		SimulationDistance:  ns.VarInt(m.SimulationDistance),
		ReducedDebugInfo:    ns.Boolean(m.ReducedDebugInfo),
		EnableRespawnScreen: ns.Boolean(m.EnableRespawnScreen),
		DoLimitedCrafting:   ns.Boolean(m.DoLimitedCrafting),
		DimensionType:       ns.VarInt(m.DimensionType),
		DimensionName:       ns.Identifier(m.DimensionName),
		HashedSeed:          ns.Int64(m.HashedSeed),
		GameMode:            m.Gamemode,
		PreviousGameMode:    ns.Int8(m.PreviousGameMode),
		IsDebug:             ns.Boolean(m.IsDebug),
		IsFlat:              ns.Boolean(m.IsFlat),
		DeathLocation:       m.DeathLocation,
		PortalCooldown:      ns.VarInt(m.PortalCooldown),
		SeaLevel:            ns.VarInt(m.SeaLevel),
		EnforcesSecureChat:  ns.Boolean(m.EnforcesSecureChat),
	}
}

// events

func (m *Module) OnDeath(cb func()) { m.onDeath = append(m.onDeath, cb) }
func (m *Module) OnSpawn(cb func()) { m.onSpawn = append(m.onSpawn, cb) }
func (m *Module) OnHealthSet(cb func(health, food float32)) {
	m.onHealthSet = append(m.onHealthSet, cb)
}
func (m *Module) OnPosition(cb func(x, y, z float64)) { m.onPosition = append(m.onPosition, cb) }
func (m *Module) OnGameEvent(cb func(event uint8, value float32)) {
	m.onGameEvent = append(m.onGameEvent, cb)
}

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
	}
}

func (m *Module) handleLogin(pkt *jp.WirePacket) {
	var d packets.S2CLogin
	if err := pkt.ReadInto(&d); err != nil {
		m.client.Logger.Println("failed to parse login play data:", err)
		return
	}

	m.EntityID = ns.VarInt(d.EntityId)
	m.IsHardcore = bool(d.IsHardcore)
	m.DimensionNames = make([]string, len(d.DimensionNames))
	for i, name := range d.DimensionNames {
		m.DimensionNames[i] = string(name)
	}
	m.MaxPlayers = int32(d.MaxPlayers)
	m.ViewDistance = int32(d.ViewDistance)
	m.SimulationDistance = int32(d.SimulationDistance)
	m.ReducedDebugInfo = bool(d.ReducedDebugInfo)
	m.EnableRespawnScreen = bool(d.EnableRespawnScreen)
	m.DoLimitedCrafting = bool(d.DoLimitedCrafting)
	m.DimensionType = int32(d.DimensionType)
	m.DimensionName = string(d.DimensionName)
	m.HashedSeed = int64(d.HashedSeed)
	m.Gamemode = d.GameMode
	m.PreviousGameMode = int8(d.PreviousGameMode)
	m.IsDebug = bool(d.IsDebug)
	m.IsFlat = bool(d.IsFlat)
	m.DeathLocation = d.DeathLocation
	m.PortalCooldown = int32(d.PortalCooldown)
	m.SeaLevel = int32(d.SeaLevel)
	m.EnforcesSecureChat = bool(d.EnforcesSecureChat)

	m.client.Logger.Println("spawned; ready")

	if m.client.Interactive {
		m.client.EnableInput()
	}

	_ = m.client.WritePacket(&packets.C2SPlayerLoaded{})

	if m.AutoRespawn {
		m.Respawn()
	}

	for _, cb := range m.onSpawn {
		cb()
	}
}

func (m *Module) handleSetHealth(pkt *jp.WirePacket) {
	var d packets.S2CSetHealth
	if err := pkt.ReadInto(&d); err != nil {
		return
	}
	wasDead := m.IsDead()
	m.Health = d.Health
	m.Food = d.Food
	m.FoodSaturation = d.FoodSaturation

	for _, cb := range m.onHealthSet {
		cb(float32(d.Health), float32(d.Food))
	}

	if m.IsDead() && !wasDead {
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
	m.ExperienceBar = d.ExperienceBar
	m.Level = d.Level
	m.TotalExperience = d.TotalExperience
}

func (m *Module) handlePlayerPosition(pkt *jp.WirePacket) {
	var d packets.S2CPlayerPosition
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	flags := int32(d.Flags)

	// apply position (absolute or relative based on flags)
	if flags&0x01 != 0 {
		m.X += d.X
	} else {
		m.X = d.X
	}
	if flags&0x02 != 0 {
		m.Y += d.Y
	} else {
		m.Y = d.Y
	}
	if flags&0x04 != 0 {
		m.Z += d.Z
	} else {
		m.Z = d.Z
	}
	if flags&0x08 != 0 {
		m.Yaw += d.Yaw
	} else {
		m.Yaw = d.Yaw
	}
	if flags&0x10 != 0 {
		m.Pitch += d.Pitch
	} else {
		m.Pitch = d.Pitch
	}

	if !m.SuppressPositionEcho {
		// confirm teleport + echo position (as vanilla client does)
		_ = m.client.WritePacket(&packets.C2SAcceptTeleportation{
			TeleportId: d.TeleportId,
		})
		_ = m.client.WritePacket(&packets.C2SMovePlayerPosRot{
			X: m.X, FeetY: m.Y, Z: m.Z,
			Yaw: ns.Float32(m.Yaw), Pitch: ns.Float32(m.Pitch),
			Flags: 0,
		})
	}

	for _, cb := range m.onPosition {
		cb(float64(m.X), float64(m.Y), float64(m.Z))
	}
}

func (m *Module) handleGameEvent(pkt *jp.WirePacket) {
	var d packets.S2CGameEvent
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	for _, cb := range m.onGameEvent {
		cb(uint8(d.Event), float32(d.Value))
	}
}

func (m *Module) handleCombatKill(pkt *jp.WirePacket) {
	var d packets.S2CPlayerCombatKill
	if err := pkt.ReadInto(&d); err != nil {
		m.client.Logger.Printf("failed to parse player combat kill data: %s", err)
		return
	}
	if d.PlayerId == m.EntityID {
		m.client.Logger.Printf("died: %++v", d.Message)
		for _, cb := range m.onDeath {
			cb()
		}
		if m.AutoRespawn {
			m.Respawn()
		}
	}
}

func (m *Module) handleChangeDifficulty(pkt *jp.WirePacket) {
	var d packets.S2CChangeDifficulty
	if err := pkt.ReadInto(&d); err != nil {
		return
	}
	m.Difficulty = uint8(d.Difficulty)
	m.DifficultyLocked = bool(d.DifficultyLocked)
}

func (m *Module) handlePlayerAbilities(pkt *jp.WirePacket) {
	var d packets.S2CPlayerAbilities
	if err := pkt.ReadInto(&d); err != nil {
		return
	}
	m.AbilityFlags = int8(d.Flags)
	m.FlyingSpeed = float32(d.FlyingSpeed)
	m.FOVModifier = float32(d.FieldOfViewModifier)
}

func (m *Module) handleSetDefaultSpawnPosition(pkt *jp.WirePacket) {
	var d packets.S2CSetDefaultSpawnPosition
	if err := pkt.ReadInto(&d); err != nil {
		return
	}
	m.SpawnDimension = string(d.DimensionName)
	m.SpawnPosition = d.Location
	m.SpawnYaw = float32(d.Yaw)
	m.SpawnPitch = float32(d.Pitch)
}

func (m *Module) handleSetTime(pkt *jp.WirePacket) {
	var d packets.S2CSetTime
	if err := pkt.ReadInto(&d); err != nil {
		return
	}
	m.WorldAge = int64(d.WorldAge)
	m.TimeOfDay = int64(d.TimeOfDay)
	m.TimeIncreasing = bool(d.TimeOfDayIncreasing)
}

func (m *Module) handleEntityEvent(pkt *jp.WirePacket) {
	// entity event is a fixed-size packet: Int32 entity ID + Int8 status
	if len(pkt.Data) < 5 {
		return
	}
	eid := int32(binary.BigEndian.Uint32(pkt.Data[0:4]))
	status := int8(pkt.Data[4])

	// status 24-28 = op permission levels 0-4
	if eid == int32(m.EntityID) && status >= 24 && status <= 28 {
		m.OpLevel = status - 24
	}
}
