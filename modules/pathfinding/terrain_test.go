package pathfinding

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/physics"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/client/modules/world"
	"github.com/go-mclib/data/blocks"
	"github.com/go-mclib/data/chunks"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func navigationTerrain(t *testing.T) (*Module, *chunks.ChunkColumn) {
	t.Helper()
	c := client.New("", "navigation-test", false)
	c.SetState(jp.StatePlay)
	m := New()
	m.client = c
	m.self, m.world, m.collisions, m.physics = self.New(), world.New(), collisions.New(), physics.New()
	c.Register(m.self)
	c.Register(m.world)
	c.Register(m.collisions)
	c.Register(m.physics)
	if err := m.collisions.Wire(); err != nil {
		t.Fatal(err)
	}
	if err := m.physics.Wire(); err != nil {
		t.Fatal(err)
	}
	if err := m.Wire(); err != nil {
		t.Fatal(err)
	}
	column := chunks.NewColumn(0, 0)
	column.Sections[7] = chunks.NewEmptySection()
	column.Sections[8] = chunks.NewEmptySection()
	stone := blocks.DefaultStateID(blocks.BlockID("minecraft:stone"))
	for x := range 16 {
		for z := range 16 {
			column.SetBlockState(x, 63, z, stone)
		}
	}
	data, err := column.EncodeSections()
	if err != nil {
		t.Fatal(err)
	}
	packet, err := jp.ToWire(&packets.S2CLevelChunkWithLight{ChunkData: ns.ChunkData{Data: data}})
	if err != nil {
		t.Fatal(err)
	}
	m.world.HandlePacket(packet)
	column = m.world.GetChunk(0, 0)
	if column == nil {
		t.Fatal("terrain fixture chunk was not loaded")
	}
	m.self.SetPosition(4.5, 64, 8.5)
	return m, column
}

func terrainState(t *testing.T, name string, overrides map[string]string) int32 {
	t.Helper()
	id := blocks.BlockID("minecraft:" + name)
	_, props := blocks.StateProperties(int(blocks.DefaultStateID(id)))
	for key, value := range overrides {
		props[key] = value
	}
	state := blocks.StateID(int(id), props)
	if state < 0 {
		t.Fatalf("invalid fixture state %s %v", name, props)
	}
	return state
}

func TestStairWalkingUsesSuccessiveCollisionSteps(t *testing.T) {
	m, column := navigationTerrain(t)
	cfg := m.simulationConfig()
	column.SetBlockState(5, 64, 8, terrainState(t, "oak_stairs", map[string]string{"facing": "east", "half": "bottom", "shape": "straight"}))
	if !canPassBetween(m.world, m.collisions, 4, 8, 5, 8, 64, 65, playerHeight, cfg) {
		t.Fatal("two half-height stair risers were treated as a full-block wall")
	}
	path, err := m.FindPath(5.5, 65, 8.5)
	if err != nil {
		t.Fatalf("stair goal was unreachable: %v", err)
	}
	for _, node := range path {
		if node.Jump {
			t.Fatal("stair path required a jump instead of automatic steps")
		}
	}
	column.SetBlockState(5, 64, 8, blocks.DefaultStateID(blocks.BlockID("minecraft:stone")))
	if canPassBetween(m.world, m.collisions, 4, 8, 5, 8, 64, 65, playerHeight, cfg) {
		t.Fatal("a full block was invented as an automatic step")
	}
}

func TestFenceAndClosedGateDoNotBecomeGoalShortcuts(t *testing.T) {
	for _, name := range []string{"oak_fence", "cobblestone_wall", "oak_fence_gate"} {
		t.Run(name, func(t *testing.T) {
			m, column := navigationTerrain(t)
			cfg := m.simulationConfig()
			column.SetBlockState(5, 64, 8, terrainState(t, name, nil))
			open := &nodeHeap{}
			tryMove(m.world, m.collisions, nil, &PathNode{X: 4, Y: 64, Z: 8}, 64, 5, 65, 8, 5, 65, 8, map[[3]int]float64{}, open, cfg)
			if len(*open) != 0 {
				t.Fatal("goal bypassed tall collision geometry")
			}
			if canPassBetween(m.world, m.collisions, 4, 8, 5, 8, 64, 64, playerHeight, cfg) {
				t.Fatal("walk passed through a closed barrier")
			}
		})
	}
}

func TestOpenGateAllowsTraversal(t *testing.T) {
	m, column := navigationTerrain(t)
	cfg := m.simulationConfig()
	column.SetBlockState(5, 64, 8, terrainState(t, "oak_fence_gate", map[string]string{"open": "true"}))
	if !canPassBetween(m.world, m.collisions, 4, 8, 5, 8, 64, 64, playerHeight, cfg) {
		t.Fatal("open gate retained a collision barrier")
	}
}

func TestJumpRejectsUnknownSweptTerrainAndCancelledContext(t *testing.T) {
	m, _ := navigationTerrain(t)
	cfg := m.simulationConfig()
	if jumpTerrain(m.world, 14.5, 64, 8.5, 17.5, 64, 8.5, cfg) {
		t.Fatal("jump treated an unloaded swept chunk as air")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := simulateOneJump(ctx, m.collisions, m.world, 4.5, 64, 8.5, 270, cfg); ok {
		t.Fatal("cancelled trajectory produced a landing")
	}
}

func TestFailedEdgeIsExcludedFromRecoveryRoute(t *testing.T) {
	m, _ := navigationTerrain(t)
	defer m.Stop()
	m.navigationContext = context.Background()
	m.navigating = true
	m.path = []PathNode{{X: 4, Y: 64, Z: 8}, {X: 5, Y: 64, Z: 8}}
	m.pathIndex = 1
	m.goalX, m.goalY, m.goalZ = 7.5, 64, 8.5
	m.bestWaypointDistance = math.Inf(1)
	m.physics.SetInput(1, 0, false)
	m.mu.Lock()
	for range 43 {
		m.detectStuck(1)
	}
	cfg := m.simulationConfig()
	cfg.blockedEdges = m.blockedEdges
	m.mu.Unlock()
	forward, strafe, jump := m.physics.Input()
	if forward != 0 || strafe != 0 || jump {
		t.Fatal("failed edge retained movement input")
	}
	path, err := findPath(context.Background(), m.world, m.collisions, nil, 4, 64, 8, 7, 64, 8, 1000, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(path); i++ {
		a, b := path[i-1], path[i]
		if a.X == 4 && a.Z == 8 && b.X == 5 && b.Z == 8 {
			t.Fatal("recovery repeated the rejected edge")
		}
	}
}

func TestNavigationCancellationClearsInputWithoutAnotherPhysicsTick(t *testing.T) {
	m, _ := navigationTerrain(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan bool, 1)
	m.OnNavigationComplete(func(reached bool) { completed <- reached })
	if err := m.NavigateToContext(ctx, 5.5, 64, 8.5); err != nil {
		t.Fatal(err)
	}
	m.physics.SetInput(1, 0, true)
	cancel()
	select {
	case reached := <-completed:
		forward, strafe, jump := m.physics.Input()
		if reached || forward != 0 || strafe != 0 || jump {
			t.Fatal("cancelled operation left physical input active")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation waited for another physics tick")
	}
}

func TestTerrainUsesConfiguredStepHeightAndScaledBody(t *testing.T) {
	m, column := navigationTerrain(t)
	cfg := m.simulationConfig()
	stone := blocks.DefaultStateID(blocks.BlockID("minecraft:stone"))
	column.SetBlockState(5, 64, 8, stone)
	cfg.maxStep = 1
	if !canPassBetween(m.world, m.collisions, 4, 8, 5, 8, 64, 65, cfg.height, cfg) {
		t.Fatal("tracked one-block step height was ignored")
	}
	column.SetBlockState(4, 65, 8, stone)
	if canStandAt(m.world, m.collisions, 4, 64, 8, cfg) {
		t.Fatal("full-size body fit through a one-block-high opening")
	}
	cfg.width = float64(float32(playerWidth) * float32(0.5))
	cfg.height = float64(float32(playerHeight) * float32(0.5))
	cfg.sneakingHeight = float64(float32(playerSneakingHeight) * float32(0.5))
	if !canStandAt(m.world, m.collisions, 4, 64, 8, cfg) {
		t.Fatal("scaled player was rejected from an opening its body fits")
	}
}
