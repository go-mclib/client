package client

import (
	"math"
	"math/rand"

	packets "github.com/go-mclib/data/go/773/java_packets"
	ns "github.com/go-mclib/protocol/net_structures"
)

// Move moves the player to the given x, y, z position
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

// MoveRelative moves the player relative to the current position
func (c *Client) MoveRelative(deltaX, deltaY, deltaZ float64) error {
	return c.Move(float64(c.Self.X)+deltaX, float64(c.Self.Y)+deltaY, float64(c.Self.Z)+deltaZ)
}

// LookAt looks at the given x, y, z position
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

// SetRotation sets the absolute yaw and pitch of the player
func (c *Client) SetRotation(yaw, pitch float64) error {
	rotate, err := packets.C2SMovePlayerRot.WithData(packets.C2SMovePlayerRotData{
		Yaw:   ns.Float(yaw),
		Pitch: ns.Float(pitch),
		Flags: 0x01, // on ground
	})
	if err != nil {
		return err
	}

	c.Self.Yaw = ns.Float(yaw)
	c.Self.Pitch = ns.Float(pitch)

	return c.WritePacket(rotate)
}

// Rotate rotates the player by the given delta yaw and pitch
func (c *Client) Rotate(deltaYaw, deltaPitch float64) error {
	newYaw := float64(c.Self.Yaw) + deltaYaw
	newPitch := float64(c.Self.Pitch) + deltaPitch

	if newPitch > 90 {
		newPitch = 90
	} else if newPitch < -90 {
		newPitch = -90
	}

	for newYaw < 0 {
		newYaw += 360
	}
	for newYaw >= 360 {
		newYaw -= 360
	}

	return c.SetRotation(newYaw, newPitch)
}

// UseAt uses the item in the specified hand at the given yaw and pitch
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

// Use uses the item in the specified hand at the current yaw and pitch
func (c *Client) Use(hand int8) error {
	return c.UseAt(hand, float64(c.Self.Yaw), float64(c.Self.Pitch))
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
