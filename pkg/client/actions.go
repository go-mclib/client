package client

import (
	"github.com/go-mclib/data/pkg/packets"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

// BreakBlock starts or finishes breaking a block at the given position.
// For instant break (creative mode), call with start=true only.
// For survival mode, call with start=true, wait, then call with start=false.
func (c *Client) BreakBlock(x, y, z int, face int8, start bool) error {
	var status ns.VarInt
	if start {
		status = 0 // started digging
	} else {
		status = 2 // finished digging
	}
	return c.WritePacket(&packets.C2SPlayerAction{
		Status:   status,
		Location: ns.Position{X: x, Y: y, Z: z},
		Face:     ns.Int8(face),
		Sequence: 0,
	})
}

// CancelBreakBlock cancels the current block breaking action.
func (c *Client) CancelBreakBlock(x, y, z int, face int8) error {
	return c.WritePacket(&packets.C2SPlayerAction{
		Status:   1, // cancelled digging
		Location: ns.Position{X: x, Y: y, Z: z},
		Face:     ns.Int8(face),
		Sequence: 0,
	})
}

// PlaceBlock places a block from the given hand at the specified position and face.
// cursorX, cursorY, cursorZ are positions of the crosshair on the block (0.0 to 1.0).
func (c *Client) PlaceBlock(x, y, z int, face int8, hand int8, cursorX, cursorY, cursorZ float32) error {
	return c.WritePacket(&packets.C2SUseItemOn{
		Hand:            ns.VarInt(hand),
		Location:        ns.Position{X: x, Y: y, Z: z},
		Face:            ns.VarInt(face),
		CursorPositionX: ns.Float32(cursorX),
		CursorPositionY: ns.Float32(cursorY),
		CursorPositionZ: ns.Float32(cursorZ),
		InsideBlock:     false,
		WorldBorderHit:  false,
		Sequence:        0,
	})
}

// InteractBlock right-clicks on a block (doors, buttons, levers, etc.).
func (c *Client) InteractBlock(x, y, z int, face int8, hand int8, cursorX, cursorY, cursorZ float32) error {
	return c.PlaceBlock(x, y, z, face, hand, cursorX, cursorY, cursorZ)
}

// SwingArm swings the player's arm (animation).
func (c *Client) SwingArm(hand int8) error {
	return c.WritePacket(&packets.C2SSwing{Hand: ns.VarInt(hand)})
}

// DropItem drops the currently held item. If dropStack is true, the entire stack is dropped.
func (c *Client) DropItem(dropStack bool) error {
	var status ns.VarInt
	if dropStack {
		status = 3
	} else {
		status = 4
	}
	return c.WritePacket(&packets.C2SPlayerAction{
		Status:   status,
		Location: ns.Position{X: 0, Y: 0, Z: 0},
		Face:     0,
		Sequence: 0,
	})
}
