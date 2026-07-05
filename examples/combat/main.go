package main

import (
	"flag"

	"github.com/go-mclib/client/helpers"
	"github.com/go-mclib/client/modules/combat"
	"github.com/go-mclib/client/modules/entities"
	"github.com/go-mclib/client/modules/inventory"
	"github.com/go-mclib/client/modules/physics"
	"github.com/go-mclib/client/modules/self"
	dataEntities "github.com/go-mclib/data/entities"
)

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
	s := self.From(c)
	p := physics.From(c)

	filter := func(e *entities.Entity) bool {
		if !dataEntities.IsAttackable(e.TypeName) {
			return false
		}
		// without -rotate, only attack entities already under the crosshair
		return *rotate || com.IsInCrosshair(e.ID)
	}

	// retarget the nearest attackable entity each tick; the combat module
	// handles cooldown, reach and line-of-sight
	p.OnTick(func() {
		px, py, pz := s.Position()
		closest := ents.GetClosestEntity(px, py, pz, filter)
		if closest == nil {
			com.StopAttacking()
			return
		}
		com.StartAttacking(closest.ID)
	})

	com.OnAttack(func(entityID int32) {
		if e := ents.GetEntity(entityID); e != nil {
			c.Logger.Printf("attacked %s (#%d)", e.TypeName, e.ID)
		}
	})

	helpers.Run(c)
}
