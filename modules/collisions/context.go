package collisions

import (
	"strconv"

	"github.com/go-mclib/data/block_shapes"
	"github.com/go-mclib/data/blocks"
	"github.com/go-mclib/data/hitboxes"
)

type playerContext struct {
	descending       bool
	fallDistance     float64
	walkOnPowderSnow bool
}

// SetPlayerContext supplies EntityCollisionContext data for live player movement.
// Navigation must not change this state or simulate contextual snow/scaffolding
// without independently modeling the corresponding player state.
func (m *Module) SetPlayerContext(descending bool, fallDistance float64, walkOnPowderSnow bool) {
	m.context = playerContext{descending, fallDistance, walkOnPowderSnow}
}

// Scaffolding's empty-context dump contains its stable shape. The runtime choice
// additionally depends on the original entity bottom, descending flag and state.
var scaffoldingBottom = func() map[int32]bool {
	result := make(map[int32]bool, 32)
	block := blocks.BlockID("minecraft:scaffolding")
	for distance := 0; distance <= 7; distance++ {
		for _, bottom := range []string{"false", "true"} {
			for _, waterlogged := range []string{"false", "true"} {
				state := blocks.StateID(int(block), map[string]string{"distance": strconv.Itoa(distance), "bottom": bottom, "waterlogged": waterlogged})
				if state >= 0 {
					result[state] = distance != 0 && bottom == "true"
				}
			}
		}
	}
	return result
}()

var powderSnowState = blocks.DefaultStateID(blocks.BlockID("minecraft:powder_snow"))
var scaffoldBottomShape = []hitboxes.AABB{{MinX: 0, MinY: 0, MinZ: 0, MaxX: 1, MaxY: 0.125, MaxZ: 1}}
var powderFallingShape = []hitboxes.AABB{{MinX: 0, MinY: 0, MinZ: 0, MaxX: 1, MaxY: float64(float32(0.9)), MaxZ: 1}}
var fullCollisionShape = []hitboxes.AABB{{MinX: 0, MinY: 0, MinZ: 0, MaxX: 1, MaxY: 1, MaxZ: 1}}

func (m *Module) collisionShape(state int32, blockY int, entityBottom float64) []hitboxes.AABB {
	const aboveTolerance = float64(float32(1.0e-5))
	if bottom, scaffold := scaffoldingBottom[state]; scaffold {
		if entityBottom > float64(blockY)+1-aboveTolerance && !m.context.descending {
			return block_shapes.CollisionShape(state)
		}
		if bottom && entityBottom > float64(blockY)-aboveTolerance {
			return scaffoldBottomShape
		}
		return nil
	}
	if state == powderSnowState {
		if m.context.fallDistance > 2.5 {
			return powderFallingShape
		}
		if m.context.walkOnPowderSnow && entityBottom > float64(blockY)+1-aboveTolerance && !m.context.descending {
			return fullCollisionShape
		}
		return nil
	}
	return block_shapes.CollisionShape(state)
}

func localBox(box hitboxes.AABB) AABB {
	return AABB{MinX: box.MinX, MinY: box.MinY, MinZ: box.MinZ, MaxX: box.MaxX, MaxY: box.MaxY, MaxZ: box.MaxZ}
}
