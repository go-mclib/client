package physics

import (
	"math"
	"slices"
	"strings"

	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/entities"
	"github.com/go-mclib/data/blocks"
	dataentities "github.com/go-mclib/data/entities"
	"github.com/go-mclib/data/registries"
)

// applyEntityPushing models the OTHER entities' client ticks, not the local
// player's pushEntities: EntitySelector.pushableBy only selects the local player
// on clients. LivingEntity.doPush calls localPlayer.push(other) once per source.
// Ordinary server pushes set needsSync (tracking players only), whereas hurtMarked
// motion is sent to self too and replaces velocity in handleEntityMotion.
func (m *Module) applyEntityPushing(x, y, z, height float64) {
	if m.entities == nil || m.self == nil || m.self.IsDead() || m.self.Gamemode() == 3 ||
		m.self.IsVehicle() || m.onClimbableAt(x, y, z) {
		return
	}
	playerBox := collisions.EntityAABB(x, y, z, m.bodyWidth(), height)
	// Boats search a wider horizontal box. Living sources are filtered against
	// their unexpanded bounds below (LivingEntity.pushEntities has no inflation).
	search := playerBox.Inflate(float64(float32(0.2)), 0, float64(float32(0.2)))
	sleeping := m.self.Sleeping()
	for _, e := range m.entities.PhysicsEntitiesInAABB(search.MinX, search.MinY, search.MinZ, search.MaxX, search.MaxY, search.MaxZ) {
		// Living sources call localPlayer.push(source), whose override refuses
		// sleeping. Boats call boat.push(localPlayer), bypassing that override.
		if sleeping && !entities.IsBoat(e.TypeName) {
			continue
		}
		if !m.entities.CanPushLocal(e) || !entityPushSourceIntersects(e, playerBox) {
			continue
		}
		dx, dz := entityPushImpulse(e.X-x, e.Z-z)
		m.velX += dx
		m.velZ += dz
	}
}

func entityPushSourceIntersects(e *entities.Entity, playerBox collisions.AABB) bool {
	box := e.BoundingBox()
	if entities.IsBoat(e.TypeName) {
		// AbstractBoat.tick inflates (0.2F,-0.01F,0.2F), then push
		// only accepts non-boats whose feet are no higher than the boat's feet.
		return playerBox.MinY <= e.Y && box.Inflate(float64(float32(0.2)), -float64(float32(0.01)), float64(float32(0.2))).Intersects(playerBox)
	}
	switch e.TypeName {
	case "minecraft:armor_stand", "minecraft:bat", "minecraft:parrot":
		// Their pushEntities/doPush overrides never push a player.
		return false
	case "minecraft:ender_dragon", "minecraft:vex":
		// Dragon has noPhysics permanently; Vex sets it around super.tick.
		return false
	}
	// A source need not itself be pushable: e.g. Shulker.doPush still calls
	// localPlayer.push(shulker), even though Shulker.push(Entity) is empty.
	return dataentities.IsAttackable(e.TypeName) && box.Intersects(playerBox)
}

// entityPushImpulse returns the local half of Entity.push. Preserve Java's
// float constants widened to double and its sequential multiply operations.
func entityPushImpulse(dx, dz float64) (float64, float64) {
	distance := math.Max(math.Abs(dx), math.Abs(dz))
	if !(distance >= float64(float32(0.01))) {
		return 0, 0
	}
	distance = math.Sqrt(distance)
	dx /= distance
	dz /= distance
	power := math.Min(1, 1/distance)
	dx *= power
	dz *= power
	dx *= float64(float32(0.05))
	dz *= float64(float32(0.05))
	if math.IsInf(dx, 0) || math.IsNaN(dx) || math.IsInf(dz, 0) || math.IsNaN(dz) {
		return 0, 0
	}
	return -dx, -dz
}

// onClimbableAt follows LivingEntity.onClimbable, including open trapdoors
// continuing a ladder with the same facing. Tags use the generated vanilla set.
func (m *Module) onClimbableAt(x, y, z float64) bool {
	if m.world == nil || m.self != nil && m.self.Gamemode() == 3 {
		return false
	}
	bx, by, bz := int(math.Floor(x)), int(math.Floor(y)), int(math.Floor(z))
	id, props := blocks.StateProperties(int(m.world.GetBlock(bx, by, bz)))
	name := blocks.BlockName(id)
	if m.self != nil && m.self.FallFlying() && slices.Contains(registries.TagData["minecraft:block"]["minecraft:can_glide_through"], name) {
		return false
	}
	if slices.Contains(registries.TagData["minecraft:block"]["minecraft:climbable"], name) {
		return true
	}
	if !strings.HasSuffix(name, "_trapdoor") || props["open"] != "true" {
		return false
	}
	below, belowProps := blocks.StateProperties(int(m.world.GetBlock(bx, by-1, bz)))
	return blocks.BlockName(below) == "minecraft:ladder" && belowProps["facing"] == props["facing"]
}
