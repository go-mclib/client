package combat

import (
	"fmt"
	"math"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/entities"
	"github.com/go-mclib/client/modules/inventory"
	"github.com/go-mclib/client/modules/physics"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

const (
	ModuleName = "combat"

	EntityInteractionRange = 3.0

	// base attack_speed attribute value (fist); held-item modifiers adjust it
	baseAttackSpeed = 4.0
)

type Module struct {
	client *client.Client

	self      *self.Module
	entities  *entities.Module
	inventory *inventory.Module // optional: held-item attack speed

	targetID             int32
	attacking            bool
	ticksSinceLastAttack int

	onAttack []func(entityID int32)
}

func New() *Module { return &Module{} }

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c
	c.OnTransfer(m.Reset)
}

func (m *Module) Wire() error {
	m.self = self.From(m.client)
	m.entities = entities.From(m.client)
	if m.self == nil || m.entities == nil {
		return fmt.Errorf("requires self and entities modules")
	}
	m.inventory = inventory.From(m.client)

	// optional: physics enables continuous (per-tick) attacking
	if p := physics.From(m.client); p != nil {
		p.OnTick(func() {
			m.ticksSinceLastAttack++
			if m.attacking {
				m.tryAttack()
			}
		})
	}
	return nil
}

func (m *Module) HandlePacket(_ *jp.WirePacket) {}

func (m *Module) Reset() {
	m.targetID = 0
	m.attacking = false
	m.ticksSinceLastAttack = 0
}

func From(c *client.Client) *Module {
	mod := c.Module(ModuleName)
	if mod == nil {
		return nil
	}
	return mod.(*Module)
}

func (m *Module) OnAttack(cb func(entityID int32)) {
	m.onAttack = append(m.onAttack, cb)
}

// Attack performs a single attack on the given entity.
func (m *Module) Attack(entityID int32) error {
	e := m.entities.GetEntity(entityID)
	if e == nil {
		return fmt.Errorf("entity %d not found", entityID)
	}

	if !m.isWithinReach(e) {
		return fmt.Errorf("entity %d out of reach", entityID)
	}

	if m.GetAttackCooldown() < 0.9 {
		return fmt.Errorf("attack on cooldown")
	}

	return m.performAttack(e)
}

// StartAttacking begins continuous attacking on the given entity.
// Attacks are executed each physics tick when cooldown is ready.
func (m *Module) StartAttacking(entityID int32) {
	m.targetID = entityID
	m.attacking = true
}

// StopAttacking stops continuous attacking.
func (m *Module) StopAttacking() {
	m.attacking = false
	m.targetID = 0
}

// IsWithinReach returns true if the entity is within attack range.
func (m *Module) IsWithinReach(entityID int32) bool {
	e := m.entities.GetEntity(entityID)
	if e == nil {
		return false
	}
	return m.isWithinReach(e)
}

// IsInCrosshair reports whether the ray from the player's eyes along the
// current look direction hits the entity's AABB within attack range.
func (m *Module) IsInCrosshair(entityID int32) bool {
	e := m.entities.GetEntity(entityID)
	if e == nil {
		return false
	}

	ox, oy, oz := m.self.Position()
	oy += self.EyeHeight

	yaw, pitch := m.self.Rotation()
	yawRad := float64(yaw) * math.Pi / 180
	pitchRad := float64(pitch) * math.Pi / 180
	dx := -math.Sin(yawRad) * math.Cos(pitchRad)
	dy := -math.Sin(pitchRad)
	dz := math.Cos(yawRad) * math.Cos(pitchRad)

	box := collisions.EntityAABB(e.X, e.Y, e.Z, e.Width, e.Height)
	return rayIntersectsAABB(ox, oy, oz, dx, dy, dz, box, EntityInteractionRange)
}

// GetAttackCooldown returns the current attack cooldown progress (0.0 to 1.0).
func (m *Module) GetAttackCooldown() float32 {
	return min(float32(m.ticksSinceLastAttack+1)/m.cooldownTicks(), 1.0)
}

func (m *Module) tryAttack() {
	if m.GetAttackCooldown() < 0.9 {
		return
	}
	e := m.entities.GetEntity(m.targetID)
	if e == nil {
		m.StopAttacking()
		return
	}
	if !m.isWithinReach(e) || !m.entities.CanSee(m.targetID) {
		return
	}
	_ = m.performAttack(e)
}

func (m *Module) performAttack(e *entities.Entity) error {
	m.self.LookAt(e.X, e.Y+e.EyeHeight, e.Z)

	// send attack packet
	m.client.SendPacket(&packets.C2SAttack{
		EntityId: ns.VarInt(e.ID),
	})

	// swing arm
	m.client.SendPacket(&packets.C2SSwing{Hand: 0})

	m.ticksSinceLastAttack = 0

	for _, cb := range m.onAttack {
		cb(e.ID)
	}

	return nil
}

// cooldownTicks returns the full attack cooldown duration in ticks, derived
// from the held item's attack_speed modifier when inventory data is available.
func (m *Module) cooldownTicks() float32 {
	speed := float32(baseAttackSpeed)
	if m.inventory != nil {
		if held := m.inventory.HeldItem(); held != nil && !held.IsEmpty() && held.Components != nil {
			for _, mod := range held.Components.AttributeModifiers {
				if mod.Type == "minecraft:attack_speed" {
					if s := baseAttackSpeed + mod.Amount; s > 0 {
						speed = float32(s)
					}
				}
			}
		}
	}
	return 20.0 / speed
}

// isWithinReach checks distance from player eye position to the closest point on entity AABB.
func (m *Module) isWithinReach(e *entities.Entity) bool {
	sx, sy, sz := m.self.Position()
	eyeX := sx
	eyeY := sy + self.EyeHeight
	eyeZ := sz

	aabb := collisions.EntityAABB(e.X, e.Y, e.Z, e.Width, e.Height)
	cx, cy, cz := aabb.ClosestPoint(eyeX, eyeY, eyeZ)

	dx := eyeX - cx
	dy := eyeY - cy
	dz := eyeZ - cz
	dist := math.Sqrt(dx*dx + dy*dy + dz*dz)

	return dist <= EntityInteractionRange
}

// rayIntersectsAABB tests a ray against an AABB within maxDist (slab method).
func rayIntersectsAABB(ox, oy, oz, dx, dy, dz float64, box collisions.AABB, maxDist float64) bool {
	tMin, tMax := 0.0, maxDist
	for i, dv := range [3]float64{dx, dy, dz} {
		var o, lo, hi float64
		switch i {
		case 0:
			o, lo, hi = ox, box.MinX, box.MaxX
		case 1:
			o, lo, hi = oy, box.MinY, box.MaxY
		case 2:
			o, lo, hi = oz, box.MinZ, box.MaxZ
		}
		if math.Abs(dv) < 1e-9 {
			if o < lo || o > hi {
				return false
			}
			continue
		}
		t1 := (lo - o) / dv
		t2 := (hi - o) / dv
		if t1 > t2 {
			t1, t2 = t2, t1
		}
		tMin = max(tMin, t1)
		tMax = min(tMax, t2)
		if tMin > tMax {
			return false
		}
	}
	return true
}
