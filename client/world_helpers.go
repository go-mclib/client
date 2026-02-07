package client

import (
	"math"
	"math/rand"

	"github.com/go-mclib/data/pkg/packets"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

// Move moves the player to the given x, y, z position
func (c *Client) Move(x, y, z float64) error {
	move := &packets.C2SMovePlayerPos{
		X:     ns.Float64(x),
		FeetY: ns.Float64(y),
		Z:     ns.Float64(z),
		Flags: 0x01, // on ground
	}

	return c.WritePacket(move)
}

// MoveRelative moves the player relative to the current position
func (c *Client) MoveRelative(deltaX, deltaY, deltaZ float64) error {
	return c.Move(float64(c.Self.X)+deltaX, float64(c.Self.Y)+deltaY, float64(c.Self.Z)+deltaZ)
}

// LookAt looks at the given x, y, z position
func (c *Client) LookAt(x, y, z float64) error {
	look := &packets.C2SMovePlayerRot{
		Yaw:   ns.Float32(rand.Float64() * 360),
		Pitch: ns.Float32(rand.Float64() * 360),
		Flags: 0x01, // on ground
	}

	return c.WritePacket(look)
}

// SetRotation sets the absolute yaw and pitch of the player
func (c *Client) SetRotation(yaw, pitch float64) error {
	rotate := &packets.C2SMovePlayerRot{
		Yaw:   ns.Float32(yaw),
		Pitch: ns.Float32(pitch),
		Flags: 0x01, // on ground
	}

	c.Self.Yaw = ns.Float32(yaw)
	c.Self.Pitch = ns.Float32(pitch)

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
	use := &packets.C2SUseItem{
		Hand:     ns.VarInt(hand),
		Sequence: 0,
		Yaw:      ns.Float32(yaw),
		Pitch:    ns.Float32(pitch),
	}

	return c.WritePacket(use)
}

// Use uses the item in the specified hand at the current yaw and pitch
func (c *Client) Use(hand int8) error {
	return c.UseAt(hand, float64(c.Self.Yaw), float64(c.Self.Pitch))
}

// Drop drops the currently held item.
// If `dropStack` is true, the entire stack is dropped
func (c *Client) DropItem(dropStack bool) error {
	var status ns.VarInt
	if dropStack {
		status = ns.VarInt(3)
	} else {
		status = ns.VarInt(4)
	}

	drop := &packets.C2SPlayerAction{
		Status:   status,
		Location: ns.Position{X: 0, Y: 0, Z: 0},
		Face:     ns.Int8(0),
		Sequence: ns.VarInt(0),
	}

	return c.WritePacket(drop)
}

func (c *Client) Respawn() error {
	respawn := &packets.C2SClientCommand{
		ActionId: 0, // perform respawn
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

// Block face constants
const (
	FaceBottom = 0 // -Y
	FaceTop    = 1 // +Y
	FaceNorth  = 2 // -Z
	FaceSouth  = 3 // +Z
	FaceWest   = 4 // -X
	FaceEast   = 5 // +X
)

// Hand constants
const (
	HandMain = 0
	HandOff  = 1
)

// GetBlockAt returns the block state ID at the given world coordinates
func (c *Client) GetBlockAt(x, y, z int) int32 {
	return c.World.GetBlock(x, y, z)
}

// BreakBlock starts or finishes breaking a block at the given position.
// This sends the appropriate PlayerAction packets.
// For instant break (creative mode), call with start=true only.
// For survival mode, call with start=true, wait for the block to break, then call with start=false.
func (c *Client) BreakBlock(x, y, z int, face int8, start bool) error {
	var status ns.VarInt
	if start {
		status = 0 // Started digging
	} else {
		status = 2 // Finished digging
	}

	action := &packets.C2SPlayerAction{
		Status:   status,
		Location: ns.Position{X: x, Y: y, Z: z},
		Face:     ns.Int8(face),
		Sequence: 0,
	}

	return c.WritePacket(action)
}

// CancelBreakBlock cancels the current block breaking action
func (c *Client) CancelBreakBlock(x, y, z int, face int8) error {
	action := &packets.C2SPlayerAction{
		Status:   1, // Cancelled digging
		Location: ns.Position{X: x, Y: y, Z: z},
		Face:     ns.Int8(face),
		Sequence: 0,
	}

	return c.WritePacket(action)
}

// PlaceBlock places a block from the given hand at the specified position and face.
// cursorX, cursorY, cursorZ are the positions of the crosshair on the block (0.0 to 1.0).
func (c *Client) PlaceBlock(x, y, z int, face int8, hand int8, cursorX, cursorY, cursorZ float32) error {
	place := &packets.C2SUseItemOn{
		Hand:            ns.VarInt(hand),
		Location:        ns.Position{X: x, Y: y, Z: z},
		Face:            ns.VarInt(face),
		CursorPositionX: ns.Float32(cursorX),
		CursorPositionY: ns.Float32(cursorY),
		CursorPositionZ: ns.Float32(cursorZ),
		InsideBlock:     false,
		WorldBorderHit:  false,
		Sequence:        0,
	}

	return c.WritePacket(place)
}

// InteractBlock right-clicks on a block at the specified position and face.
// This is used for interacting with blocks like doors, buttons, levers, etc.
// cursorX, cursorY, cursorZ are the positions of the crosshair on the block (0.0 to 1.0).
func (c *Client) InteractBlock(x, y, z int, face int8, hand int8, cursorX, cursorY, cursorZ float32) error {
	// InteractBlock is the same as PlaceBlock at the protocol level
	return c.PlaceBlock(x, y, z, face, hand, cursorX, cursorY, cursorZ)
}

// SwingArm swings the player's arm (for animation)
func (c *Client) SwingArm(hand int8) error {
	swing := &packets.C2SSwing{
		Hand: ns.VarInt(hand),
	}

	return c.WritePacket(swing)
}
