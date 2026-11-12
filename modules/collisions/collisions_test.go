package collisions

import (
	"math"
	"testing"

	"github.com/go-mclib/data/blocks"
)

type collisionWorld map[[3]int]int32

func (w collisionWorld) GetLoadedBlock(x, y, z int) (int32, bool) {
	return w[[3]int{x, y, z}], true
}

func blockState(t *testing.T, name string, props map[string]string) int32 {
	t.Helper()
	id := blocks.ID("minecraft:" + name)
	state := blocks.DefaultStateID(id)
	if props != nil {
		state = blocks.StateID(id, props)
	}
	if state < 0 {
		t.Fatalf("invalid fixture state %s %v", name, props)
	}
	return state
}

func floorWorld(t *testing.T) collisionWorld {
	t.Helper()
	w := collisionWorld{}
	stone := blockState(t, "stone", nil)
	for x := -1; x <= 3; x++ {
		for z := -1; z <= 3; z++ {
			w[[3]int{x, 0, z}] = stone
		}
	}
	return w
}

func TestWallSlideRetainsTangentMovement(t *testing.T) {
	w := floorWorld(t)
	for y := 1; y <= 3; y++ {
		w[[3]int{1, y, 0}] = blockState(t, "stone", nil)
	}
	m := &Module{world: w}
	x, y, z, horizontal, vertical := m.CollideMovement(0.5, 1, 0.5, PlayerWidth, PlayerHeight, 0.4, -0.08, 0.3, true, StepUpHeight)
	if x != 1-(0.5+float64(float32(0.6))/2) || y != 0 || z != 0.3 || !horizontal || !vertical {
		t.Fatalf("wall slide = (%v,%v,%v), collision=(%v,%v)", x, y, z, horizontal, vertical)
	}
}

func TestAirborneNearFloorCannotStep(t *testing.T) {
	w := floorWorld(t)
	w[[3]int{1, 1, 0}] = blockState(t, "stone_slab", map[string]string{"type": "bottom", "waterlogged": "false"})
	m := &Module{world: w}
	x, y, _, horizontal, vertical := m.CollideMovement(0.5, 1.0005, 0.5, PlayerWidth, PlayerHeight, 0.5, 0, 0, false, StepUpHeight)
	if x != 1-(0.5+PlayerWidth/2) || y != 0 || !horizontal || vertical {
		t.Fatalf("airborne player stepped: x=%v y=%v collision=%v/%v", x, y, horizontal, vertical)
	}
}

func TestLandingStepUsesGroundedCandidateAndFinalFlags(t *testing.T) {
	w := floorWorld(t)
	w[[3]int{1, 1, 0}] = blockState(t, "stone_slab", map[string]string{"type": "bottom", "waterlogged": "false"})
	m := &Module{world: w}
	for _, start := range []struct {
		y, dy, wantY float64
		ground       bool
	}{
		{1, 0, 0.5, true},
		{1.2, -0.4, 0.5 - (1.2 - 1.0), false},
	} {
		x, y, z, horizontal, vertical := m.CollideMovement(0.5, start.y, 0.5, PlayerWidth, PlayerHeight, 0.5, start.dy, 0, start.ground, StepUpHeight)
		if x != 0.5 || math.Abs(y-start.wantY) > 1e-15 || z != 0 || horizontal || !vertical {
			t.Fatalf("landing/step from %v = (%v,%v,%v), collision=%v/%v", start, x, y, z, horizontal, vertical)
		}
	}
}

func TestStepCannotPassLowCeiling(t *testing.T) {
	w := floorWorld(t)
	w[[3]int{1, 1, 0}] = blockState(t, "stone_stairs", map[string]string{"facing": "east", "half": "bottom", "shape": "straight", "waterlogged": "false"})
	for x := range 2 {
		w[[3]int{x, 3, 0}] = blockState(t, "stone", nil)
	}
	m := &Module{world: w}
	x, y, _, horizontal, _ := m.CollideMovement(0.5, 1, 0.5, PlayerWidth, PlayerHeight, 0.5, -0.08, 0, true, StepUpHeight)
	if x != 1-(0.5+PlayerWidth/2) || y != 0 || !horizontal {
		t.Fatalf("stepped into ceiling: x=%v y=%v horizontal=%v", x, y, horizontal)
	}
	delete(w, [3]int{0, 3, 0})
	delete(w, [3]int{1, 3, 0})
	x, y, _, horizontal, _ = m.CollideMovement(0.5, 1, 0.5, PlayerWidth, PlayerHeight, 0.5, -0.08, 0, true, StepUpHeight)
	if x != 0.5 || y != 0.5 || horizontal {
		t.Fatalf("uncovered stair failed: x=%v y=%v horizontal=%v", x, y, horizontal)
	}
}

func TestTallCollisionOriginsAndOrientations(t *testing.T) {
	for _, kind := range []string{"oak_fence", "cobblestone_wall", "oak_fence_gate"} {
		for _, facing := range []string{"north", "south", "west", "east"} {
			t.Run(kind+"/"+facing, func(t *testing.T) {
				var props map[string]string
				halfThickness := 0.125
				switch kind {
				case "oak_fence":
					props = map[string]string{"north": "false", "south": "false", "west": "false", "east": "false", "waterlogged": "false"}
					props[facing] = "true"
				case "cobblestone_wall":
					props = map[string]string{"north": "none", "south": "none", "west": "none", "east": "none", "waterlogged": "false", "up": "true"}
					props[facing] = "low"
					halfThickness = 0.25
				default:
					props = map[string]string{"facing": facing, "in_wall": "true", "open": "false", "powered": "false"}
				}
				w := collisionWorld{{1, 0, 1}: blockState(t, kind, props)}
				m := &Module{world: w}
				dx, dz := 1.0, 0.0
				if facing == "east" || facing == "west" {
					dx, dz = 0, 1
				}
				if facing == "south" || facing == "west" {
					dx, dz = -dx, -dz
				}
				// Gates run along their facing-normal axis, unlike fence arms.
				if kind == "oak_fence_gate" {
					dx, dz = dz, dx
				}
				x, z := 1.5-dx, 1.5-dz
				ax, ay, az, horizontal, vertical := m.CollideMovement(x, 1.25, z, PlayerWidth, PlayerHeight, dx, 0, dz, false, StepUpHeight)
				gap := 1 - halfThickness - PlayerWidth/2
				if math.Abs(ax-dx*gap) > 1e-15 || ay != 0 || math.Abs(az-dz*gap) > 1e-15 || !horizontal || vertical {
					t.Fatalf("passed tall %s: movement=%v/%v/%v flags=%v/%v", kind, ax, ay, az, horizontal, vertical)
				}
				if m.CanFitAt(1.5, 1.25, 1.5, PlayerWidth, PlayerHeight) {
					t.Fatal("fit ignored tall collision from lower block")
				}
				if top, ok := m.SupportSurfaceY(1, 1, 1, PlayerWidth); !ok || top != 1.5 {
					t.Fatalf("tall support=%v/%v", top, ok)
				}
				if kind == "oak_fence_gate" {
					props["open"] = "true"
					w[[3]int{1, 0, 1}] = blockState(t, kind, props)
					ax, ay, az, horizontal, vertical = m.CollideMovement(x, 1.25, z, PlayerWidth, PlayerHeight, dx, 0, dz, false, StepUpHeight)
					if ax != dx || ay != 0 || az != dz || horizontal || vertical {
						t.Fatal("open gate retains collision")
					}
				}
			})
		}
	}
}

func TestFenceCollisionIsNotFullCube(t *testing.T) {
	state := blockState(t, "oak_fence", map[string]string{"north": "false", "south": "false", "west": "false", "east": "false", "waterlogged": "false"})
	m := &Module{world: collisionWorld{{1, 0, 1}: state}}
	if !m.CanFitAt(1.05, 1.25, 1.5, PlayerWidth, PlayerHeight) {
		t.Fatal("thin fence shape replaced by full cube")
	}
	if m.IsOnGround(1.5, 1.5005, 1.5, PlayerWidth) {
		t.Fatal("ground probe accepts hovering")
	}
	if !m.IsOnGround(1.5, 1.5, 1.5, PlayerWidth) {
		t.Fatal("fence top does not support feet")
	}
}

func TestVoxelInternalPlanesClipInitialOverlap(t *testing.T) {
	shape := voxelShape{
		parts:  []blocks.AABB{{MinX: 0, MinY: 0, MinZ: 0, MaxX: 1, MaxY: 1, MaxZ: 1}},
		coords: [3][]float64{{0, 0.5, 1}, {0, 0.375, 1}, {0, 1}},
	}
	box := AABB{MinX: 0.1, MinY: 0.1, MinZ: 0.1, MaxX: 0.4, MaxY: 0.3, MaxZ: 0.4}
	if got := shape.clip(box, 0, 1); got != 0.5-box.MaxX {
		t.Fatalf("initial overlap lost internal grid plane: %v", got)
	}
	if got := shape.clip(box, 0, -1); got != -1 {
		t.Fatalf("initial overlap cannot escape: %v", got)
	}
	candidates := collectCandidateStepUpHeights(AABB{MinY: 0}, []voxelShape{shape}, float32(StepUpHeight), 0)
	if len(candidates) != 1 || candidates[0] != 0.375 {
		t.Fatalf("internal step planes=%v", candidates)
	}
}

func TestAxisOrderAndPerpendicularEpsilon(t *testing.T) {
	box := AABB{MinX: 0, MinY: 0, MinZ: 0, MaxX: 0.6, MaxY: 1.8, MaxZ: 0.6}
	wall := voxelShape{box: AABB{MinX: 0.7, MinY: 0, MinZ: 0.7, MaxX: 1, MaxY: 2, MaxZ: 1}}
	x, _, z := collideWithShapes(box, []voxelShape{wall}, 0.2, 0, 0.3)
	if z != 0.3 || x != 0.7-box.MaxX {
		t.Fatalf("dominant Z axis not resolved first: %v/%v", x, z)
	}
	x, _, z = collideWithShapes(box, []voxelShape{wall}, 0.3, 0, 0.2)
	if x != 0.3 || z != 0.7-box.MaxZ {
		t.Fatalf("dominant X axis not resolved first: %v/%v", x, z)
	}
	thin := AABB{MinX: 1, MinY: box.MaxY - Epsilon, MinZ: 0, MaxX: 2, MaxY: 3, MaxZ: 1}
	if got := box.clipCollide(thin, 0, 1); got != 1-box.MaxX {
		t.Fatalf("exact perpendicular voxel boundary=%v", got)
	}
	thin.MinY = math.Nextafter(thin.MinY, math.Inf(1))
	if got := box.clipCollide(thin, 0, 1); got != 1 {
		t.Fatalf("below-epsilon overlap collides: %v", got)
	}
}

func TestContextualScaffoldAndPowderSnow(t *testing.T) {
	scaffold := blockState(t, "scaffolding", map[string]string{"distance": "1", "bottom": "true", "waterlogged": "false"})
	m := &Module{world: collisionWorld{{0, 0, 0}: scaffold}}
	if !m.IsOnGround(0.5, 1, 0.5, PlayerWidth) {
		t.Fatal("scaffolding top failed to support player")
	}
	m.SetPlayerContext(true, 0, false)
	if m.IsOnGround(0.5, 1, 0.5, PlayerWidth) {
		t.Fatal("descending player cannot pass scaffolding top")
	}
	m.world = collisionWorld{{0, 0, 0}: powderSnowState}
	m.SetPlayerContext(false, 0, false)
	if m.IsOnGround(0.5, 1, 0.5, PlayerWidth) {
		t.Fatal("powder snow supports bare feet")
	}
	m.SetPlayerContext(false, 0, true)
	if !m.IsOnGround(0.5, 1, 0.5, PlayerWidth) {
		t.Fatal("leather boots cannot stand on powder snow")
	}
	m.SetPlayerContext(false, 3, false)
	_, dy, _, _, vertical := m.CollideMovement(0.5, 1.2, 0.5, PlayerWidth, PlayerHeight, 0, -0.5, 0, false, StepUpHeight)
	if dy != float64(float32(0.9))-1.2 || !vertical {
		t.Fatalf("powder falling collision=%v/%v", dy, vertical)
	}
}

func TestSuffocationAndUnknownFit(t *testing.T) {
	w := collisionWorld{{0, 0, 0}: blockState(t, "oak_fence", nil)}
	m := &Module{world: w}
	box := EntityAABB(0.5, 0.5, 0.5, PlayerWidth, PlayerHeight)
	if m.CollidesWithSuffocatingBlock(box) {
		t.Fatal("fence marked suffocating")
	}
	w[[3]int{0, 0, 0}] = blockState(t, "stone", nil)
	if !m.CollidesWithSuffocatingBlock(box) {
		t.Fatal("stone not suffocating")
	}
	if New().CanFitAt(0.5, 64, 0.5, PlayerWidth, PlayerHeight) {
		t.Fatal("unknown terrain reported safe fit")
	}
}
