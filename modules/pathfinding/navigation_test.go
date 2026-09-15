package pathfinding

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/go-mclib/client/modules/physics"
	"github.com/go-mclib/data/blocks"
	"github.com/go-mclib/data/chunks"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func waitNavigationPlanner(t *testing.T, m *Module) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		m.mu.Lock()
		planning := m.planning
		m.mu.Unlock()
		if !planning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("bounded planner did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

// dryNavigationStep exercises the real controller, shared vanilla travel
// primitives and real voxel collision resolver without a socket or tick timer.
// The motion packet exposes the previous tick's carried velocity to navigation.
func dryNavigationStep(t *testing.T, m *Module, velocity *[3]float64) bool {
	t.Helper()
	waitNavigationPlanner(t, m)
	// Fast-forward only the retry timer; physics still advances one fixed tick.
	m.mu.Lock()
	if len(m.path) == 0 && !m.planning && !m.waitingForTerrain {
		m.nextPlan = time.Time{}
	}
	m.mu.Unlock()
	wire, err := jp.ToWire(&packets.S2CSetEntityMotion{EntityId: ns.VarInt(m.self.EntityID()), Velocity: ns.LpVec3{X: velocity[0], Y: velocity[1], Z: velocity[2]}})
	if err != nil {
		t.Fatal(err)
	}
	m.physics.HandlePacket(wire)
	m.navigationTick()
	if !m.IsNavigating() {
		return false
	}
	x, y, z := m.self.Position()
	cfg := m.simulationConfig()
	if !navigationLoaded(m.world, x, y, z, cfg) {
		return false
	}
	forward, strafe, jumping := m.physics.Input()
	height, slow := cfg.height, 1.0
	if m.self.Sneaking() {
		height, slow = cfg.sneakingHeight, 0.3
	}
	forward, strafe = physics.ProcessInput(forward, strafe, 1, slow)
	onGround := m.collisions.IsOnGround(x, y, z, cfg.width)
	yaw, _ := m.self.Rotation()
	if jumping && onGround {
		dx, dy, dz := physics.JumpImpulse(float64(yaw), cfg.jumpPower, m.self.Sprinting())
		velocity[0] += dx
		velocity[1] = math.Max(velocity[1], dy)
		velocity[2] += dz
	}
	friction, acceleration := 1.0, physics.FlyingSpeed
	if m.self.Sprinting() {
		acceleration = physics.SprintFlyingSpeed
	}
	if onGround {
		friction = physics.ModifiedFriction(physics.GetBlockFrictionAt(m.world, x, y, z, cfg.width), cfg.frictionModifier)
		acceleration = physics.GroundAcceleration(m.self.MovementSpeed(m.self.Sprinting()), friction)
	}
	dx, _, dz := physics.MoveRelative(acceleration, forward, strafe, float64(yaw))
	velocity[0], velocity[2] = velocity[0]+dx, velocity[2]+dz
	ax, ay, az, _, _ := m.collisions.CollideMovement(x, y, z, cfg.width, height, velocity[0], velocity[1], velocity[2], onGround, cfg.maxStep)
	if ax != velocity[0] {
		velocity[0] = 0
	}
	if ay != velocity[1] {
		velocity[1] = 0
	}
	if az != velocity[2] {
		velocity[2] = 0
	}
	m.self.SetPosition(x+ax, y+ay, z+az)
	velocity[0], velocity[1], velocity[2] = physics.AirVelocity(velocity[0], velocity[1], velocity[2], friction, cfg.gravity, cfg.airDragModifier)
	return jumping
}

func TestNavigationBrakesWithoutEndpointOscillation(t *testing.T) {
	for _, surface := range []string{"stone", "blue_ice"} {
		t.Run(surface, func(t *testing.T) {
			m, column := navigationTerrain(t)
			defer m.Stop()
			state := blocks.DefaultStateID(blocks.BlockID("minecraft:" + surface))
			for x := range 16 {
				for z := range 16 {
					column.SetBlockState(x, 63, z, state)
				}
			}
			if err := m.NavigateTo(12.5, 64, 8.5); err != nil {
				t.Fatal(err)
			}
			var velocity [3]float64
			maxX, maxCross := 0.0, 0.0
			for range 240 {
				if dryNavigationStep(t, m, &velocity) {
					t.Fatal("flat walking produced an uncontrolled jump")
				}
				x, _, z := m.self.Position()
				maxX, maxCross = math.Max(maxX, x), math.Max(maxCross, math.Abs(z-8.5))
				if !m.IsNavigating() {
					break
				}
			}
			x, y, z := m.self.Position()
			if m.IsNavigating() || math.Hypot(x-12.5, z-8.5) > 0.12 || math.Abs(y-64) > 0.01 {
				t.Fatalf("did not settle at destination: (%g,%g,%g)", x, y, z)
			}
			if maxX > 12.62 || maxCross > 0.08 {
				t.Fatalf("overshoot/wobble: max x=%g, cross-track=%g", maxX, maxCross)
			}
		})
	}
}

func TestLookaheadCannotCutWallCornerOrUnsupportedGap(t *testing.T) {
	m, column := navigationTerrain(t)
	cfg := m.simulationConfig()
	stone := blocks.DefaultStateID(blocks.BlockID("minecraft:stone"))
	column.SetBlockState(5, 64, 8, stone)
	column.SetBlockState(5, 65, 8, stone)
	if walkCorridor(m.world, m.collisions, 4.7, 64, 8.5, 5.5, 64, 9.5, false, cfg) {
		t.Fatal("shortcut clipped the inside wall corner")
	}
	column.SetBlockState(5, 64, 8, 0)
	column.SetBlockState(5, 65, 8, 0)
	column.SetBlockState(5, 63, 8, 0)
	if walkCorridor(m.world, m.collisions, 4.5, 64, 8.5, 6.5, 64, 8.5, false, cfg) {
		t.Fatal("lookahead silently turned a walk into a gap jump")
	}
}

func loadNavigationColumn(t *testing.T, m *Module, cx int32) {
	t.Helper()
	column := chunks.NewColumn(cx, 0)
	column.Sections[7], column.Sections[8] = chunks.NewEmptySection(), chunks.NewEmptySection()
	stone := blocks.DefaultStateID(blocks.BlockID("minecraft:stone"))
	for x := range 16 {
		for z := range 16 {
			column.SetBlockState(int(cx)*16+x, 63, z, stone)
		}
	}
	data, err := column.EncodeSections()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := jp.ToWire(&packets.S2CLevelChunkWithLight{ChunkX: ns.Int32(cx), ChunkZ: 0, ChunkData: ns.ChunkData{Data: data}})
	if err != nil {
		t.Fatal(err)
	}
	m.world.HandlePacket(wire)
}

func TestNavigationContinuesAcrossMultipleLoadedFrontiers(t *testing.T) {
	m, _ := navigationTerrain(t)
	defer m.Stop()
	completed := make(chan bool, 1)
	m.OnNavigationComplete(func(ok bool) { completed <- ok })
	if err := m.NavigateToContext(context.Background(), 40.5, 64, 8.5); err != nil {
		t.Fatal(err)
	}
	var velocity [3]float64
	loaded := int32(0)
	for range 600 {
		waitNavigationPlanner(t, m)
		x, _, _ := m.self.Position()
		if loaded < 2 && x > float64(loaded*16)+11 {
			loaded++
			loadNavigationColumn(t, m, loaded)
		}
		dryNavigationStep(t, m, &velocity)
		x, _, _ = m.self.Position()
		if x > float64((loaded+1)*16)-1.3 {
			t.Fatal("navigation entered the unknown chunk halo")
		}
		select {
		case reached := <-completed:
			if !reached || math.Abs(x-40.5) > 0.12 || loaded != 2 {
				t.Fatalf("premature frontier completion: reached=%v x=%g loaded=%d", reached, x, loaded)
			}
			return
		default:
		}
	}
	t.Fatal("navigation failed to traverse successive loaded frontiers")
}

func TestUnloadedFrontierWaitsAndStopInvalidatesPlanner(t *testing.T) {
	m, _ := navigationTerrain(t)
	if err := m.NavigateTo(80.5, 64, 8.5); err != nil {
		t.Fatal(err)
	}
	var velocity [3]float64
	for range 160 {
		dryNavigationStep(t, m, &velocity)
	}
	if !m.IsNavigating() {
		t.Fatal("temporary unloaded frontier ended the operation")
	}
	x, _, _ := m.self.Position()
	if x > 14.7 {
		t.Fatalf("crossed physics safety halo: x=%g", x)
	}
	m.Stop()
	loadNavigationColumn(t, m, 1)
	waitNavigationPlanner(t, m)
	m.navigationTick()
	f, s, j := m.physics.Input()
	if m.IsNavigating() || f != 0 || s != 0 || j {
		t.Fatal("late terrain/planner publication restarted stopped navigation")
	}
}

func TestPlannedJumpPressesOnceAndSettlesAtLanding(t *testing.T) {
	m, column := navigationTerrain(t)
	defer m.Stop()
	// A settled sprint-jump crosses two missing blocks. Three requires a
	// running takeoff, which this fixture (and this trajectory) does not use.
	for x := 5; x <= 6; x++ {
		column.SetBlockState(x, 63, 8, 0)
	}
	cfg := m.simulationConfig()
	landing, ok := simulateOneJump(context.Background(), m.collisions, m.world, 4.5, 64, 8.5, 270, cfg)
	if !ok {
		t.Fatal("fixture jump has no simulated landing")
	}
	m.navigationContext = context.Background()
	m.navigating, m.segmentFinal = true, true
	m.goalX, m.goalY, m.goalZ = float64(landing.X)+0.5, float64(landing.Y), float64(landing.Z)+0.5
	m.path = []PathNode{{X: 4, Y: 64, Z: 8}, {X: landing.X, Y: landing.Y, Z: landing.Z, Jump: true, JumpYaw: 270}}
	m.resetWaypoint()
	var velocity [3]float64
	presses := 0
	for range 150 {
		if dryNavigationStep(t, m, &velocity) {
			presses++
		}
		if !m.IsNavigating() {
			break
		}
	}
	x, y, z := m.self.Position()
	if presses != 1 || m.IsNavigating() || math.Hypot(x-m.goalX, z-m.goalZ) > 0.12 || math.Abs(y-m.goalY) > 0.08 {
		t.Fatalf("jump did not land and settle exactly once: presses=%d position=(%g,%g,%g)", presses, x, y, z)
	}
}

func TestControllerTraversesStairsDescentsAndCrouchedCorridor(t *testing.T) {
	for _, terrain := range []string{"stairs", "descent", "crouch"} {
		t.Run(terrain, func(t *testing.T) {
			m, column := navigationTerrain(t)
			defer m.Stop()
			stone := blocks.DefaultStateID(blocks.BlockID("minecraft:stone"))
			goalY := 64.0
			switch terrain {
			case "stairs":
				column.SetBlockState(5, 64, 8, terrainState(t, "oak_stairs", map[string]string{"facing": "east", "half": "bottom", "shape": "straight"}))
				column.SetBlockState(6, 64, 8, stone)
				column.SetBlockState(7, 64, 8, stone)
				goalY = 65
			case "descent":
				column.SetBlockState(4, 64, 8, stone)
				m.self.SetPosition(4.5, 65, 8.5)
			case "crouch":
				ceiling := terrainState(t, "oak_slab", map[string]string{"type": "top"})
				for x := 4; x <= 8; x++ {
					for _, z := range []int{7, 9} {
						column.SetBlockState(x, 64, z, stone)
						column.SetBlockState(x, 65, z, stone)
					}
					if x >= 5 {
						column.SetBlockState(x, 65, 8, ceiling)
					}
				}
			}
			if err := m.NavigateTo(7.5, goalY, 8.5); err != nil {
				t.Fatal(err)
			}
			var velocity [3]float64
			for range 300 {
				if dryNavigationStep(t, m, &velocity) {
					t.Fatal("step/descent/crouch invented a jump")
				}
				if !m.IsNavigating() {
					break
				}
			}
			x, y, z := m.self.Position()
			if m.IsNavigating() || math.Hypot(x-7.5, z-8.5) > 0.12 || math.Abs(y-goalY) > 0.08 {
				t.Fatalf("terrain traversal did not arrive: (%g,%g,%g)", x, y, z)
			}
		})
	}
}

func TestChangedTerrainReplansRatherThanDrivingIntoNewWall(t *testing.T) {
	m, column := navigationTerrain(t)
	defer m.Stop()
	if err := m.NavigateTo(12.5, 64, 8.5); err != nil {
		t.Fatal(err)
	}
	var velocity [3]float64
	changed := false
	for range 300 {
		waitNavigationPlanner(t, m)
		x, _, _ := m.self.Position()
		if !changed && x > 5.5 {
			stone := blocks.DefaultStateID(blocks.BlockID("minecraft:stone"))
			column.SetBlockState(8, 64, 8, stone)
			column.SetBlockState(8, 65, 8, stone)
			changed = true
		}
		dryNavigationStep(t, m, &velocity)
		if !m.IsNavigating() {
			break
		}
	}
	x, y, z := m.self.Position()
	if !changed || m.IsNavigating() || math.Hypot(x-12.5, z-8.5) > 0.12 || math.Abs(y-64) > 0.08 {
		t.Fatalf("changed terrain recovery did not arrive: (%g,%g,%g)", x, y, z)
	}
}

// This is the live-server bed-on-blue-ice stall scene, including negative Y
// and fractional spawn coordinates. Chunks ahead arrive as the player moves.
func loadBedIceColumn(t *testing.T, m *Module, cx, cz int32) {
	t.Helper()
	column := chunks.NewColumn(cx, cz)
	column.Sections[0] = chunks.NewEmptySection()
	grass := blocks.DefaultStateID(blocks.BlockID("minecraft:grass_block"))
	ice := blocks.DefaultStateID(blocks.BlockID("minecraft:blue_ice"))
	for lx := range 16 {
		for lz := range 16 {
			x, z := int(cx)*16+lx, int(cz)*16+lz
			state := grass
			if x >= 0 && x <= 30 && z >= 19 && z <= 21 {
				state = ice
			}
			column.SetBlockState(x, -61, z, state)
		}
	}
	if cx == 1 && cz == 1 {
		column.SetBlockState(22, -60, 20, terrainState(t, "red_bed", map[string]string{"part": "foot", "facing": "south"}))
		column.SetBlockState(22, -60, 21, terrainState(t, "red_bed", map[string]string{"part": "head", "facing": "south"}))
	}
	data, err := column.EncodeSections()
	if err != nil {
		t.Fatal(err)
	}
	wire, err := jp.ToWire(&packets.S2CLevelChunkWithLight{ChunkX: ns.Int32(cx), ChunkZ: ns.Int32(cz), ChunkData: ns.ChunkData{Data: data}})
	if err != nil {
		t.Fatal(err)
	}
	m.world.HandlePacket(wire)
}

func TestBedOnIceDoesNotStallLongDistanceNavigation(t *testing.T) {
	m, _ := navigationTerrain(t)
	defer m.Stop()
	for cx := range int32(4) {
		for cz := range int32(3) {
			loadBedIceColumn(t, m, cx, cz)
		}
	}
	m.self.SetPosition(20.58, -60, 20.58)
	completed := make(chan bool, 1)
	m.OnNavigationComplete(func(reached bool) { completed <- reached })
	if err := m.NavigateTo(200.5, -60, 20.5); err != nil {
		t.Fatal(err)
	}
	var velocity [3]float64
	loadedAhead := int32(3)
	for tick := range 1800 {
		waitNavigationPlanner(t, m)
		x, _, _ := m.self.Position()
		if next := int32(math.Floor(x/16)) + 2; next > loadedAhead {
			loadedAhead++
			for cz := range int32(3) {
				loadBedIceColumn(t, m, loadedAhead, cz)
			}
		}
		dryNavigationStep(t, m, &velocity)
		x, y, z := m.self.Position()
		if tick == 250 && x < 25 {
			t.Fatalf("stalled beside bed: (%g,%g,%g)", x, y, z)
		}
		select {
		case reached := <-completed:
			if !reached || math.Hypot(x-200.5, z-20.5) > 0.12 || math.Abs(y+60) > 0.08 {
				t.Fatalf("bed/ice streaming route did not reach ultimate destination: reached=%v (%g,%g,%g)", reached, x, y, z)
			}
			return
		default:
		}
	}
	t.Fatal("bed/ice streaming route exceeded bounded travel ticks")
}

func TestReachedWaypointRejectsOutgoingBedCornerRatherThanIncomingEdge(t *testing.T) {
	m, _ := navigationTerrain(t)
	defer m.Stop()
	for cx := range int32(3) {
		for cz := range int32(3) {
			loadBedIceColumn(t, m, cx, cz)
		}
	}
	m.self.SetPosition(21.5, -60, 21.5)
	m.navigationContext = context.Background()
	m.navigating = true
	m.frontiers = make(map[[3]int]int)
	m.goalX, m.goalY, m.goalZ = 25.5, -60, 22.5
	m.segmentFinal = true
	m.path = []PathNode{{X: 21, Y: -60, Z: 21}, {X: 22, Y: -60, Z: 22}, {X: 25, Y: -60, Z: 22}}
	m.resetWaypoint()
	var velocity [3]float64
	for range 160 {
		dryNavigationStep(t, m, &velocity)
		if !m.IsNavigating() {
			break
		}
	}
	x, y, z := m.self.Position()
	if m.IsNavigating() || math.Hypot(x-25.5, z-22.5) > 0.12 || math.Abs(y+60) > 0.08 {
		t.Fatalf("reached waypoint kept targeting itself instead of recovering outgoing edge: (%g,%g,%g)", x, y, z)
	}
}
