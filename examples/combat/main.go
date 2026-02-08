package main

import (
	"flag"
	"math"

	"github.com/go-mclib/client/pkg/client/modules/combat"
	"github.com/go-mclib/client/pkg/client/modules/entities"
	"github.com/go-mclib/client/pkg/client/modules/inventory"
	"github.com/go-mclib/client/pkg/client/modules/physics"
	"github.com/go-mclib/client/pkg/client/modules/self"
	"github.com/go-mclib/client/pkg/helpers"
	dataEntities "github.com/go-mclib/data/pkg/data/entities"
	"github.com/go-mclib/data/pkg/packets"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

const baseAttackSpeed = 4.0

func main() {
	var f helpers.Flags
	helpers.RegisterFlags(&f)
	rotate := flag.Bool("rotate", false, "rotate to face nearest entity before attacking")
	flag.Parse()

	c := helpers.NewClient(f)
	c.Register(entities.New())
	c.Register(combat.New())
	c.Register(inventory.New())

	ents := entities.From(c)
	com := combat.From(c)
	inv := inventory.From(c)
	s := self.From(c)
	p := physics.From(c)

	var ticksSinceAttack int

	p.OnTick(func() {
		ticksSinceAttack++

		cooldownTicks := attackCooldownTicks(inv)
		cooldown := float32(ticksSinceAttack) / float32(cooldownTicks)
		if cooldown < 0.9 {
			return
		}

		px, py, pz := float64(s.X), float64(s.Y), float64(s.Z)

		filter := func(e *entities.Entity) bool {
			if !dataEntities.IsAttackable(e.TypeName) {
				return false
			}
			if !*rotate {
				return crosshairHits(s, e)
			}
			return true
		}

		closest := ents.GetClosestEntity(px, py, pz, filter)
		if closest == nil || !com.IsWithinReach(closest.ID) {
			return
		}

		if *rotate {
			_ = s.LookAt(closest.X, closest.Y+closest.EyeHeight, closest.Z)
		}

		c.SendPacket(&packets.C2SInteract{
			EntityId:        ns.VarInt(closest.ID),
			Type:            1,
			SneakKeyPressed: ns.Boolean(p.Sneaking),
		})
		c.SendPacket(&packets.C2SSwing{Hand: 0})
		ticksSinceAttack = 0

		c.Logger.Printf("attacked %s (#%d)", closest.TypeName, closest.ID)
	})

	helpers.Run(c)
}

// attackCooldownTicks returns the number of ticks for a full attack cooldown
// based on the held item's attack speed attribute.
func attackCooldownTicks(inv *inventory.Module) float32 {
	held := inv.HeldItem()
	if !held.IsEmpty() && held.Components != nil {
		for _, mod := range held.Components.AttributeModifiers {
			if mod.Type == "minecraft:attack_speed" {
				speed := baseAttackSpeed + mod.Amount
				if speed > 0 {
					return 20.0 / float32(speed)
				}
			}
		}
	}
	return 20.0 / baseAttackSpeed // 5 ticks (fist/default)
}

// crosshairHits checks whether a ray from the bot's eye along its look direction
// intersects the entity's AABB within attack range (slab method).
func crosshairHits(s *self.Module, e *entities.Entity) bool {
	ox, oy, oz := float64(s.X), float64(s.Y)+self.EyeHeight, float64(s.Z)

	yawRad := float64(s.Yaw) * math.Pi / 180
	pitchRad := float64(s.Pitch) * math.Pi / 180
	dx := -math.Sin(yawRad) * math.Cos(pitchRad)
	dy := -math.Sin(pitchRad)
	dz := math.Cos(yawRad) * math.Cos(pitchRad)

	hw := e.Width / 2
	minX, minY, minZ := e.X-hw, e.Y, e.Z-hw
	maxX, maxY, maxZ := e.X+hw, e.Y+e.Height, e.Z+hw

	tMin, tMax := 0.0, combat.EntityInteractionRange

	for i, dv := range [3]float64{dx, dy, dz} {
		var o, lo, hi float64
		switch i {
		case 0:
			o, lo, hi = ox, minX, maxX
		case 1:
			o, lo, hi = oy, minY, maxY
		case 2:
			o, lo, hi = oz, minZ, maxZ
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
		if t1 > tMin {
			tMin = t1
		}
		if t2 < tMax {
			tMax = t2
		}
		if tMin > tMax {
			return false
		}
	}

	return true
}
