package client

import (
	packets "github.com/go-mclib/data/go/772/java_packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/net_structures"
)

// SelfStore stores the current state of the bot
type SelfStore struct {
	// The entity ID of the bot.
	EntityID ns.VarInt

	// 0 or less = dead, 20 = full HP.
	Health ns.Float
	// 0 - 20
	Food ns.VarInt
	// Seems to vary from 0.0 to 5.0 in integer increments.
	FoodSaturation ns.Float

	// 0 - 1
	ExperienceBar ns.Float
	// Current experience level
	Level ns.VarInt
	// https://minecraft.wiki/w/Experience#Leveling_up
	TotalExperience ns.VarInt

	// The X coordinate of the bot.
	X ns.Double
	// The Y coordinate of the bot.
	Y ns.Double
	// The Z coordinate of the bot.
	Z ns.Double
}

func NewSelfStore() *SelfStore {
	return &SelfStore{
		// packets will overwrite, assume sane defaults at epoch
		Health:          ns.Float(20),
		Food:            ns.VarInt(20),
		FoodSaturation:  ns.Float(5),
		ExperienceBar:   ns.Float(0),
		Level:           ns.VarInt(0),
		TotalExperience: ns.VarInt(0),
		X:               ns.Double(0),
		Y:               ns.Double(0),
		Z:               ns.Double(0),
	}
}

func (s *SelfStore) IsDead() bool {
	return s.Health <= 0
}

func (s *SelfStore) HandlePacket(c *Client, pkt *jp.Packet) {
	switch pkt.PacketID {
	case packets.S2CSetHealth.PacketID:
		var d packets.S2CSetHealthData
		if err := jp.BytesToPacketData(pkt.Data, &d); err != nil {
			return
		}
		s.Health = d.Health
		s.Food = d.Food
		s.FoodSaturation = d.FoodSaturation
	case packets.S2CSetExperience.PacketID:
		var d packets.S2CSetExperienceData
		if err := jp.BytesToPacketData(pkt.Data, &d); err != nil {
			return
		}
		s.ExperienceBar = d.ExperienceBar
		s.Level = d.Level
		s.TotalExperience = d.TotalExperience
	case packets.S2CPlayerPosition.PacketID:
		var d packets.S2CPlayerPositionData
		if err := jp.BytesToPacketData(pkt.Data, &d); err != nil {
			return
		}
		s.X = d.X
		s.Y = d.Y
		s.Z = d.Z
	}
}
