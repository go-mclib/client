package collisions

import (
	"strconv"

	"github.com/go-mclib/data/blocks"
)

type playerContext struct {
	descending       bool
	fallDistance     float64
	walkOnPowderSnow bool
}

// SetPlayerContext supplies EntityCollisionContext data for live player movement.
// The physics module sets it once per tick before moving; do not call it
// otherwise. Navigation must not change this state or simulate contextual
// snow/scaffolding without independently modeling the corresponding player state.
func (m *Module) SetPlayerContext(descending bool, fallDistance float64, walkOnPowderSnow bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.context = playerContext{descending, fallDistance, walkOnPowderSnow}
}

// Scaffolding's empty-context dump contains its stable shape. The runtime choice
// additionally depends on the original entity bottom, descending flag and state.
var scaffoldingBottom = func() map[int32]bool {
	result := make(map[int32]bool, 32)
	block := blocks.ID("minecraft:scaffolding")
	for distance := range 8 {
		for _, bottom := range []string{"false", "true"} {
			for _, waterlogged := range []string{"false", "true"} {
				state := blocks.StateID(block, map[string]string{"distance": strconv.Itoa(distance), "bottom": bottom, "waterlogged": waterlogged})
				if state >= 0 {
					result[state] = distance != 0 && bottom == "true"
				}
			}
		}
	}
	return result
}()

var powderSnowState = blocks.DefaultStateID(blocks.ID("minecraft:powder_snow"))
var scaffoldBottomShape = []blocks.AABB{{MinX: 0, MinY: 0, MinZ: 0, MaxX: 1, MaxY: 0.125, MaxZ: 1}}
var powderFallingShape = []blocks.AABB{{MinX: 0, MinY: 0, MinZ: 0, MaxX: 1, MaxY: float64(float32(0.9)), MaxZ: 1}}
var fullCollisionShape = []blocks.AABB{{MinX: 0, MinY: 0, MinZ: 0, MaxX: 1, MaxY: 1, MaxZ: 1}}

func (c playerContext) collisionShape(state int32, blockY int, entityBottom float64) []blocks.AABB {
	const aboveTolerance = float64(float32(1.0e-5))
	if bottom, scaffold := scaffoldingBottom[state]; scaffold {
		if entityBottom > float64(blockY)+1-aboveTolerance && !c.descending {
			return blocks.CollisionShape(state)
		}
		if bottom && entityBottom > float64(blockY)-aboveTolerance {
			return scaffoldBottomShape
		}
		return nil
	}
	if state == powderSnowState {
		if c.fallDistance > 2.5 {
			return powderFallingShape
		}
		if c.walkOnPowderSnow && entityBottom > float64(blockY)+1-aboveTolerance && !c.descending {
			return fullCollisionShape
		}
		return nil
	}
	return blocks.CollisionShape(state)
}
