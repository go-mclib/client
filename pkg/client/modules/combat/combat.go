package combat

import (
	"fmt"
	"math"

	"github.com/go-mclib/client/pkg/client"
	"github.com/go-mclib/client/pkg/client/modules/collisions"
	"github.com/go-mclib/client/pkg/client/modules/entities"
	"github.com/go-mclib/client/pkg/client/modules/physics"
	"github.com/go-mclib/client/pkg/client/modules/self"
	"github.com/go-mclib/data/pkg/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

const (
	ModuleName = "combat"

	EntityInteractionRange = 3.0
	DefaultAttackSpeed     = 4.0
	DefaultCooldownTicks   = 5 // 20 / 4.0
)

type Module struct {
	client *client.Client

	targetID             int32
	attacking            bool
	ticksSinceLastAttack int

	onAttack []func(entityID int32)
}

func New() *Module { return &Module{} }

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c

	// register tick callback for continuous attacking
	p := physics.From(c)
	if p != nil {
		p.OnTick(func() {
			m.ticksSinceLastAttack++
			if m.attacking {
				m.tryAttack()
			}
		})
	}
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

// events

func (m *Module) OnAttack(cb func(entityID int32)) {
	m.onAttack = append(m.onAttack, cb)
}

// Attack performs a single attack on the given entity.
func (m *Module) Attack(entityID int32) error {
	ents := entities.From(m.client)
	if ents == nil {
		return fmt.Errorf("entities module not registered")
	}
	e := ents.GetEntity(entityID)
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
	ents := entities.From(m.client)
	if ents == nil {
		return false
	}
	e := ents.GetEntity(entityID)
	if e == nil {
		return false
	}
	return m.isWithinReach(e)
}

// GetAttackCooldown returns the current attack cooldown progress (0.0 to 1.0).
func (m *Module) GetAttackCooldown() float32 {
	v := float32(m.ticksSinceLastAttack+1) / float32(DefaultCooldownTicks)
	if v > 1.0 {
		return 1.0
	}
	return v
}

func (m *Module) tryAttack() {
	if m.GetAttackCooldown() < 0.9 {
		return
	}
	ents := entities.From(m.client)
	if ents == nil {
		return
	}
	e := ents.GetEntity(m.targetID)
	if e == nil {
		m.StopAttacking()
		return
	}
	if !m.isWithinReach(e) || !ents.CanSee(m.targetID) {
		return
	}
	_ = m.performAttack(e)
}

func (m *Module) performAttack(e *entities.Entity) error {
	s := self.From(m.client)
	if s == nil {
		return fmt.Errorf("self module not registered")
	}

	_ = s.LookAt(e.X, e.Y+e.EyeHeight, e.Z)

	// send attack packet
	sneaking := false
	p := physics.From(m.client)
	if p != nil {
		sneaking = p.Sneaking
	}
	m.client.SendPacket(&packets.C2SInteract{
		EntityId:        ns.VarInt(e.ID),
		Type:            1, // attack
		SneakKeyPressed: ns.Boolean(sneaking),
	})

	// swing arm
	m.client.SendPacket(&packets.C2SSwing{Hand: 0})

	m.ticksSinceLastAttack = 0

	for _, cb := range m.onAttack {
		cb(e.ID)
	}

	return nil
}

// isWithinReach checks distance from player eye position to the closest point on entity AABB.
func (m *Module) isWithinReach(e *entities.Entity) bool {
	s := self.From(m.client)
	if s == nil {
		return false
	}

	eyeX := float64(s.X)
	eyeY := float64(s.Y) + self.EyeHeight
	eyeZ := float64(s.Z)

	aabb := collisions.EntityAABB(e.X, e.Y, e.Z, e.Width, e.Height)
	cx, cy, cz := aabb.ClosestPoint(eyeX, eyeY, eyeZ)

	dx := eyeX - cx
	dy := eyeY - cy
	dz := eyeZ - cz
	dist := math.Sqrt(dx*dx + dy*dy + dz*dz)

	return dist <= EntityInteractionRange
}
