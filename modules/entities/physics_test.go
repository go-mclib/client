package entities

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/self"
	dataentities "github.com/go-mclib/data/entities"
	"github.com/go-mclib/data/packets"
	"github.com/go-mclib/data/registries"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

func TestPhysicsPositionInterpolatesRetargetsAndSnaps(t *testing.T) {
	m := New()
	e := &Entity{ID: 1, TypeName: "minecraft:oak_boat", Width: 0.6, Height: 1.8}
	m.trackEntityLocked(e)
	e.moveTo(3, 0, 0, 0, 0, false)
	m.TickPhysics()
	got := m.PhysicsEntitiesInAABB(0.5, 0, -1, 1.5, 2, 1)
	if len(got) != 1 || got[0].X != 1 {
		t.Fatalf("first interpolation step did not reach one third: %+v", got)
	}
	if m.GetEntity(1).X != 3 {
		t.Fatal("physics interpolation changed the packet delta codec base")
	}
	e.moveTo(4, 0, 0, 0, 0, false)
	m.TickPhysics()
	got = m.PhysicsEntitiesInAABB(1.5, 0, -1, 2.5, 2, 1)
	if len(got) != 1 || got[0].X != 2 {
		t.Fatalf("retargeted interpolation did not start from client position: %+v", got)
	}
	// Duplicate updates must not restart the active interpolation.
	e.moveTo(4, 0, 0, 0, 0, false)
	m.TickPhysics()
	m.TickPhysics()
	got = m.PhysicsEntitiesInAABB(3.5, 0, -1, 4.5, 2, 1)
	if len(got) != 1 || got[0].X != 4 {
		t.Fatalf("duplicate update extended interpolation: %+v", got)
	}
	e.moveTo(100, 0, 0, 0, 0, true)
	got = m.PhysicsEntitiesInAABB(99.5, 0, -1, 100.5, 2, 1)
	if len(got) != 1 || got[0].X != 100 {
		t.Fatalf("large position sync was interpolated instead of snapped: %+v", got)
	}
}

func TestRelativePositionUsesJavaCodecRounding(t *testing.T) {
	base := -0.5 / 4096
	if got := decodeEntityDelta(base, 1); got != 1.0/4096 {
		t.Fatalf("negative half unit rounded away from zero: %g", got)
	}
	if got := decodeEntityDelta(base, 0); got != base {
		t.Fatalf("zero delta quantized an unchanged coordinate: %g", got)
	}
}

func TestSteppedMovementPreservesCollisionRouteAndVelocity(t *testing.T) {
	m := New()
	e := &Entity{ID: 1, TypeName: "minecraft:zombie", Width: 0.6, Height: 1.8, VelX: 0.5, VelY: -0.25, VelZ: 0.75}
	m.trackEntityLocked(e)
	m.handleMoveEntityPos(equipmentPacket(t, &packets.S2CMoveEntityPos{
		EntityID: 1, Delta: packets.VecDelta{Steps: []packets.VecDeltaStep{
			{Ticks: 2, X: 8192},
			{Ticks: 1, Z: 12288},
		}},
	}))
	if target := m.GetEntity(1); target.X != 2 || target.Z != 3 {
		t.Fatalf("relative steps did not accumulate into codec target: %+v", target)
	}
	for _, position := range [][2]float64{{1, 0}, {2, 0}, {2, 3}} {
		m.TickPhysics()
		got := m.PhysicsEntitiesInAABB(position[0]-0.1, 0, position[1]-0.1, position[0]+0.1, 2, position[1]+0.1)
		if len(got) != 1 || got[0].X != position[0] || got[0].Z != position[1] {
			t.Fatalf("stepped collision route skipped %v: %+v", position, got)
		}
	}
	m.handleEntityPositionSync(equipmentPacket(t, &packets.S2CEntityPositionSync{
		EntityID: 1, Path: packets.PositionPath{Steps: []packets.PositionStep{
			{X: 4, Z: 3, TickOffset: 2}, {X: 4, Z: 6, TickOffset: 1},
		}},
	}))
	m.TickPhysics()
	got := m.PhysicsEntitiesInAABB(2.9, 0, 2.9, 3.1, 2, 3.1)
	if len(got) != 1 || got[0].X != 3 || got[0].Z != 3 {
		t.Fatalf("absolute path collapsed into its endpoint: %+v", got)
	}
	m.handleEntityPositionSync(equipmentPacket(t, &packets.S2CEntityPositionSync{
		EntityID: 1, Path: packets.PositionPath{X: 100},
	}))
	if target := m.GetEntity(1); target.VelX != 0.5 || target.VelY != -0.25 || target.VelZ != 0.75 {
		t.Fatalf("position-only sync changed velocity: %+v", target)
	}
	got = m.PhysicsEntitiesInAABB(99.9, 0, -0.1, 100.1, 2, 0.1)
	if len(got) != 1 || got[0].X != 100 {
		t.Fatalf("large sync retained queued path: %+v", got)
	}
}

func TestLivingMovementQueuesTargetsAndUsesEntityInterval(t *testing.T) {
	m := New()
	e := &Entity{ID: 1, TypeName: "minecraft:player", Width: 0.6, Height: 1.8}
	m.trackEntityLocked(e)
	e.moveTo(2, 0, 0, 0, 0, false)
	m.TickPhysics()
	if got := m.PhysicsEntitiesInAABB(0.9, 0, -1, 1.1, 2, 1); len(got) != 1 || got[0].X != 1 {
		t.Fatalf("player did not use its two-tick update interval: %+v", got)
	}
	e.moveTo(4, 0, 0, 0, 0, false)
	m.TickPhysics()
	if got := m.PhysicsEntitiesInAABB(1.9, 0, -1, 2.1, 2, 1); len(got) != 1 || got[0].X != 2 {
		t.Fatalf("new target replaced unfinished queued segment: %+v", got)
	}
}

func TestSwingAnimationDispatchIsSeparateFromCriticalAnimation(t *testing.T) {
	c := client.New("unused", "test", false)
	client.NewSimulator(c).SetState(jp.StatePlay)
	m := New()
	c.Register(m)
	var swing [4]int32
	var animate uint8 = 255
	m.OnSwingAnimation(func(entityID, hand, kind, duration int32) { swing = [4]int32{entityID, hand, kind, duration} })
	m.OnEntityAnimation(func(_ int32, kind uint8) { animate = kind })
	m.HandlePacket(equipmentPacket(t, &packets.S2CSwingAnimation{EntityID: 7, Hand: 1, Type: 2, Duration: 0}))
	if swing != [4]int32{7, 1, 2, 0} || animate != 255 {
		t.Fatalf("swing hand/type/zero duration lost or treated as Animate: %v %d", swing, animate)
	}
	m.HandlePacket(equipmentPacket(t, &packets.S2CAnimate{EntityID: 7, Animation: 1}))
	if animate != 1 || swing != [4]int32{7, 1, 2, 0} {
		t.Fatal("critical animation overwrote swing event")
	}
}

func TestPushTeamsAndNestedPassengers(t *testing.T) {
	m := New()
	m.client = &client.Client{Username: "local"}
	m.self = self.New()
	e := &Entity{ID: 42, TypeName: "minecraft:zombie", scoreName: "other"}
	if !m.CanPushLocal(e) {
		t.Fatal("unaffiliated walking entities cannot push")
	}
	team := func(method int8, rule int32, players ...ns.String) {
		packet := &packets.S2CSetPlayerTeam{TeamName: "a", Method: ns.Int8(method), Players: ns.PrefixedArray[ns.String](players)}
		if method == 0 || method == 2 {
			packet.Parameters = &packets.PlayerTeamParameters{CollisionRule: ns.VarInt(rule)}
		}
		m.handleSetPlayerTeam(equipmentPacket(t, packet))
	}
	team(0, 1, "local")
	if m.CanPushLocal(e) {
		t.Fatal("collision=never still permits a push")
	}
	team(2, 2)
	if m.CanPushLocal(e) {
		t.Fatal("pushOtherTeams did not suppress non-allied collision")
	}
	team(3, 0, "other")
	if !m.CanPushLocal(e) {
		t.Fatal("allied pair excluded by pushOtherTeams")
	}
	team(2, 3)
	if m.CanPushLocal(e) {
		t.Fatal("pushOwnTeam did not suppress allied collision")
	}
	team(1, 0)
	if !m.CanPushLocal(e) {
		t.Fatal("removed team continued suppressing pushes")
	}
	m.handleSetPassengers(equipmentPacket(t, &packets.S2CSetPassengers{EntityID: 7, Passengers: []ns.VarInt{ns.VarInt(m.self.EntityID()), 8}}))
	m.handleSetPassengers(equipmentPacket(t, &packets.S2CSetPassengers{EntityID: 8, Passengers: []ns.VarInt{42}}))
	if m.CanPushLocal(e) {
		t.Fatal("indirect passengers of the same vehicle push each other")
	}
	m.handleSetPassengers(equipmentPacket(t, &packets.S2CSetPassengers{EntityID: 8}))
	if !m.CanPushLocal(e) {
		t.Fatal("dismount did not restore pushing")
	}
}

func TestRigidEntityCollisionsExcludeLivingPushers(t *testing.T) {
	m := New()
	m.trackEntityLocked(&Entity{ID: 1, TypeName: "minecraft:player", Width: 0.6, Height: 1.8})
	m.trackEntityLocked(&Entity{ID: 2, TypeName: "minecraft:zombie", Width: 0.6, Height: 1.95})
	if boxes := m.CollisionBoxes(-2, -1, -2, 2, 3, 2); len(boxes) != 0 {
		t.Fatalf("living pushers became solid movement obstacles: %v", boxes)
	}
	m.trackEntityLocked(&Entity{ID: 3, TypeName: "minecraft:oak_boat", Width: 1.375, Height: 0.5625})
	boxes := m.CollisionBoxes(-2, -1, -2, 2, 3, 2)
	if len(boxes) != 1 || boxes[0].MaxY != 0.5625 {
		t.Fatalf("boat collider missing or wrong: %v", boxes)
	}
	m.vehicles[3], m.vehicles[-1] = 7, 7
	if boxes := m.CollisionBoxes(-2, -1, -2, 2, 3, 2); len(boxes) != 0 {
		t.Fatalf("shared vehicle collider remained: %v", boxes)
	}
}

func TestPoseAndShulkerMetadataChangeCollisionBounds(t *testing.T) {
	player := &Entity{TypeName: "minecraft:player", Width: 0.6, Height: 1.8}
	player.Metadata.Set(dataentities.EntityIndexPose, 20, []byte{5})
	if box := player.BoundingBox(); box.MaxY != 1.5 {
		t.Fatalf("crouching collision height = %g", box.MaxY)
	}
	m := New()
	shulker := &Entity{ID: 1, TypeName: "minecraft:shulker", Width: 1, Height: 1}
	shulker.Metadata.Set(16, 12, []byte{4}) // attached west, opening east
	shulker.Metadata.Set(17, 0, []byte{100})
	m.trackEntityLocked(shulker)
	if boxes := m.CollisionBoxes(0.75, 0, -0.25, 1.25, 1, 0.25); len(boxes) != 0 {
		t.Fatal("closed shulker occupies open-lid volume")
	}
	for range 20 {
		m.TickPhysics()
	}
	boxes := m.CollisionBoxes(0.75, 0, -0.25, 1.25, 1, 0.25)
	if len(boxes) != 1 || boxes[0].MaxX != 1.5 || boxes[0].MinX != -0.5 {
		t.Fatalf("opening shulker lid did not extend east: %v", boxes)
	}
	health := make([]byte, 4)
	binary.BigEndian.PutUint32(health, math.Float32bits(0))
	shulker.Metadata.Set(dataentities.LivingEntityIndexHealth, 3, health)
	if boxes := m.CollisionBoxes(-2, -1, -2, 2, 3, 2); len(boxes) != 0 {
		t.Fatal("dead shulker remained a rigid collider")
	}
}

func TestRemoteScaleAttributesChangeContactGeometry(t *testing.T) {
	m := New()
	e := &Entity{ID: 42, TypeName: "minecraft:player", Width: float64(float32(0.6)), Height: float64(float32(1.8))}
	m.trackEntityLocked(e)
	if got := m.PhysicsEntitiesInAABB(0.5, 0, -1, 0.75, 2, 1); len(got) != 0 {
		t.Fatal("unscaled player intersects distant query")
	}
	m.handleUpdateAttributes(equipmentPacket(t, &packets.S2CUpdateAttributes{
		EntityID: 42,
		Attributes: []packets.AttributeSnapshot{{
			Type: ns.VarInt(registries.Attribute.Get("minecraft:scale")),
			Base: 1,
			Modifiers: []packets.AttributeModifier{
				{ID: "test:add", Amount: 1, Operation: 0},
				{ID: "test:base", Amount: 0.5, Operation: 1},
				{ID: "test:total", Amount: 1, Operation: 2},
			},
		}},
	}))
	got := m.PhysicsEntitiesInAABB(0.5, 0, -1, 0.75, 2, 1)
	if len(got) != 1 || got[0].BoundingBox().MaxX != float64(float32(0.6)*float32(6))/2 {
		t.Fatalf("scale attribute modifiers did not change client contact box: %v", got)
	}
	// Sleeping uses fixed dimensions even when scale was synchronized.
	e.Metadata.Set(dataentities.EntityIndexPose, 20, []byte{2})
	if got := m.PhysicsEntitiesInAABB(0.5, 0, -1, 0.75, 2, 1); len(got) != 0 {
		t.Fatal("sleeping box inherited the standing scale")
	}
}

func TestCurrentCubeMetadataSizeIsIndependentOfAgeLock(t *testing.T) {
	e := &Entity{TypeName: "minecraft:slime", Width: float64(float32(0.52)), Height: float64(float32(0.52))}
	// 26.2 inserts both AgeableMob fields before AbstractCubeMob's size.
	e.Metadata.Set(16, 8, []byte{1})
	e.Metadata.Set(17, 8, []byte{1})
	e.Metadata.Set(18, 1, []byte{4})
	box := e.BoundingBox()
	if box.MaxX != float64(float32(0.52)*float32(4))/2 || box.MaxY != float64(float32(0.52)*float32(4)) {
		t.Fatalf("cube used the baby/age-lock flag as size: %v", box)
	}
}

func TestHappyGhastStillStateAndBabyCollision(t *testing.T) {
	m := New()
	e := &Entity{ID: 1, TypeName: "minecraft:happy_ghast", Width: 4, Height: 4}
	m.trackEntityLocked(e)
	if boxes := m.CollisionBoxes(-1, -1, -1, 1, 1, 1); len(boxes) != 0 {
		t.Fatal("moving happy ghast is rigid at the player's side")
	}
	e.Metadata.Set(19, 8, []byte{1}) // STAYS_STILL, after both AgeableMob fields
	if boxes := m.CollisionBoxes(-1, -1, -1, 1, 1, 1); len(boxes) != 1 {
		t.Fatal("stationary adult happy ghast lacks its rigid collider")
	}
	e.Metadata.Set(16, 8, []byte{1})
	if boxes := m.CollisionBoxes(-1, -1, -1, 1, 1, 1); len(boxes) != 0 {
		t.Fatal("baby happy ghast retained the adult rigid collider")
	}
	if box := e.BoundingBox(); box.MaxY != float64(float32(4)*float32(0.2375)) {
		t.Fatalf("baby happy ghast push dimensions = %v", box)
	}
}

func TestPositionBacklogIsBoundedWithoutPhysicsTicks(t *testing.T) {
	e := &Entity{TypeName: "minecraft:zombie"}
	for i := range 10 * maxPositionSteps {
		e.moveTo(float64(i+1), 64, 0, 0, 0, false)
		if len(e.positionSteps) > maxPositionSteps {
			t.Fatalf("backlog grew to %d steps", len(e.positionSteps))
		}
	}
	e = &Entity{TypeName: "minecraft:zombie"}
	for i := range maxPositionSteps + 1 {
		e.moveTo(float64(i+1), 64, 0, 0, 0, false)
	}
	if e.physicsX != e.X || len(e.positionSteps) != 0 {
		t.Fatalf("overflow did not snap to the target: physics=%v target=%v steps=%d", e.physicsX, e.X, len(e.positionSteps))
	}
}
