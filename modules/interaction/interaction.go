// Package interaction submits reach- and visibility-checked right clicks.
// It does not navigate, predict item consumption, or claim server-side success.
package interaction

import (
	"errors"
	"fmt"
	"math"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/entities"
	"github.com/go-mclib/client/modules/physics"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/client/modules/world"
	"github.com/go-mclib/data/block_shapes"
	"github.com/go-mclib/data/blocks"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

var (
	ErrOutOfReach = errors.New("target is outside interaction reach")
	ErrObstructed = errors.New("target has no unobstructed interaction point")
	ErrUnloaded   = errors.New("interaction terrain is not loaded")
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
		return result, fmt.Errorf("block position is outside supported world coordinates")
	}
	state, loaded := w.GetLoadedBlock(x, y, z)
	if !loaded {
		return result, ErrUnloaded
	}
	name := blocks.BlockName(blocks.StateBlockID(state))
	if name == "minecraft:air" || name == "minecraft:cave_air" || name == "minecraft:void_air" {
		return result, fmt.Errorf("target block is air")
	}
	ex, ey, ez := eye(me)
	reach := me.AttributeValue("minecraft:block_interaction_range", 4.5)
	best := math.Inf(1)
	var hit [3]float64
	var face int
	withinReach := false
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
	shapes := block_shapes.CollisionShape(state)
	if len(shapes) == 0 {
		// Non-colliding interactive blocks (buttons/levers) still accept a hit
		// inside their block cell. This is not a claim about their outline shape.
		consider(collisions.AABB{MaxX: 1, MaxY: 1, MaxZ: 1})
	} else {
		for _, box := range shapes {
			consider(collisions.AABB{MinX: box.MinX, MinY: box.MinY, MinZ: box.MinZ, MaxX: box.MaxX, MaxY: box.MaxY, MaxZ: box.MaxZ})
		}
	}
	if !withinReach {
		return result, ErrOutOfReach
	}
	if math.IsInf(best, 1) {
		return result, ErrObstructed
	}
	if err := aim(c, me, hit, send); err != nil {
		return result, err
	}
	result.Sequence, result.BlockState = c.NextBISequence(), state
	err = send(&packets.C2SUseItemOn{
		Hand: ns.VarInt(hand), Location: ns.Position{X: x, Y: y, Z: z}, Face: ns.VarInt(face),
		CursorPositionX: ns.Float32(hit[0] - float64(x)), CursorPositionY: ns.Float32(hit[1] - float64(y)), CursorPositionZ: ns.Float32(hit[2] - float64(z)),
		Sequence: ns.VarInt(result.Sequence),
	})
	if err != nil {
		return result, err
	}
	return result, nil
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
		return fmt.Errorf("interaction requires entities module")
	}
	e := ents.GetEntity(entityID)
	if e == nil {
		return fmt.Errorf("entity is not tracked")
	}
	if !w.IsChunkLoaded(int32(math.Floor(e.X/16)), int32(math.Floor(e.Z/16))) {
		return ErrUnloaded
	}
	ex, ey, ez := eye(me)
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
	if err := send(&packets.C2SInteract{EntityId: ns.VarInt(entityID), Hand: ns.VarInt(hand), Location: ns.LpVec3{X: hx - e.X, Y: hy - e.Y, Z: hz - e.Z}, UsingSecondaryAction: ns.Boolean(me.Sneaking())}); err != nil {
		return err
	}
	return nil
}

func modules(c *client.Client, hand int, send func(jp.Packet) error) (*self.Module, *world.Module, *collisions.Module, error) {
	if c == nil || send == nil || hand < 0 || hand > 1 {
		return nil, nil, nil, fmt.Errorf("client, session transport and hand 0 or 1 are required")
	}
	me, w, col := self.From(c), world.From(c), collisions.From(c)
	if me == nil || w == nil || col == nil || c.State() != jp.StatePlay || me.IsDead() {
		return nil, nil, nil, fmt.Errorf("interaction requires a living play session with self/world/collisions")
	}
	return me, w, col, nil
}

func eye(me *self.Module) (x, y, z float64) {
	x, y, z = me.Position()
	height := self.EyeHeight
	if me.Sneaking() {
		height = 1.27
	}
	y += height * me.AttributeValue("minecraft:scale", 1)
	return
}

func aim(c *client.Client, me *self.Module, hit [3]float64, send func(jp.Packet) error) error {
	ex, ey, ez := eye(me)
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
