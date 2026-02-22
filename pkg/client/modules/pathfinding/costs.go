package pathfinding

import "math"

// tick-based cost constants, roughly matching real traversal time in ticks.
// Based on Baritone's ActionCosts with MC physics.
const (
	WalkOneBlockCost    = 20.0 / 4.317 // ~4.63 ticks
	SprintOneBlockCost  = 20.0 / 5.612 // ~3.56 ticks
	SneakOneBlockCost   = 20.0 / 1.3   // ~15.38 ticks
	WalkOffBlockCost    = WalkOneBlockCost * 0.8
	CenterAfterFallCost = WalkOneBlockCost - WalkOffBlockCost

	SoulSandWalkCost = WalkOneBlockCost * 2 // soul sand halves speed

	DoorInteractCost = 10.0 // ticks to stop, open/close, resume

	CostInf = 1_000_000.0

	playerWidth          = 0.6
	playerHeight         = 1.8
	playerSneakingHeight = 1.5
	eyeHeight            = 1.62
	safeFallDistance     = 4
)

// pre-computed costs
var (
	FallNBlocksCost   [257]float64 // fall cost for 0..256 blocks
	JumpOneBlockCost  float64
	Fall125BlocksCost float64
	Fall025BlocksCost float64
)

func init() {
	for i := range FallNBlocksCost {
		FallNBlocksCost[i] = distanceToTicks(float64(i))
	}
	Fall125BlocksCost = distanceToTicks(1.25)
	Fall025BlocksCost = distanceToTicks(0.25)
	JumpOneBlockCost = Fall125BlocksCost - Fall025BlocksCost
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
	return (math.Pow(0.98, float64(tick)) - 1) * -3.92
}

// fallCost returns the tick-based cost for falling n blocks.
func fallCost(n int) float64 {
	if n < 0 {
		n = -n
	}
	if n < len(FallNBlocksCost) {
		return FallNBlocksCost[n]
	}
	return distanceToTicks(float64(n))
}

// descendCost is the cost of walking off a block and falling 1 block.
func descendCost() float64 {
	return WalkOffBlockCost + max(FallNBlocksCost[1], CenterAfterFallCost)
}

// danger block names and their cost modifiers
var dangerCosts = map[string]float64{
	"minecraft:magma_block":      50,
	"minecraft:cactus":           50,
	"minecraft:lava":             100,
	"minecraft:sweet_berry_bush": 5,
	"minecraft:powder_snow":      20,
	"minecraft:soul_sand":        SoulSandWalkCost - WalkOneBlockCost,
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
