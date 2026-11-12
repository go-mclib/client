package physics

import (
	"math"
	"strings"

	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/client/modules/world"
	"github.com/go-mclib/data/blocks"
	"github.com/go-mclib/data/registries"
)

// Block effects run after travel; stuck multipliers are consumed by the next move.
type blockEffects struct {
	stuckX, stuckY, stuckZ float64
	originalX, originalZ   float64
	inPowderSnow           bool
	visited                map[[3]int]struct{}
	traversalVisited       map[[3]int]struct{}
}

var (
	waterBlockID        = blocks.ID("minecraft:water")
	bubbleColumnBlockID = blocks.ID("minecraft:bubble_column")
	effectWeaving       = registries.MobEffect.Get("minecraft:weaving")
)

// getBlockFriction returns vanilla Block.getFriction(), widened from float.
func getBlockFriction(stateID int32) float64 {
	switch blockName(stateID) {
	case "minecraft:ice", "minecraft:packed_ice":
		return float64(float32(0.98))
	case "minecraft:blue_ice":
		return float64(float32(0.989))
	case "minecraft:slime_block":
		return float64(float32(0.8))
	default:
		return defaultBlockFriction
	}
}

func blockName(stateID int32) string {
	return blocks.Name(blocks.StateBlockID(stateID))
}

func blockSpeedFactorByID(blockID int32) float64 {
	switch blocks.Name(blockID) {
	case "minecraft:soul_sand", "minecraft:honey_block":
		return float64(float32(0.4))
	default:
		return 1
	}
}

// supportingBlockAt uses the collision footprint, including overhanging feet and
// tall fences, rather than assuming the block under the player's center supports it.
func supportingBlockAt(w *world.Module, x, y, z, width float64) (pos [3]int, found bool) {
	half := width / 2
	best := math.Inf(1)
	for by := int(math.Floor(y)) - 2; by <= int(math.Floor(y)); by++ {
		for bz := int(math.Floor(z - half)); bz <= int(math.Floor(z+half)); bz++ {
			for bx := int(math.Floor(x - half)); bx <= int(math.Floor(x+half)); bx++ {
				for _, shape := range blocks.CollisionShape(w.GetBlock(bx, by, bz)) {
					if x+half <= float64(bx)+shape.MinX || x-half >= float64(bx)+shape.MaxX ||
						z+half <= float64(bz)+shape.MinZ || z-half >= float64(bz)+shape.MaxZ ||
						y <= float64(by)+shape.MinY || y-1e-6 >= float64(by)+shape.MaxY {
						continue
					}
					dx, dy, dz := x-float64(bx)-0.5, y-float64(by)-0.5, z-float64(bz)-0.5
					distance := dx*dx + dy*dy + dz*dz
					// Vec3i.compareTo orders Y, then Z, then X; equal-distance ties prefer the greater position.
					greater := by > pos[1] || by == pos[1] && (bz > pos[2] || bz == pos[2] && bx > pos[0])
					if distance < best || distance == best && greater {
						best, pos, found = distance, [3]int{bx, by, bz}, true
					}
				}
			}
		}
	}
	return
}

func blockOnPos(w *world.Module, x, y, z, width, offset float64) [3]int {
	if pos, ok := supportingBlockAt(w, x, y, z, width); ok {
		name := blockName(w.GetBlock(pos[0], pos[1], pos[2]))
		if offset <= 0.5 && strings.HasSuffix(name, "_fence") || strings.HasSuffix(name, "_wall") || strings.HasSuffix(name, "_fence_gate") {
			return pos
		}
		pos[1] = int(math.Floor(y - offset))
		return pos
	}
	return [3]int{int(math.Floor(x)), int(math.Floor(y - offset)), int(math.Floor(z))}
}

// The 0.500001F offset is deliberate (Entity.getBlockPosBelowThatAffectsMyMovement).
func getBlockSpeedFactorAt(w *world.Module, x, y, z, width float64) float64 {
	feet := blocks.StateBlockID(w.GetBlock(int(math.Floor(x)), int(math.Floor(y)), int(math.Floor(z))))
	factor := blockSpeedFactorByID(feet)
	if feet == waterBlockID || feet == bubbleColumnBlockID || factor != 1 {
		return factor
	}
	pos := blockOnPos(w, x, y, z, width, float64(float32(0.500001)))
	return blockSpeedFactorByID(blocks.StateBlockID(w.GetBlock(pos[0], pos[1], pos[2])))
}

func blockJumpFactorAt(w *world.Module, x, y, z, width float64) float64 {
	if blockName(w.GetBlock(int(math.Floor(x)), int(math.Floor(y)), int(math.Floor(z)))) == "minecraft:honey_block" {
		return float64(float32(0.5))
	}
	pos := blockOnPos(w, x, y, z, width, float64(float32(0.500001)))
	if blockName(w.GetBlock(pos[0], pos[1], pos[2])) == "minecraft:honey_block" {
		return float64(float32(0.5))
	}
	return 1
}

func (m *Module) consumeStuckMovement(dx, dy, dz float64) (float64, float64, float64) {
	b := &m.blocks
	if b.stuckX*b.stuckX+b.stuckY*b.stuckY+b.stuckZ*b.stuckZ > 1e-7 {
		dx, dy, dz = dx*b.stuckX, dy*b.stuckY, dz*b.stuckZ
		b.stuckX, b.stuckY, b.stuckZ = 0, 0, 0
		m.velX, m.velY, m.velZ = 0, 0, 0
	}
	b.originalX, b.originalZ = dx, dz
	return dx, dy, dz
}

func entityBounciness(s *self.Module) float64 {
	if s.Sneaking() {
		return 0
	}
	return s.AttributeValue("minecraft:bounciness", 0)
}

// Entity.restituteMovementAfterCollisions in 26.2 compensates gravity and drag
// for the portion of this move before impact; simply negating Y is not equivalent.
func (m *Module) applyLandingEffects(s *self.Module, x, y, z, incomingY, resolvedY float64) {
	pos := blockOnPos(m.world, x, y, z, m.bodyWidth(), float64(float32(0.2)))
	name := blockName(m.world.GetBlock(pos[0], pos[1], pos[2]))
	gravity := m.getEffectiveGravity(s)
	restitution := entityBounciness(s)
	if m.onGround {
		if -incomingY < gravity || s.Sneaking() || name == "minecraft:honey_block" {
			restitution = 0
		} else if name == "minecraft:slime_block" {
			restitution = max(restitution, 1)
		} else if strings.HasSuffix(name, "_bed") {
			restitution = max(restitution, 0.75)
		}
	}
	m.velY = collisionBounceY(incomingY, resolvedY, gravity, ModifiedFriction(VerticalAirFriction, s.AttributeValue("minecraft:air_drag_modifier", 1)), restitution)
}

func collisionBounceY(incoming, resolved, gravity, drag, restitution float64) float64 {
	if restitution <= 0 {
		return 0
	}
	portion := resolved / incoming
	return (portion*gravity - incoming) * (1 + portion*(drag-1)) * restitution
}

// Called after travel's gravity/drag, before entity pushing (LivingEntity.aiStep).
func (m *Module) applyInsideBlockEffects(s *self.Module, fromX, fromY, fromZ, x, y, z, height float64) {
	if s.IsSpectator() {
		return
	}
	if m.onGround {
		pos := blockOnPos(m.world, x, y, z, m.bodyWidth(), float64(float32(0.2)))
		if blockName(m.world.GetBlock(pos[0], pos[1], pos[2])) == "minecraft:slime_block" && math.Abs(m.velY) < 0.1 && !s.Sneaking() {
			scale := 0.4 + math.Abs(m.velY)*0.2
			m.velX, m.velZ = m.velX*scale, m.velZ*scale
		}
	}
	m.blocks.inPowderSnow = false
	if m.blocks.visited == nil {
		m.blocks.visited = make(map[[3]int]struct{})
		m.blocks.traversalVisited = make(map[[3]int]struct{})
	}
	defer clear(m.blocks.visited)
	from, to := [3]float64{fromX, fromY, fromZ}, [3]float64{x, y, z}
	visitMovementBlocks(from, to, m.bodyWidth(), height, m.blocks.originalX, m.blocks.originalZ, m.blocks.traversalVisited, func(pos [3]int, precise bool, minY float64) {
		if _, seen := m.blocks.visited[pos]; seen {
			return
		}
		state := m.world.GetBlock(pos[0], pos[1], pos[2])
		if !movementEffectBlock(state) {
			return
		}
		if blockName(state) == "minecraft:powder_snow" && m.fallDistance > 2.5 && minY+1e-7 >= float64(pos[1])+float64(float32(0.9)) {
			return
		}
		m.blocks.visited[pos] = struct{}{}
		m.applyInsideBlock(s, state, pos, to, precise)
	})
}

func movementEffectBlock(state int32) bool {
	switch blockName(state) {
	case "minecraft:cobweb", "minecraft:sweet_berry_bush", "minecraft:powder_snow", "minecraft:honey_block", "minecraft:bubble_column":
		return true
	default:
		return false
	}
}

func (m *Module) applyInsideBlock(s *self.Module, state int32, pos [3]int, to [3]float64, precise bool) {
	// makeStuckInBlock resets fall distance as soon as the contact is applied.
	switch blockName(state) {
	case "minecraft:cobweb":
		if s.IsFlying() {
			return
		}
		m.blocks.stuckX, m.blocks.stuckY, m.blocks.stuckZ = 0.25, float64(float32(0.05)), 0.25
		m.fallDistance = 0
		if s.HasEffect(effectWeaving) {
			m.blocks.stuckX, m.blocks.stuckY, m.blocks.stuckZ = 0.5, 0.25, 0.5
		}
	case "minecraft:sweet_berry_bush":
		if s.IsFlying() {
			return
		}
		m.blocks.stuckX, m.blocks.stuckY, m.blocks.stuckZ = float64(float32(0.8)), 0.75, float64(float32(0.8))
		m.fallDistance = 0
	case "minecraft:powder_snow":
		m.blocks.inPowderSnow = true
		if !s.IsFlying() && blockName(m.world.GetBlock(int(math.Floor(to[0])), int(math.Floor(to[1])), int(math.Floor(to[2])))) == "minecraft:powder_snow" {
			m.blocks.stuckX, m.blocks.stuckY, m.blocks.stuckZ = float64(float32(0.9)), 1.5, float64(float32(0.9))
			m.fallDistance = 0
		}
	case "minecraft:honey_block":
		m.applyHoneySlide(pos, to)
	case "minecraft:bubble_column":
		if precise && !s.IsFlying() {
			_, props := blocks.StateProperties(state)
			above := m.world.GetBlock(pos[0], pos[1]+1, pos[2])
			fluid, _, _ := blocks.FluidState(above)
			m.velY = bubbleColumnVelocity(m.velY, props["drag"] == "true", !blocks.HasCollision(above) && fluid == 0)
			if blocks.HasCollision(above) || fluid != 0 {
				m.fallDistance = 0
			}
		}
	}
}

func (m *Module) applyHoneySlide(pos [3]int, to [3]float64) {
	oldY := m.velY/float64(float32(0.98)) + 0.08
	if m.onGround || to[1] > float64(pos[1])+0.9375-1e-7 || oldY >= -0.08 {
		return
	}
	distance := 0.4375 + m.bodyWidth()/2
	if math.Abs(float64(pos[0])+0.5-to[0])+1e-7 <= distance && math.Abs(float64(pos[2])+0.5-to[2])+1e-7 <= distance {
		return
	}
	if oldY < -0.13 {
		scale := -0.05 / oldY
		m.velX, m.velZ = m.velX*scale, m.velZ*scale
	}
	m.velY = (-0.05 - 0.08) * float64(float32(0.98))
	m.fallDistance = 0
}

func bubbleColumnVelocity(y float64, down, surface bool) float64 {
	if down {
		if surface {
			return max(-0.9, y-0.03)
		}
		return max(-0.3, y-0.03)
	}
	if surface {
		return min(1.8, y+0.1)
	}
	return min(0.7, y+0.06)
}
