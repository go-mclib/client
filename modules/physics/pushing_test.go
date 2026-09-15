package physics

import (
	"math"
	"testing"

	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/entities"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func TestEntityPushFloatBoundaryAndForce(t *testing.T) {
	threshold := float64(float32(0.01))
	if x, z := entityPushImpulse(math.Nextafter(threshold, 0), 0); x != 0 || z != 0 {
		t.Fatalf("sub-threshold separation pushed: %g,%g", x, z)
	}
	x, z := entityPushImpulse(threshold, 0)
	want := -(threshold / math.Sqrt(threshold)) * float64(float32(0.05))
	if x != want || z != 0 {
		t.Fatalf("Java float boundary: got %g,%g, want %g,0", x, z, want)
	}
	// absMax, not Euclidean normalization, determines the impulse.
	x, z = entityPushImpulse(0.25, -0.25)
	want = -0.5 * float64(float32(0.05))
	if x != want || z != -want {
		t.Fatalf("diagonal push = %g,%g, want %g,%g", x, z, want, -want)
	}
	x, z = entityPushImpulse(4, 0)
	if x != -float64(float32(0.05)) || z != 0 {
		t.Fatalf("distant push cap = %g,%g", x, z)
	}
}

func TestClientPushSourcesAndStrictBounds(t *testing.T) {
	box := collisions.EntityAABB(0, 0, 0, collisions.PlayerWidth, collisions.PlayerHeight)
	for _, name := range []string{"minecraft:player", "minecraft:zombie", "minecraft:shulker"} {
		e := &entities.Entity{TypeName: name, X: 0.25, Width: 0.6, Height: 1.8}
		if !entityPushSourceIntersects(e, box) {
			t.Errorf("client ticking %s did not push", name)
		}
	}
	for _, name := range []string{"minecraft:parrot", "minecraft:bat", "minecraft:armor_stand", "minecraft:vex", "minecraft:ender_dragon", "minecraft:minecart", "minecraft:experience_bottle", "minecraft:unknown"} {
		e := &entities.Entity{TypeName: name, X: 0.25, Width: 0.6, Height: 1.8}
		if entityPushSourceIntersects(e, box) {
			t.Errorf("non-client pusher %s contributed an impulse", name)
		}
	}
	e := &entities.Entity{TypeName: "minecraft:player", Width: 0.5, Height: 1.8}
	e.X = box.MaxX + e.Width/2
	if entityPushSourceIntersects(e, box) {
		t.Fatal("touching face counted as intersection")
	}
	e.X = math.Nextafter(e.X, 0)
	if !entityPushSourceIntersects(e, box) {
		t.Fatal("strictly overlapping face did not push")
	}
}

func TestBoatPushUsesExpandedBoxAndFeetGate(t *testing.T) {
	box := collisions.EntityAABB(0, 0, 0, collisions.PlayerWidth, collisions.PlayerHeight)
	boat := &entities.Entity{TypeName: "minecraft:oak_boat", X: 1.1, Width: 1.375, Height: 0.5625}
	if boat.BoundingBox().Intersects(box) || !entityPushSourceIntersects(boat, box) {
		t.Fatal("boat's 0.2F horizontal push margin was not used")
	}
	boat.Y = -0.001
	if entityPushSourceIntersects(boat, box) {
		t.Fatal("boat pushed a player whose feet were above the boat's feet")
	}
}

func TestAuthoritativeMotionReplacesPriorPush(t *testing.T) {
	m := New()
	m.self = self.New()
	pushX, pushZ := entityPushImpulse(0.25, 0)
	m.velX, m.velZ = pushX, pushZ
	wire, err := jp.ToWire(&packets.S2CSetEntityMotion{EntityId: ns.VarInt(m.self.EntityID()), Velocity: ns.LpVec3{X: 1, Y: 0, Z: 0}})
	if err != nil {
		t.Fatal(err)
	}
	m.handleEntityMotion(wire)
	if m.velX != 1 || m.velY != 0 || m.velZ != 0 {
		t.Fatalf("authoritative packet retained old push: %g,%g,%g", m.velX, m.velY, m.velZ)
	}
}
