package client

import (
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

// SelfStore stores the current state of the bot
type SelfStore struct {
	// The entity ID of the bot.
	EntityID ns.VarInt

	// 0 or less = dead, 20 = full HP.
	Health ns.Float32
	// 0 - 20
	Food ns.VarInt
	// Seems to vary from 0.0 to 5.0 in integer increments.
	FoodSaturation ns.Float32

	// 0 - 1
	ExperienceBar ns.Float32
	// Current experience level
	Level ns.VarInt
	// https://minecraft.wiki/w/Experience#Leveling_up
	TotalExperience ns.VarInt

	// The X coordinate of the bot.
	X ns.Float64
	// The Y coordinate of the bot.
	Y ns.Float64
	// The Z coordinate of the bot.
	Z ns.Float64

	// The yaw rotation of the bot (0-360 degrees).
	Yaw ns.Float32
	// The pitch rotation of the bot (-90 to 90 degrees).
	Pitch ns.Float32

	// The location of the last death of the bot.
	DeathLocation ns.PrefixedOptional[ns.GlobalPos]
	// The gamemode of the bot.
	Gamemode ns.Uint8
}

func NewSelfStore() *SelfStore {
	return &SelfStore{
		// packets will overwrite, assume sane defaults at epoch
		Health:          ns.Float32(20),
		Food:            ns.VarInt(20),
		FoodSaturation:  ns.Float32(5),
		ExperienceBar:   ns.Float32(0),
		Level:           ns.VarInt(0),
		TotalExperience: ns.VarInt(0),
		X:               ns.Float64(0),
		Y:               ns.Float64(0),
		Z:               ns.Float64(0),
		Yaw:             ns.Float32(0),
		Pitch:           ns.Float32(0),
	}
}

func (s *SelfStore) IsDead() bool {
	return s.Health <= 0
}

func (s *SelfStore) HandlePacket(c *Client, pkt *jp.WirePacket) {
	switch pkt.PacketID {
	case packets.S2CSetHealthID:
		var d packets.S2CSetHealth
		if err := pkt.ReadInto(&d); err != nil {
			return
		}
		s.Health = d.Health
		s.Food = d.Food
		s.FoodSaturation = d.FoodSaturation
	case packets.S2CSetExperienceID:
		var d packets.S2CSetExperience
		if err := pkt.ReadInto(&d); err != nil {
			return
		}
		s.ExperienceBar = d.ExperienceBar
		s.Level = d.Level
		s.TotalExperience = d.TotalExperience
	case packets.S2CPlayerPositionID:
		var d packets.S2CPlayerPosition
		if err := pkt.ReadInto(&d); err != nil {
			return
		}
		s.X = d.X
		s.Y = d.Y
		s.Z = d.Z
		s.Yaw = d.Yaw
		s.Pitch = d.Pitch
	}
}
