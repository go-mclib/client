package collisions

import (
	"fmt"
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
	world  *world.Module
}

func New() *Module { return &Module{} }

func (m *Module) Name() string                  { return ModuleName }
func (m *Module) Init(c *client.Client)         { m.client = c }
func (m *Module) HandlePacket(_ *jp.WirePacket) {}
func (m *Module) Reset()                        {}

func (m *Module) Wire() error {
	if m.world = world.From(m.client); m.world == nil {
		return fmt.Errorf("requires world module")
	}
	return nil
}

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

	// step-up (vanilla Entity.collide): on a horizontal collision while on the
	// ground (or about to land), try stepping over the obstacle. Crucially the
	// step is measured from the LANDING box when descending, otherwise from the
	// ORIGINAL box — never from the post-Y position. Stepping from post-Y would
	// let an upward velocity (a jump or knockback) stack with the 0.6 step and
	// climb a full block in a single tick, which the server rejects as moving
	// into the wall (rubberband) and which clobbers normal wall-sliding. With
	// the original box, a single step is capped at maxUpStep, so a full block
	// must be cleared by the jump arc over several ticks.
	onGroundAfterCollision := verticalCollision && dy < 0
	if StepUpHeight > 0 && horizontalCollision && (onGroundAfterCollision || m.IsOnGround(x, y, z, width)) {
		originY := y
		if onGroundAfterCollision {
			originY = y + adjY // step from the landing position
		}
		stepResult := m.tryStepUp(x, originY, z, width, height, dx, dz, shapes)
		if stepResult != nil {
			stepHorizSq := stepResult[0]*stepResult[0] + stepResult[2]*stepResult[2]
			currHorizSq := adjX*adjX + adjZ*adjZ
			if stepHorizSq > currHorizSq {
				// distanceToGround = aabb.minY - groundedAABB.minY = y - originY
				adjX = stepResult[0]
				adjY = stepResult[1] + (originY - y)
				adjZ = stepResult[2]
				horizontalCollision = dx != adjX || dz != adjZ
			}
		}
	}

	return
}

// tryStepUp attempts to step up over an obstacle.
// Returns [dx, dy, dz] adjusted movement, or nil if no upward movement was possible.
// The caller compares horizontal distance to decide whether to use the step-up result.
func (m *Module) tryStepUp(x, y, z, width, height, dx, dz float64, existingShapes []AABB) []float64 {
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
	if stepDY < Epsilon {
		return nil // couldn't step up at all
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

	return []float64{stepDX, stepDY + downDY, stepDZ}
}

// getBlockCollisions returns all block collision AABBs within the given region.
func (m *Module) getBlockCollisions(region AABB) []AABB {
	w := m.world

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

// blockShapesWorld returns the collision AABBs of a single block in world coords.
func (m *Module) blockShapesWorld(bx, by, bz int) []AABB {
	stateID := m.world.GetBlock(bx, by, bz)
	if stateID == 0 {
		return nil
	}
	shapes := block_shapes.CollisionShape(stateID)
	out := make([]AABB, 0, len(shapes))
	for _, s := range shapes {
		out = append(out, AABB{
			MinX: s.MinX + float64(bx), MinY: s.MinY + float64(by), MinZ: s.MinZ + float64(bz),
			MaxX: s.MaxX + float64(bx), MaxY: s.MaxY + float64(by), MaxZ: s.MaxZ + float64(bz),
		})
	}
	return out
}

// SupportSurfaceY returns the height at which a player of the given width standing
// in the block column at (bx,by,bz) rests its feet: the highest collision-shape
// top that overlaps the player's footprint and lies in [by, by+1). A full block
// below yields by; a slab/stair/snow/path yields a fractional height; a tall
// shape in the block below (e.g. a fence top at by+0.5) is also a valid surface.
// ok is false when nothing supports the player at this level.
func (m *Module) SupportSurfaceY(bx, by, bz int, width float64) (surfaceY float64, ok bool) {
	hw := width / 2
	cx, cz := float64(bx)+0.5, float64(bz)+0.5
	fpMinX, fpMaxX := cx-hw, cx+hw
	fpMinZ, fpMaxZ := cz-hw, cz+hw
	lo, hi := float64(by), float64(by)+1.0

	best := math.Inf(-1)
	// scan the feet block and the block below (a tall shape there can reach up
	// into this node's [by, by+1) range)
	for _, dy := range [2]int{0, -1} {
		for _, s := range m.blockShapesWorld(bx, by+dy, bz) {
			if s.MaxY < lo-Epsilon || s.MaxY >= hi {
				continue // top must rest within this node's vertical range
			}
			if s.MaxX <= fpMinX || s.MinX >= fpMaxX || s.MaxZ <= fpMinZ || s.MinZ >= fpMaxZ {
				continue // shape doesn't overlap the player's footprint
			}
			if s.MaxY > best {
				best = s.MaxY
			}
		}
	}
	if math.IsInf(best, -1) {
		return 0, false
	}
	return best, true
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

// CollidesWith reports whether any block collision shape overlaps the given box.
func (m *Module) CollidesWith(box AABB) bool {
	return slices.ContainsFunc(m.getBlockCollisions(box), box.Intersects)
}
