package client

import (
	"math"
	"math/rand"

	packets "github.com/go-mclib/data/go/772/java_packets"
	ns "github.com/go-mclib/protocol/net_structures"
)

func (c *Client) Move(x, y, z float64) error {
	move, err := packets.C2SMovePlayerPos.WithData(packets.C2SMovePlayerPosData{
		X:     ns.Double(x),
		FeetY: ns.Double(y),
		Z:     ns.Double(z),
		Flags: 0x01, // on ground
	})
	if err != nil {
		return err
	}

	return c.WritePacket(move)
}

func (c *Client) MoveRelative(deltaX, deltaY, deltaZ float64) error {
	return c.Move(float64(c.Self.X)+deltaX, float64(c.Self.Y)+deltaY, float64(c.Self.Z)+deltaZ)
}

func (c *Client) LookAt(x, y, z float64) error {
	look, err := packets.C2SMovePlayerRot.WithData(packets.C2SMovePlayerRotData{
		Yaw:   ns.Float(rand.Float64() * 360),
		Pitch: ns.Float(rand.Float64() * 360),
		Flags: 0x01, // on ground
	})
	if err != nil {
		return err
	}

	return c.WritePacket(look)
}

func (c *Client) UseAt(hand int8, yaw, pitch float64) error {
	use, err := packets.C2SUseItem.WithData(packets.C2SUseItemData{
		Hand:  ns.VarInt(hand),
		Yaw:   ns.Float(yaw),
		Pitch: ns.Float(pitch),
	})
	if err != nil {
		return err
	}

	return c.WritePacket(use)
}

func (c *Client) Respawn() error {
	respawn, err := packets.C2SClientCommand.WithData(packets.C2SClientCommandData{
		ActionId: 0, // perform respawn
	})
	if err != nil {
		return err
	}
	return c.WritePacket(respawn)
}

// WorldPosToYawPitch calculates the yaw and pitch to look at a given world position
func WorldPosToYawPitch(x, y, z, lookX, lookY, lookZ float64) (yaw, pitch float64) {
	dx := x - lookX
	dz := z - lookZ
	yaw = math.Atan2(dz, dx) * 180 / math.Pi
	pitch = -math.Atan2(y-lookY, math.Sqrt(dx*dx+dz*dz)) * 180 / math.Pi
	return
}
