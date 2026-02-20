package collisions

import (
	"math"
	"slices"

	"github.com/go-mclib/client/pkg/client"
	"github.com/go-mclib/client/pkg/client/modules/world"
	block_shapes "github.com/go-mclib/data/pkg/data/hitboxes/blocks"
	jp "github.com/go-mclib/protocol/java_protocol"
)

const ModuleName = "collisions"

type Module struct {
	client *client.Client
}

func New() *Module { return &Module{} }

func (m *Module) Name() string                  { return ModuleName }
func (m *Module) Init(c *client.Client)         { m.client = c }
func (m *Module) HandlePacket(_ *jp.WirePacket) {}
func (m *Module) Reset()                        {}

func From(c *client.Client) *Module {
	mod := c.Module(ModuleName)
	if mod == nil {
		return nil
	}
	return mod.(*Module)
}

// CollideMovement resolves entity movement against world block collisions.
// Returns the adjusted movement vector and collision flags.
// Implements the same algorithm as Entity.collide() in the Minecraft source.
func (m *Module) CollideMovement(x, y, z, width, height float64, dx, dy, dz float64) (adjX, adjY, adjZ float64, horizontalCollision, verticalCollision bool) {
	entityBox := EntityAABB(x, y, z, width, height)

	// collect block collision shapes in the expanded region
	expanded := entityBox.ExpandTowards(dx, dy, dz).Inflate(Epsilon, Epsilon, Epsilon)
	shapes := m.getBlockCollisions(expanded)

	if len(shapes) == 0 {
		return dx, dy, dz, false, false
	}

	adjX, adjY, adjZ = dx, dy, dz

	// vanilla axis order (Direction.axisStepOrder): Y always first,
	// then resolve the dominant horizontal axis before the minor one.
	// if |dx| < |dz|: Y → Z → X; else Y → X → Z
	for _, s := range shapes {
		adjY = entityBox.clipYCollide(s, adjY)
	}
	entityBox = entityBox.Move(0, adjY, 0)

	if math.Abs(dx) < math.Abs(dz) {
		// Y → Z → X
		for _, s := range shapes {
			adjZ = entityBox.clipZCollide(s, adjZ)
		}
		entityBox = entityBox.Move(0, 0, adjZ)
		for _, s := range shapes {
			adjX = entityBox.clipXCollide(s, adjX)
		}
	} else {
		// Y → X → Z
		for _, s := range shapes {
			adjX = entityBox.clipXCollide(s, adjX)
		}
		entityBox = entityBox.Move(adjX, 0, 0)
		for _, s := range shapes {
			adjZ = entityBox.clipZCollide(s, adjZ)
		}
	}

	horizontalCollision = dx != adjX || dz != adjZ
	verticalCollision = dy != adjY

	// step-up: if horizontal collision while on ground (or about to land), try stepping up
	onGroundAfterCollision := verticalCollision && dy < 0
	if StepUpHeight > 0 && horizontalCollision && (onGroundAfterCollision || m.IsOnGround(x, y, z, width)) {
		stepResult := m.tryStepUp(x, y+float64(adjY), z, width, height, dx, dz, shapes)
		if stepResult != nil {
			adjX, adjY, adjZ = stepResult[0], float64(adjY)+stepResult[1], stepResult[2]
			horizontalCollision = dx != adjX || dz != adjZ
		}
	}

	return
}

// tryStepUp attempts to step up over an obstacle.
// Returns [dx, dy, dz] if stepping up allows more horizontal progress, nil otherwise.
func (m *Module) tryStepUp(x, y, z, width, height, dx, dz float64, existingShapes []AABB) []float64 {
	// try moving up by StepUpHeight, then forward, then back down
	stepBox := EntityAABB(x, y, z, width, height)
	expanded := stepBox.ExpandTowards(dx, StepUpHeight, dz).Inflate(Epsilon, Epsilon, Epsilon)
	shapes := m.getBlockCollisions(expanded)

	if len(shapes) == 0 {
		shapes = existingShapes
	}

	// move up
	stepDY := StepUpHeight
	for _, s := range shapes {
		stepDY = stepBox.clipYCollide(s, stepDY)
	}
	stepBox = stepBox.Move(0, stepDY, 0)

	// horizontal axes in vanilla axis order
	stepDX := dx
	stepDZ := dz
	if math.Abs(dx) < math.Abs(dz) {
		for _, s := range shapes {
			stepDZ = stepBox.clipZCollide(s, stepDZ)
		}
		stepBox = stepBox.Move(0, 0, stepDZ)
		for _, s := range shapes {
			stepDX = stepBox.clipXCollide(s, stepDX)
		}
		stepBox = stepBox.Move(stepDX, 0, 0)
	} else {
		for _, s := range shapes {
			stepDX = stepBox.clipXCollide(s, stepDX)
		}
		stepBox = stepBox.Move(stepDX, 0, 0)
		for _, s := range shapes {
			stepDZ = stepBox.clipZCollide(s, stepDZ)
		}
		stepBox = stepBox.Move(0, 0, stepDZ)
	}

	// move back down
	downDY := -stepDY
	for _, s := range shapes {
		downDY = stepBox.clipYCollide(s, downDY)
	}

	// check if stepping up gained more horizontal distance
	origHorizSq := dx*dx + dz*dz
	_ = origHorizSq
	stepHorizSq := stepDX*stepDX + stepDZ*stepDZ

	// only use step-up if it improved horizontal movement
	if stepHorizSq > Epsilon {
		return []float64{stepDX, stepDY + downDY, stepDZ}
	}
	return nil
}

// getBlockCollisions returns all block collision AABBs within the given region.
func (m *Module) getBlockCollisions(region AABB) []AABB {
	w := world.From(m.client)
	if w == nil {
		return nil
	}

	minBX := int(math.Floor(region.MinX))
	minBY := int(math.Floor(region.MinY))
	minBZ := int(math.Floor(region.MinZ))
	maxBX := int(math.Floor(region.MaxX))
	maxBY := int(math.Floor(region.MaxY))
	maxBZ := int(math.Floor(region.MaxZ))

	var result []AABB
	for bx := minBX; bx <= maxBX; bx++ {
		for by := minBY; by <= maxBY; by++ {
			for bz := minBZ; bz <= maxBZ; bz++ {
				stateID := w.GetBlock(bx, by, bz)
				if stateID == 0 {
					continue
				}
				shapes := block_shapes.CollisionShape(stateID)
				for _, s := range shapes {
					// offset from block-local coords to world coords
					result = append(result, AABB{
						MinX: s.MinX + float64(bx),
						MinY: s.MinY + float64(by),
						MinZ: s.MinZ + float64(bz),
						MaxX: s.MaxX + float64(bx),
						MaxY: s.MaxY + float64(by),
						MaxZ: s.MaxZ + float64(bz),
					})
				}
			}
		}
	}
	return result
}

// IsOnGround checks if an entity at the given position would be on the ground.
func (m *Module) IsOnGround(x, y, z, width float64) bool {
	hw := width / 2
	feetBox := AABB{
		MinX: x - hw, MinY: y - 0.001, MinZ: z - hw,
		MaxX: x + hw, MaxY: y, MaxZ: z + hw,
	}
	shapes := m.getBlockCollisions(feetBox)
	return slices.ContainsFunc(shapes, feetBox.Intersects)
}

// CanFitAt checks if an entity of the given size can exist at the position without colliding.
func (m *Module) CanFitAt(x, y, z, width, height float64) bool {
	entityBox := EntityAABB(x, y, z, width, height)
	shapes := m.getBlockCollisions(entityBox)
	return !slices.ContainsFunc(shapes, entityBox.Intersects)
}

// GetBlockCollisions returns all block collision AABBs within the given region (public).
func (m *Module) GetBlockCollisions(region AABB) []AABB {
	return m.getBlockCollisions(region)
}
