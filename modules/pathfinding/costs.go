package pathfinding

import (
	"math"

	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/physics"
	"github.com/go-mclib/client/modules/self"
)

// tick-based cost constants, roughly matching real traversal time in ticks.
// Based on Baritone's ActionCosts with MC physics.
const (
	walkOneBlockCost    = 20.0 / 4.317 // ~4.63 ticks
	sprintOneBlockCost  = 20.0 / 5.612 // ~3.56 ticks
	sneakOneBlockCost   = 20.0 / 1.3   // ~15.38 ticks
	walkOffBlockCost    = walkOneBlockCost * 0.8
	centerAfterFallCost = walkOneBlockCost - walkOffBlockCost

	soulSandWalkCost = walkOneBlockCost * 2 // soul sand halves speed

	doorInteractCost = 10.0 // ticks to stop, open/close, resume

	// player dimensions live in the collisions and self modules
	playerWidth          = collisions.PlayerWidth
	playerHeight         = collisions.PlayerHeight
	playerSneakingHeight = collisions.PlayerSneakingHeight
	eyeHeight            = self.EyeHeight

	safeFallDistance = 3
)

// pre-computed costs
var fallNBlocksCost [257]float64

func init() {
	for i := range fallNBlocksCost {
		fallNBlocksCost[i] = distanceToTicks(float64(i))
	}
}

// distanceToTicks converts a fall distance to tick count using MC physics.
// Matches Baritone's ActionCosts.distanceToTicks.
func distanceToTicks(distance float64) float64 {
	if distance == 0 {
		return 0
	}
	remaining := distance
	tick := 0
	for {
		v := fallVelocity(tick)
		if remaining <= v {
			return float64(tick) + remaining/v
		}
		remaining -= v
		tick++
	}
}

// fallVelocity returns the distance fallen during a given tick.
// Matches Baritone's ActionCosts.velocity.
func fallVelocity(tick int) float64 {
	drag := physics.VerticalAirFriction
	return physics.Gravity * drag * (1 - math.Pow(drag, float64(tick))) / (1 - drag)
}

// fallCost returns the tick-based cost for falling n blocks.
func fallCost(n int) float64 {
	if n < 0 {
		n = -n
	}
	if n < len(fallNBlocksCost) {
		return fallNBlocksCost[n]
	}
	return distanceToTicks(float64(n))
}

// descendCost is the cost of walking off a block and falling 1 block.
func descendCost() float64 {
	return walkOffBlockCost + max(fallNBlocksCost[1], centerAfterFallCost)
}

// danger block names and their cost modifiers
var dangerCosts = map[string]float64{
	"minecraft:magma_block":      50,
	"minecraft:cactus":           50,
	"minecraft:lava":             100,
	"minecraft:sweet_berry_bush": 5,
	"minecraft:powder_snow":      20,
	"minecraft:soul_sand":        soulSandWalkCost - walkOneBlockCost,
	"minecraft:water":            2,
	"minecraft:campfire":         50,
	"minecraft:soul_campfire":    75,
	"minecraft:fire":             100,
	"minecraft:soul_fire":        100,
	"minecraft:wither_rose":      100,
	"minecraft:ice":              3,
	"minecraft:packed_ice":       3,
	"minecraft:blue_ice":         4,
}
