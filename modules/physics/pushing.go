package physics

import (
	"math"

	"github.com/go-mclib/client/modules/collisions"
)

// nonPushableEntities lists entity types where isPushable() returns false in vanilla.
// Most non-LivingEntity types default to false; ArmorStand and Bat override to false.
var nonPushableEntities = map[string]bool{
	"minecraft:armor_stand": true, "minecraft:bat": true,
	"minecraft:item": true, "minecraft:experience_orb": true,
	"minecraft:arrow": true, "minecraft:spectral_arrow": true, "minecraft:trident": true,
	"minecraft:fireball": true, "minecraft:small_fireball": true, "minecraft:dragon_fireball": true,
	"minecraft:wither_skull": true, "minecraft:shulker_bullet": true, "minecraft:llama_spit": true,
	"minecraft:wind_charge": true, "minecraft:breeze_wind_charge": true,
	"minecraft:egg": true, "minecraft:ender_pearl": true, "minecraft:snowball": true, "minecraft:potion": true,
	"minecraft:fishing_bobber": true, "minecraft:eye_of_ender": true,
	"minecraft:tnt": true, "minecraft:falling_block": true, "minecraft:firework_rocket": true,
	"minecraft:item_frame": true, "minecraft:glow_item_frame": true, "minecraft:painting": true,
	"minecraft:leash_knot": true, "minecraft:marker": true, "minecraft:lightning_bolt": true,
	"minecraft:area_effect_cloud": true, "minecraft:evoker_fangs": true, "minecraft:end_crystal": true,
	"minecraft:interaction": true, "minecraft:text_display": true, "minecraft:block_display": true,
	"minecraft:item_display": true, "minecraft:ominous_item_spawner": true,
}

// applyEntityPushing applies pushing forces from nearby entities (Entity.push).
// Vanilla: LivingEntity.pushEntities → getPushableEntities (AABB intersection) → Entity.push
func (m *Module) applyEntityPushing(x, y, z, height float64) {
	if m.entities == nil {
		return
	}

	// find entities whose AABB intersects with the player's AABB
	hw := collisions.PlayerWidth / 2
	overlapping := m.entities.GetEntitiesInAABB(
		x-hw, y, z-hw,
		x+hw, y+height, z+hw,
	)
	for _, e := range overlapping {
		if nonPushableEntities[e.TypeName] {
			continue
		}
		dx := e.X - x
		dz := e.Z - z
		dist := math.Max(math.Abs(dx), math.Abs(dz))
		if dist < entityPushMinDist {
			continue
		}
		dist = math.Sqrt(dist)
		dx /= dist
		dz /= dist
		pow := math.Min(1.0, 1.0/dist)
		push := pow * entityPushStrength
		m.velX -= dx * push
		m.velZ -= dz * push
	}
}
