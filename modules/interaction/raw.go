package interaction

import (
	"github.com/go-mclib/client"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// The helpers below write single player-action packets without reach,
// visibility or inventory checks. send must belong to the caller's session;
// pass c.WritePacket for an immediate write on the current connection.

// StartDestroyBlock starts mining a block. Creative mode breaks it instantly;
// survival mining must be finished with StopDestroyBlock after the dig time.
func StartDestroyBlock(c *client.Client, x, y, z int, face int8, send func(jp.Packet) error) error {
	return destroyAction(c, packets.PlayerActionStartDestroyBlock, x, y, z, face, send)
}

// StopDestroyBlock finishes mining a block started with StartDestroyBlock.
func StopDestroyBlock(c *client.Client, x, y, z int, face int8, send func(jp.Packet) error) error {
	return destroyAction(c, packets.PlayerActionStopDestroyBlock, x, y, z, face, send)
}

func destroyAction(c *client.Client, status ns.VarInt, x, y, z int, face int8, send func(jp.Packet) error) error {
	return send(&packets.C2SPlayerAction{
		Status:   status,
		Location: ns.Position{X: x, Y: y, Z: z},
		Face:     ns.Int8(face),
		Sequence: ns.VarInt(c.NextBISequence()),
	})
}

// UseItemOn right-clicks a block face with hand 0 (main) or 1 (offhand).
// cursorX/Y/Z is the hit point inside the block (0..1). Use UseBlock for a
// reach- and visibility-checked click.
func UseItemOn(c *client.Client, x, y, z int, face int8, hand int, cursorX, cursorY, cursorZ float32, send func(jp.Packet) error) error {
	return send(&packets.C2SUseItemOn{
		Hand:            ns.VarInt(hand),
		Location:        ns.Position{X: x, Y: y, Z: z},
		Face:            ns.VarInt(face),
		CursorPositionX: ns.Float32(cursorX),
		CursorPositionY: ns.Float32(cursorY),
		CursorPositionZ: ns.Float32(cursorZ),
		Sequence:        ns.VarInt(c.NextBISequence()),
	})
}

// DropHeldItem drops one item from the selected hotbar slot.
func DropHeldItem(send func(jp.Packet) error) error {
	return send(&packets.C2SPlayerAction{Status: packets.PlayerActionDropItem})
}

// DropHeldStack drops the whole stack in the selected hotbar slot.
func DropHeldStack(send func(jp.Packet) error) error {
	return send(&packets.C2SPlayerAction{Status: packets.PlayerActionDropAllItems})
}

// Punch performs a main-hand air attack and resets the attack cooldown. It is
// not the cosmetic swing that follows a successful attack or use.
func Punch(send func(jp.Packet) error) error {
	return send(&packets.C2SPunch{})
}
