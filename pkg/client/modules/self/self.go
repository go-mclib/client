package self

import (
	"github.com/go-mclib/client/pkg/client"
	"github.com/go-mclib/data/pkg/data/packet_ids"
	"github.com/go-mclib/data/pkg/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

const ModuleName = "self"

type Module struct {
	client *client.Client

	// AutoRespawn automatically respawns on death (default: true).
	AutoRespawn bool

	EntityID        ns.VarInt
	Health          ns.Float32
	Food            ns.VarInt
	FoodSaturation  ns.Float32
	ExperienceBar   ns.Float32
	Level           ns.VarInt
	TotalExperience ns.VarInt
	X, Y, Z         ns.Float64
	Yaw             ns.Float32
	Pitch           ns.Float32
	DeathLocation   ns.PrefixedOptional[ns.GlobalPos]
	Gamemode        ns.Uint8

	onDeath     []func()
	onSpawn     []func()
	onHealthSet []func(health, food float32)
	onPosition  []func(x, y, z float64)
}

func New() *Module {
	return &Module{
		AutoRespawn:    true,
		Health:         20,
		Food:           20,
		FoodSaturation: 5,
	}
}

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) { m.client = c }

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

// events

func (m *Module) OnDeath(cb func()) { m.onDeath = append(m.onDeath, cb) }
func (m *Module) OnSpawn(cb func()) { m.onSpawn = append(m.onSpawn, cb) }
func (m *Module) OnHealthSet(cb func(health, food float32)) {
	m.onHealthSet = append(m.onHealthSet, cb)
}
func (m *Module) OnPosition(cb func(x, y, z float64)) { m.onPosition = append(m.onPosition, cb) }

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
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
	}
}

func (m *Module) handleLogin(pkt *jp.WirePacket) {
	var d packets.S2CLogin
	if err := pkt.ReadInto(&d); err != nil {
		m.client.Logger.Println("failed to parse login play data:", err)
		return
	}
	m.EntityID = ns.VarInt(d.EntityId)
	m.DeathLocation = d.DeathLocation
	m.Gamemode = d.GameMode
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
	m.X = d.X
	m.Y = d.Y
	m.Z = d.Z
	m.Yaw = d.Yaw
	m.Pitch = d.Pitch

	for _, cb := range m.onPosition {
		cb(float64(d.X), float64(d.Y), float64(d.Z))
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
