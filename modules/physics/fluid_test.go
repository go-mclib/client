package physics

import (
	"math"
	"testing"

	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/data/blocks"
)

type fluidTestWorld map[[3]int]int32

func (w fluidTestWorld) GetBlock(x, y, z int) int32 { return w[[3]int{x, y, z}] }

func fluidTestState(t *testing.T, name string, props map[string]string) int32 {
	t.Helper()
	id := blocks.BlockID("minecraft:" + name)
	state := blocks.DefaultStateID(id)
	if props != nil {
		state = blocks.StateID(int(id), props)
	}
	if state < 0 {
		t.Fatalf("missing block state %s %v", name, props)
	}
	return state
}

func TestFluidScanUsesIndependentDepthsAndPoseEyes(t *testing.T) {
	waterState := fluidTestState(t, "water", map[string]string{"level": "0"})
	lavaState := fluidTestState(t, "lava", map[string]string{"level": "7"})
	w := fluidTestWorld{{0, 0, 0}: waterState, {1, 0, 0}: lavaState, {0, 1, 0}: waterState}
	water, lava := scanFluids(w, 1, 0.1, 0.5, collisions.PlayerWidth, 1.8, float64(float32(1.62)))
	sourceHeight, thinHeight := float64(float32(8.0/9.0)), float64(float32(1.0/9.0))
	wantWater, wantLava := 1+sourceHeight, thinHeight
	wantWater -= 0.1
	wantLava -= 0.1
	if water.height != wantWater || lava.height != wantLava {
		t.Fatalf("independent depths = water %.17g lava %.17g", water.height, lava.height)
	}
	if water.eyesInside {
		t.Fatal("water beside, not at the eyes, submerges eyes")
	}
	water, _ = scanFluids(w, 0.9, 0.1, 0.5, collisions.PlayerWidth, 1.8, float64(float32(1.62)))
	if !water.eyesInside {
		t.Fatal("upper source cell does not submerge standing eyes")
	}
	delete(w, [3]int{0, 1, 0})
	water, _ = scanFluids(w, 0.9, 0.1, 0.5, collisions.PlayerWidth, 1.8, float64(float32(1.62)))
	if water.eyesInside {
		t.Fatal("feet immersion incorrectly submerges standing eyes")
	}
	water, _ = scanFluids(w, 0.9, 0.1, 0.5, collisions.PlayerWidth, 0.6, float64(float32(0.4)))
	if !water.eyesInside {
		t.Fatal("swimming pose eye height not used")
	}
}

func TestScaledFluidBoundsReachNeighboringCells(t *testing.T) {
	w := fluidTestWorld{{1, 0, 0}: fluidTestState(t, "water", map[string]string{"level": "0"})}
	water, _ := scanFluids(w, 0.5, 0, 0.5, collisions.PlayerWidth, 1.8, float64(float32(1.62)))
	if water.height != 0 {
		t.Fatal("ordinary player reaches neighboring fluid cell")
	}
	water, _ = scanFluids(w, 0.5, 0, 0.5, collisions.PlayerWidth*2, 1.8, float64(float32(1.62)))
	if water.height != float64(float32(8.0/9.0)) {
		t.Fatalf("scaled player immersion = %.17g", water.height)
	}
}

func TestWaterloggedAndBubbleCellsAreFullSourceFluid(t *testing.T) {
	states := []int32{
		fluidTestState(t, "oak_slab", map[string]string{"type": "bottom", "waterlogged": "true"}),
		fluidTestState(t, "bubble_column", map[string]string{"drag": "true"}),
		fluidTestState(t, "kelp_plant", nil),
	}
	for _, state := range states {
		w := fluidTestWorld{{0, 0, 0}: state}
		water, lava := scanFluids(w, 0.5, 0.5, 0.5, collisions.PlayerWidth, 0.6, float64(float32(0.4)))
		if water.height != float64(float32(8.0/9.0))-0.5 || lava.height != 0 {
			t.Fatalf("state %d immersion = %+v, %+v", state, water, lava)
		}
		// Eye equality uses the actual fluid surface, not the old 0.111 offset.
		water, _ = scanFluids(w, 0.5, 0.5, 0.5, collisions.PlayerWidth, 0.6, float64(float32(8.0/9.0))-0.5)
		if !water.eyesInside {
			t.Fatalf("state %d excludes eyes exactly at surface", state)
		}
	}
}

func TestFluidCurrentsAverageStationaryCellsAndPreserveMinimumImpulse(t *testing.T) {
	source := fluidTestState(t, "water", map[string]string{"level": "0"})
	flowing := fluidTestState(t, "water", map[string]string{"level": "1"})
	w := fluidTestWorld{{0, 0, 0}: source, {1, 0, 0}: source, {2, 0, 0}: flowing}
	water, _ := scanFluids(w, 1, 0, 0.5, collisions.PlayerWidth, 1.8, float64(float32(1.62)))
	x, y, z := water.impulse(0.1, 0, waterFlowScale)
	if x != 0.007 || y != 0 || z != 0 {
		t.Fatalf("current over one still/one moving cell = %g,%g,%g", x, y, z)
	}
	// Shallow immersion scales the direction before averaging; resting entities
	// get the minimum current, but already moving entities do not.
	water, _ = scanFluids(w, 1, 0.8, 0.5, collisions.PlayerWidth, 1.8, float64(float32(1.62)))
	x, _, _ = water.impulse(0, 0, waterFlowScale)
	if x != 0.0045000000000000005 {
		t.Fatalf("minimum shallow impulse = %.17g", x)
	}
	x, _, _ = water.impulse(0.1, 0, waterFlowScale)
	want := float64(float32(8.0 / 9.0))
	want -= 0.8
	want /= 2
	want *= waterFlowScale
	if x != want {
		t.Fatalf("moving shallow impulse = %.17g, want %.17g", x, want)
	}
	water, _ = scanFluids(w, 1, float64(float32(8.0/9.0))-0.002, 0.5, collisions.PlayerWidth, 1.8, float64(float32(1.62)))
	x, _, _ = water.impulse(0, 0, waterFlowScale)
	if x != 0 {
		t.Fatalf("sub-threshold accumulated flow incorrectly receives minimum: %g", x)
	}
}

func TestFallingFlowNormalizesHorizontalBeforeDownwardBias(t *testing.T) {
	falling := fluidTestState(t, "water", map[string]string{"level": "15"})
	flowing := fluidTestState(t, "water", map[string]string{"level": "1"})
	stone := fluidTestState(t, "stone", nil)
	w := fluidTestWorld{{0, 0, 0}: falling, {1, 0, 0}: flowing, {0, 0, -1}: stone}
	x, y, z := getFluidFlow(w, fluidWater, 0, 0, 0)
	if x != 1/math.Sqrt(37) || y != -6/math.Sqrt(37) || z != 0 {
		t.Fatalf("falling current = %.17g, %.17g, %.17g", x, y, z)
	}
	w[[3]int{0, 0, -1}] = fluidTestState(t, "stone_slab", map[string]string{"type": "bottom", "waterlogged": "false"})
	x, y, z = getFluidFlow(w, fluidWater, 0, 0, 0)
	if x != 1 || y != 0 || z != 0 {
		t.Fatalf("non-sturdy slab triggers falling bias: %g,%g,%g", x, y, z)
	}
	w[[3]int{0, 0, -1}] = fluidTestState(t, "ice", nil)
	_, y, _ = getFluidFlow(w, fluidWater, 0, 0, 0)
	if y != 0 {
		t.Fatal("ice incorrectly triggers falling bias")
	}
}

func TestFluidWallExitRequiresNoLiquidCellEvenAboveSurface(t *testing.T) {
	w := fluidTestWorld{{0, 0, 0}: fluidTestState(t, "water", map[string]string{"level": "7"})}
	box := collisions.NewAABB(0.1, 0.5, 0.1, 0.9, 1.1, 0.9)
	if !containsFluid(w, box) {
		t.Fatal("wall exit accepted a box sharing a shallow fluid cell")
	}
	if containsFluid(w, box.Move(0, 0.5, 0)) {
		t.Fatal("wall exit rejected air above the fluid cell")
	}
}

func TestWaterTravelUsesFloatDragAndSprintGravityExemption(t *testing.T) {
	s := self.New()
	m := &Module{velX: 1, velY: -0.1}
	m.applyWaterInputScaled(s, 0, 0, 0)
	m.applyWaterPhysics(s, 0, 0, 0, 1.8, 0)
	expectedY := -0.1
	expectedY *= float64(float32(0.8))
	expectedY -= 0.08 / 16
	if m.velX != float64(float32(0.8)) || m.velY != expectedY {
		t.Fatalf("water drag/gravity velocity = %.17g %.17g", m.velX, m.velY)
	}
	s.SetSprinting(true)
	m.velX, m.velY = 1, -0.1
	m.applyWaterInputScaled(s, 0, 0, 0)
	m.applyWaterPhysics(s, 0, 0, 0, 1.8, 0)
	expectedY = -0.1
	expectedY *= float64(float32(0.8))
	if m.velX != float64(float32(0.9)) || m.velY != expectedY {
		t.Fatalf("sprinting water velocity = %.17g %.17g", m.velX, m.velY)
	}
}

func TestWaterEfficiencyAndDolphinsGrace(t *testing.T) {
	drag, speed := waterTravelCoefficients(false, true, false, 1, 0.1)
	// Java float results of speed += (getSpeed() - speed) * waterWalker.
	if drag != 0x1.178d52p-1 || speed != 0x1.999998p-4 {
		t.Fatalf("ground water efficiency: %g %g", drag, speed)
	}
	drag, speed = waterTravelCoefficients(false, false, false, 1, 0.1)
	if drag != 0x1.589376p-1 || speed != 0x1.eb851ep-5 {
		t.Fatalf("airborne water efficiency: %g %g", drag, speed)
	}
	drag, graceSpeed := waterTravelCoefficients(false, false, true, 1, 0.1)
	if drag != float32(0.96) || graceSpeed != speed {
		t.Fatalf("Dolphin's Grace: %g %g", drag, graceSpeed)
	}
}

func TestLavaImmersionChangesVerticalDragAndGravity(t *testing.T) {
	s := self.New()
	m := &Module{velX: 1, velY: -0.1, fluid: fluidState{eyeHeight: 1.62, lavaHeight: 0.4}}
	m.applyLavaInputScaled(s, 0, 0, 0)
	m.applyLavaPhysics(s, 0, 0, 0, 1.8, 0)
	expectedY := -0.1
	expectedY *= float64(float32(0.8))
	expectedY -= 0.08 / 16
	expectedY -= 0.08 / 4
	if m.velX != 0.5 || m.velY != expectedY {
		t.Fatalf("shallow lava velocity = %.17g %.17g", m.velX, m.velY)
	}
	m.velX, m.velY, m.fluid.lavaHeight = 1, -0.1, math.Nextafter(0.4, 1)
	m.applyLavaInputScaled(s, 0, 0, 0)
	m.applyLavaPhysics(s, 0, 0, 0, 1.8, 0)
	expectedY = -0.1
	expectedY *= 0.5
	expectedY -= 0.08 / 4
	if m.velX != 0.5 || m.velY != expectedY {
		t.Fatalf("deep lava velocity = %.17g %.17g", m.velX, m.velY)
	}
}

func TestFluidFallingCorrectionUsesCapturedDirection(t *testing.T) {
	if got := fluidFallingAdjustedY(0.001, 0.016, true, false); got != -0.003 {
		t.Fatalf("falling correction = %g", got)
	}
	if got := fluidFallingAdjustedY(0.001, 0.016, false, false); got != 0 {
		t.Fatalf("rising incorrectly corrected = %g", got)
	}
	if got := fluidFallingAdjustedY(0.001, 0.016, true, true); got != 0.001 {
		t.Fatalf("sprinting incorrectly corrected = %g", got)
	}
}
