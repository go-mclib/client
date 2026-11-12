// Package interaction submits reach- and visibility-checked right clicks.
// It does not navigate, predict item consumption, or claim server-side success.
package interaction

import (
	"errors"
	"math"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/entities"
	"github.com/go-mclib/client/modules/physics"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/client/modules/world"
	"github.com/go-mclib/data/blocks"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

var (
	ErrOutOfReach = errors.New("target is outside interaction reach")
	ErrObstructed = errors.New("target has no unobstructed interaction point")
	ErrUnloaded   = errors.New("interaction terrain is not loaded")

	errInvalidRequest = errors.New("client, session transport and hand 0 or 1 are required")
)

// Submission means send accepted the interaction packet, not that the server
// performed the action. A block sequence acknowledgement also is not a success
// verdict. Observe world, inventory or container updates for actual effects.
type Submission struct {
	Sequence   int32
	BlockState int32
}

// UseBlock right-clicks a visible face with the selected hand (0 main, 1 off).
// send must use the caller's captured session; physics.QueuePacketContext is
// suitable inside a tick. The function never waits on network acknowledgements.
func UseBlock(c *client.Client, x, y, z, hand int, send func(jp.Packet) error) (Submission, error) {
	var result Submission
	me, w, col, err := modules(c, hand, send)
	if err != nil {
		return result, err
	}
	if x < -30000000 || x > 30000000 || z < -30000000 || z > 30000000 || y < -2048 || y > 2047 {
		return result, errors.New("block position is outside supported world coordinates")
	}
	state, loaded := w.GetLoadedBlock(x, y, z)
	if !loaded {
		return result, ErrUnloaded
	}
	name := blocks.Name(blocks.StateBlockID(state))
	if name == "minecraft:air" || name == "minecraft:cave_air" || name == "minecraft:void_air" {
		return result, errors.New("target block is air")
	}
	ex, ey, ez := me.EyePosition()
	reach := me.AttributeValue("minecraft:block_interaction_range", 4.5)
	hit, face, withinReach, ok := blockReachPoint(w, col, ex, ey, ez, x, y, z, state, reach)
	if !withinReach {
		return result, ErrOutOfReach
	}
	if !ok {
		return result, ErrObstructed
	}
	if err := aim(c, me, hit, send); err != nil {
		return result, err
	}
	result.Sequence, result.BlockState = c.NextBISequence(), state
	return result, send(&packets.C2SUseItemOn{
		Hand: ns.VarInt(hand), Location: ns.Position{X: x, Y: y, Z: z}, Face: ns.VarInt(face),
		CursorPositionX: ns.Float32(hit[0] - float64(x)), CursorPositionY: ns.Float32(hit[1] - float64(y)), CursorPositionZ: ns.Float32(hit[2] - float64(z)),
		Sequence: ns.VarInt(result.Sequence),
	})
}

// CanReachBlockFrom reports whether a player whose eyes are at (eyeX, eyeY,
// eyeZ) can right-click block (x, y, z) with UseBlock's rules: a face sample
// point of the block's collision shape (its cell when it has none) lies within
// reach, the terrain between eye and point is loaded, and no block collision
// shape obstructs the ray. w and col are required. Pass the
// minecraft:block_interaction_range attribute (default 4.5) as reach.
func CanReachBlockFrom(w *world.Module, col *collisions.Module, eyeX, eyeY, eyeZ float64, x, y, z int, reach float64) bool {
	state, loaded := w.GetLoadedBlock(x, y, z)
	if !loaded {
		return false
	}
	_, _, _, ok := blockReachPoint(w, col, eyeX, eyeY, eyeZ, x, y, z, state, reach)
	return ok
}

// blockReachPoint samples the six face centres of every collision box of the
// block and returns the closest reachable, loaded and unobstructed point.
// withinReach reports whether any sample point is within reach at all.
func blockReachPoint(w *world.Module, col *collisions.Module, ex, ey, ez float64, x, y, z int, state int32, reach float64) (hit [3]float64, face int, withinReach, ok bool) {
	best := math.Inf(1)
	consider := func(box collisions.AABB) {
		points := [6][3]float64{
			{(box.MinX + box.MaxX) / 2, box.MinY + 1e-5, (box.MinZ + box.MaxZ) / 2},
			{(box.MinX + box.MaxX) / 2, box.MaxY - 1e-5, (box.MinZ + box.MaxZ) / 2},
			{(box.MinX + box.MaxX) / 2, (box.MinY + box.MaxY) / 2, box.MinZ + 1e-5},
			{(box.MinX + box.MaxX) / 2, (box.MinY + box.MaxY) / 2, box.MaxZ - 1e-5},
			{box.MinX + 1e-5, (box.MinY + box.MaxY) / 2, (box.MinZ + box.MaxZ) / 2},
			{box.MaxX - 1e-5, (box.MinY + box.MaxY) / 2, (box.MinZ + box.MaxZ) / 2},
		}
		for f, p := range points {
			p[0] += float64(x)
			p[1] += float64(y)
			p[2] += float64(z)
			d := distanceSquared(ex, ey, ez, p[0], p[1], p[2])
			if d > reach*reach {
				continue
			}
			withinReach = true
			if d >= best || !loadedBetween(w, ex, ey, ez, p[0], p[1], p[2]) {
				continue
			}
			if blocked, _, _, _ := col.RaycastBlocks(ex, ey, ez, p[0], p[1], p[2]); blocked {
				continue
			}
			best, hit, face = d, p, f
		}
	}
	shapes := blocks.CollisionShape(state)
	if len(shapes) == 0 {
		// Non-colliding interactive blocks (buttons/levers) still accept a hit
		// inside their block cell. This is not a claim about their outline shape.
		consider(collisions.AABB{MaxX: 1, MaxY: 1, MaxZ: 1})
	} else {
		for _, box := range shapes {
			consider(collisions.AABB(box))
		}
	}
	return hit, face, withinReach, !math.IsInf(best, 1)
}

// UseEntity sends the 26.3 entity interaction record, including the hit point
// relative to the entity and actual secondary-action state. It is not an attack.
func UseEntity(c *client.Client, entityID int32, hand int, send func(jp.Packet) error) error {
	me, w, col, err := modules(c, hand, send)
	if err != nil {
		return err
	}
	ents := entities.From(c)
	if ents == nil {
		return errors.New("interaction requires entities module")
	}
	e := ents.GetEntity(entityID)
	if e == nil {
		return errors.New("entity is not tracked")
	}
	if !w.IsChunkLoaded(int32(math.Floor(e.X/16)), int32(math.Floor(e.Z/16))) {
		return ErrUnloaded
	}
	ex, ey, ez := me.EyePosition()
	box := e.BoundingBox()
	hx, hy, hz := box.ClosestPoint(ex, ey, ez)
	// Aim just inside the bounds rather than along a tangential boundary.
	hx = hx*.999 + (box.MinX+box.MaxX)*.0005
	hy = hy*.999 + (box.MinY+box.MaxY)*.0005
	hz = hz*.999 + (box.MinZ+box.MaxZ)*.0005
	reach := me.AttributeValue("minecraft:entity_interaction_range", 3)
	if distanceSquared(ex, ey, ez, hx, hy, hz) > reach*reach {
		return ErrOutOfReach
	}
	if !loadedBetween(w, ex, ey, ez, hx, hy, hz) {
		return ErrUnloaded
	}
	if blocked, _, _, _ := col.RaycastBlocks(ex, ey, ez, hx, hy, hz); blocked {
		return ErrObstructed
	}
	if err := aim(c, me, [3]float64{hx, hy, hz}, send); err != nil {
		return err
	}
	return send(&packets.C2SInteract{EntityID: ns.VarInt(entityID), Hand: ns.VarInt(hand), Location: ns.LpVec3{X: hx - e.X, Y: hy - e.Y, Z: hz - e.Z}, UsingSecondaryAction: ns.Boolean(me.Sneaking())})
}

func modules(c *client.Client, hand int, send func(jp.Packet) error) (*self.Module, *world.Module, *collisions.Module, error) {
	if c == nil || send == nil || hand < 0 || hand > 1 {
		return nil, nil, nil, errInvalidRequest
	}
	me, w, col := self.From(c), world.From(c), collisions.From(c)
	if me == nil || w == nil || col == nil || c.State() != jp.StatePlay || me.IsDead() {
		return nil, nil, nil, errors.New("interaction requires a living play session with self/world/collisions")
	}
	return me, w, col, nil
}

func aim(c *client.Client, me *self.Module, hit [3]float64, send func(jp.Packet) error) error {
	ex, ey, ez := me.EyePosition()
	yaw, pitch := self.WorldPosToYawPitch(ex, ey, ez, hit[0], hit[1], hit[2])
	me.SetRotation(yaw, pitch)
	var input ns.Uint8
	if me.Sneaking() {
		input = 32
	}
	if err := send(&packets.C2SPlayerInput{Flags: input}); err != nil {
		return err
	}
	var flags ns.Int8
	if p := physics.From(c); p != nil && p.IsOnGround() {
		flags = 1
	}
	return send(&packets.C2SMovePlayerRot{Yaw: ns.Float32(yaw), Pitch: ns.Float32(pitch), Flags: flags})
}

func loadedBetween(w *world.Module, x, y, z, tx, ty, tz float64) bool {
	for bx := int(math.Floor(math.Min(x, tx))); bx <= int(math.Floor(math.Max(x, tx))); bx++ {
		for bz := int(math.Floor(math.Min(z, tz))); bz <= int(math.Floor(math.Max(z, tz))); bz++ {
			for by := int(math.Floor(math.Min(y, ty))); by <= int(math.Floor(math.Max(y, ty))); by++ {
				if _, ok := w.GetLoadedBlock(bx, by, bz); !ok {
					return false
				}
			}
		}
	}
	return true
}

func distanceSquared(x, y, z, tx, ty, tz float64) float64 {
	dx, dy, dz := x-tx, y-ty, z-tz
	return dx*dx + dy*dy + dz*dz
}
