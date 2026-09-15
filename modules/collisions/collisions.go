package collisions

import (
	"fmt"
	"math"
	"slices"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/world"
	"github.com/go-mclib/data/block_shapes"
	"github.com/go-mclib/data/blocks"
)

const ModuleName = "collisions"

type blockGetter interface {
	GetLoadedBlock(x, y, z int) (int32, bool)
}

type entityCollisions interface {
	CollisionBoxes(minX, minY, minZ, maxX, maxY, maxZ float64) []AABB
}

type Module struct {
	client   *client.Client
	world    blockGetter
	entities entityCollisions
	context  playerContext
	border   borderState
}

func New() *Module                      { return &Module{border: defaultBorder()} }
func (m *Module) Name() string          { return ModuleName }
func (m *Module) Init(c *client.Client) { m.client = c }
func (m *Module) Reset() {
	m.context = playerContext{}
	m.border = defaultBorder()
}

func (m *Module) Wire() error {
	w := world.From(m.client)
	if w == nil {
		return fmt.Errorf("requires world module")
	}
	m.world = w
	m.entities, _ = m.client.Module("entities").(entityCollisions)
	return nil
}

func From(c *client.Client) *Module {
	mod := c.Module(ModuleName)
	if mod == nil {
		return nil
	}
	return mod.(*Module)
}

// CollideMovement resolves Entity.collide movement. onGround is the previous
// movement's ground state, not a proximity probe; maxStep is the step attribute.
// The returned collision flags compare the requested and final movement.
func (m *Module) CollideMovement(x, y, z, width, height, dx, dy, dz float64, onGround bool, maxStep float64) (adjX, adjY, adjZ float64, horizontalCollision, verticalCollision bool) {
	box := EntityAABB(x, y, z, width, height)
	region := box.ExpandTowards(dx, dy, dz)
	var entities []AABB
	if m.entities != nil {
		query := region.Inflate(Epsilon, Epsilon, Epsilon)
		entities = m.entities.CollisionBoxes(query.MinX, query.MinY, query.MinZ, query.MaxX, query.MaxY, query.MaxZ)
	}
	adjX, adjY, adjZ = dx, dy, dz
	if dx*dx+dy*dy+dz*dz != 0 {
		shapes := m.collectColliders(region, x, y, z, entities)
		adjX, adjY, adjZ = collideWithShapes(box, shapes, dx, dy, dz)
	}
	landed := dy != adjY && dy < 0
	maxStep = float64(float32(maxStep))
	if maxStep > 0 && (landed || onGround) && (dx != adjX || dz != adjZ) {
		grounded := box
		if landed {
			grounded = grounded.Move(0, adjY, 0)
		}
		stepRegion := grounded.ExpandTowards(dx, maxStep, dz)
		if !landed {
			stepRegion = stepRegion.ExpandTowards(0, -float64(float32(1.0e-5)), 0)
		}
		shapes := m.collectColliders(stepRegion, x, y, z, entities)
		for _, candidate := range collectCandidateStepUpHeights(grounded, shapes, float32(maxStep), float32(adjY)) {
			sx, sy, sz := collideWithShapes(grounded, shapes, dx, float64(candidate), dz)
			if sx*sx+sz*sz > adjX*adjX+adjZ*adjZ {
				adjX, adjY, adjZ = sx, sy-(box.MinY-grounded.MinY), sz
				break
			}
		}
	}
	return adjX, adjY, adjZ, dx != adjX || dz != adjZ, dy != adjY
}

// collectCandidateStepUpHeights uses actual voxel Y planes, float-rounded before
// comparison, and ascending order. A higher speculative step is never attempted
// once the first candidate improves horizontal travel.
func collectCandidateStepUpHeights(box AABB, shapes []voxelShape, maxStep, skip float32) []float32 {
	candidates := make([]float32, 0, 4)
	for _, shape := range shapes {
		coords := shape.coords[1]
		offset := shape.y
		if len(coords) == 0 {
			coords = []float64{shape.box.MinY, shape.box.MaxY}
			offset = 0
		}
		for _, coord := range coords {
			h := float32(coord + offset - box.MinY)
			if h >= 0 && h <= maxStep && h != skip && !slices.Contains(candidates, h) {
				candidates = append(candidates, h)
			}
		}
	}
	slices.Sort(candidates)
	return candidates
}

func collideWithShapes(box AABB, shapes []voxelShape, dx, dy, dz float64) (float64, float64, float64) {
	if len(shapes) == 0 {
		return dx, dy, dz
	}
	if dy != 0 {
		for _, shape := range shapes {
			dy = shape.clip(box, 1, dy)
		}
		box = box.Move(0, dy, 0)
	}
	if math.Abs(dx) < math.Abs(dz) {
		if dz != 0 {
			for _, shape := range shapes {
				dz = shape.clip(box, 2, dz)
			}
			box = box.Move(0, 0, dz)
		}
		if dx != 0 {
			for _, shape := range shapes {
				dx = shape.clip(box, 0, dx)
			}
		}
	} else {
		if dx != 0 {
			for _, shape := range shapes {
				dx = shape.clip(box, 0, dx)
			}
			box = box.Move(dx, 0, 0)
		}
		if dz != 0 {
			for _, shape := range shapes {
				dz = shape.clip(box, 2, dz)
			}
		}
	}
	return dx, dy, dz
}

func (m *Module) collectColliders(region AABB, x, bottom, z float64, entities []AABB) []voxelShape {
	shapes := make([]voxelShape, 0, len(entities)+1)
	for _, box := range entities {
		shapes = append(shapes, voxelShape{box: box})
	}
	shapes = m.appendBorder(shapes, region, x, z)
	return m.blockCollisions(region, bottom, false, shapes)
}

// blockCollisions follows BlockCollisions/Cursor3D: X fastest, then Y, then Z.
// The one-cell halo includes tall fences/walls/gates below the entity's feet.
// Face cells require large shapes, edge cells moving pistons, corners are skipped.
func (m *Module) blockCollisions(region AABB, bottom float64, suffocatingOnly bool, result []voxelShape) []voxelShape {
	if m.world == nil {
		return result
	}
	minX, maxX := int(math.Floor(region.MinX-Epsilon))-1, int(math.Floor(region.MaxX+Epsilon))+1
	minY, maxY := int(math.Floor(region.MinY-Epsilon))-1, int(math.Floor(region.MaxY+Epsilon))+1
	minZ, maxZ := int(math.Floor(region.MinZ-Epsilon))-1, int(math.Floor(region.MaxZ+Epsilon))+1
	for z := minZ; z <= maxZ; z++ {
		for y := minY; y <= maxY; y++ {
			for x := minX; x <= maxX; x++ {
				boundary := 0
				if x == minX || x == maxX {
					boundary++
				}
				if y == minY || y == maxY {
					boundary++
				}
				if z == minZ || z == maxZ {
					boundary++
				}
				if boundary == 3 {
					continue
				}
				state, known := m.world.GetLoadedBlock(x, y, z)
				if !known || state == 0 || suffocatingOnly && !block_shapes.IsSuffocating(state) {
					continue
				}
				if boundary == 1 && !block_shapes.HasLargeCollisionShape(state) || boundary == 2 && blocks.BlockName(blocks.StateBlockID(state)) != "minecraft:moving_piston" {
					continue
				}
				shape, collidable := m.collisionVoxel(state, x, y, z, bottom)
				if !collidable {
					continue
				}
				// Yield the entire voxel shape, including internal planes and
				// parts outside the query, when any occupied part intersects.
				for i := range shape.size() {
					part := shape.part(i)
					intersects := region.Intersects(part)
					if !block_shapes.IsFullBlock(state) {
						intersects = region.intersectsVoxel(part)
					}
					if intersects {
						result = append(result, shape)
						break
					}
				}
			}
		}
	}
	return result
}

// SupportSurfaceY finds the highest collision top in this node's [by,by+1)
// range, including shapes protruding from adjacent columns and the block below.
func (m *Module) SupportSurfaceY(bx, by, bz int, width float64) (surfaceY float64, ok bool) {
	footprint := EntityAABB(float64(bx)+0.5, float64(by), float64(bz)+0.5, width, 1)
	if !m.loaded(footprint.Inflate(1, 0, 1).ExpandTowards(0, -1, 0)) {
		return 0, false
	}
	best := math.Inf(-1)
	for _, collider := range m.blockCollisions(footprint.Inflate(0, 1.0e-6, 0), footprint.MinY, false, nil) {
		for i := range collider.size() {
			shape := collider.part(i)
			if shape.MaxY < float64(by)-Epsilon || shape.MaxY >= float64(by)+1 {
				continue
			}
			if shape.MaxX <= footprint.MinX+Epsilon || shape.MinX > footprint.MaxX-Epsilon || shape.MaxZ <= footprint.MinZ+Epsilon || shape.MinZ > footprint.MaxZ-Epsilon {
				continue
			}
			best = math.Max(best, shape.MaxY)
		}
	}
	if math.IsInf(best, -1) {
		return 0, false
	}
	return best, true
}

// IsOnGround probes the vanilla supporting-block slice; it is not a replacement
// for the previous tick's onGround flag when deciding whether stepping is allowed.
func (m *Module) IsOnGround(x, y, z, width float64) bool {
	feet := EntityAABB(x, y-1.0e-6, z, width, 1.0e-6)
	return len(m.blockCollisions(feet, y, false, nil)) != 0
}

func (m *Module) loaded(box AABB) bool {
	if m.world == nil {
		return false
	}
	for z := int(math.Floor(box.MinZ)); z <= int(math.Floor(box.MaxZ-Epsilon)); z++ {
		for y := int(math.Floor(box.MinY)); y <= int(math.Floor(box.MaxY-Epsilon)); y++ {
			for x := int(math.Floor(box.MinX)); x <= int(math.Floor(box.MaxX-Epsilon)); x++ {
				if _, known := m.world.GetLoadedBlock(x, y, z); !known {
					return false
				}
			}
		}
	}
	return true
}

// CanFitAt requires known terrain and the vanilla 1e-7 pose-fit deflation.
func (m *Module) CanFitAt(x, y, z, width, height float64) bool {
	box := EntityAABB(x, y, z, width, height).Inflate(-Epsilon, -Epsilon, -Epsilon)
	return m.loaded(box.Inflate(1, 0, 1).ExpandTowards(0, -1, 0)) && m.NoCollision(box)
}

// NoCollision checks block, rigid-entity and world-border overlap without
// changing the supplied AABB. Callers are responsible for loaded terrain.
func (m *Module) NoCollision(box AABB) bool {
	if m.CollidesWith(box) {
		return false
	}
	if m.entities != nil {
		query := box.Inflate(Epsilon, Epsilon, Epsilon)
		if len(m.entities.CollisionBoxes(query.MinX, query.MinY, query.MinZ, query.MaxX, query.MaxY, query.MaxZ)) != 0 {
			return false
		}
	}
	var borderStorage [1]voxelShape
	for _, border := range m.appendBorder(borderStorage[:0], box, (box.MinX+box.MaxX)/2, (box.MinZ+box.MaxZ)/2) {
		for i := range border.size() {
			if box.intersectsVoxel(border.part(i)) {
				return false
			}
		}
	}
	return true
}

// CollidesWith reports block collision overlap, without pose-fit deflation.
func (m *Module) CollidesWith(box AABB) bool {
	return len(m.blockCollisions(box, box.MinY, false, nil)) != 0
}

// CollidesWithSuffocatingBlock is the collision query used by corner escape.
// Fences, stairs and other merely-collidable blocks are not suffocating blocks.
func (m *Module) CollidesWithSuffocatingBlock(box AABB) bool {
	return len(m.blockCollisions(box, box.MinY, true, nil)) != 0
}
