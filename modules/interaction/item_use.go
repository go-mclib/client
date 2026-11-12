package interaction

import (
	"errors"
	"math"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/inventory"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// BeginItemUse submits one vanilla 26.3 use-item request with the current aim.
// It neither repeats right clicks nor predicts use, consumption or ammunition.
// hand is 0 (selected main hand) or 1 (offhand). send must be captured from the
// caller's session and preserve ordering with ReleaseItemUse. The returned
// sequence can be acknowledged even when the server refuses to use the item.
func BeginItemUse(c *client.Client, hand int, send func(jp.Packet) error) (Submission, error) {
	var result Submission
	if c == nil || send == nil || hand < 0 || hand > 1 {
		return result, errInvalidRequest
	}
	me, inv := self.From(c), inventory.From(c)
	if me == nil || inv == nil || c.State() != jp.StatePlay || me.IsDead() {
		return result, errors.New("item use requires a living play session with self/inventory")
	}
	item := inv.HeldItem()
	if hand == 1 {
		item = inv.Offhand()
	}
	if item.IsEmpty() {
		return result, errors.New("selected hand is empty")
	}
	yaw, pitch := me.Rotation()
	if math.IsNaN(float64(yaw)) || math.IsInf(float64(yaw), 0) || math.IsNaN(float64(pitch)) || math.IsInf(float64(pitch), 0) || pitch < -90 || pitch > 90 {
		return result, errors.New("item use requires finite yaw and pitch within -90..90")
	}
	result.Sequence = c.NextBISequence()
	return result, send(&packets.C2SUseItem{Hand: ns.VarInt(hand), Sequence: ns.VarInt(result.Sequence), Yaw: ns.Float32(yaw), Pitch: ns.Float32(pitch)})
}

// ReleaseItemUse submits vanilla RELEASE_USE_ITEM, BlockPos.ZERO,
// Direction.DOWN, sequence 0. Release has no hand or new prediction sequence.
// Use a captured, surviving session transport, not an already cancelled action
// context. No current-client lookup is made: cleanup must not target a reconnect.
func ReleaseItemUse(send func(jp.Packet) error) error {
	if send == nil {
		return errors.New("captured session transport is required")
	}
	return send(&packets.C2SPlayerAction{Status: packets.PlayerActionReleaseUseItem, Location: ns.Position{}, Face: 0, Sequence: 0})
}
