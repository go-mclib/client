package collisions

import (
	"sync"
	"testing"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
)

func TestBorderCollisionUsesRoundedBoundsAndTickLerp(t *testing.T) {
	m := &Module{world: collisionWorld{}, border: defaultBorder()}
	m.border.setSize(10, 6, 2)
	x, _, _, horizontal, _ := m.CollideMovement(4.5, 64, 0, PlayerWidth, PlayerHeight, 1, 0, 0, false, 0)
	if x != 5-(4.5+PlayerWidth/2) || !horizontal {
		t.Fatalf("initial border clip=%v/%v", x, horizontal)
	}
	m.TickBorder()
	x, _, _, _, _ = m.CollideMovement(4.5, 64, 0, PlayerWidth, PlayerHeight, 1, 0, 0, false, 0)
	if x != 5-(4.5+PlayerWidth/2) {
		t.Fatalf("moving border did not use previous tick size: %v", x)
	}
	m.TickBorder()
	x, _, _, horizontal, _ = m.CollideMovement(2.5, 64, 0, PlayerWidth, PlayerHeight, 1, 0, 0, false, 0)
	if x != 3-(2.5+PlayerWidth/2) || !horizontal {
		t.Fatalf("completed border lerp clip=%v/%v", x, horizontal)
	}
	m.border.x = 0.25
	m.border.setSize(10, 10, 0)
	x, _, _, horizontal, _ = m.CollideMovement(5.5, 64, 0, PlayerWidth, PlayerHeight, 1, 0, 0, false, 0)
	if x != 6-(5.5+PlayerWidth/2) || !horizontal {
		t.Fatalf("fractional border not rounded outwards: %v/%v", x, horizontal)
	}
	x, _, _, horizontal, _ = m.CollideMovement(20, 64, 0, PlayerWidth, PlayerHeight, 1, 0, 0, false, 0)
	if x != 1 || horizontal {
		t.Fatal("far-outside player incorrectly clamped to border")
	}
}

func borderPacket(t *testing.T, p jp.Packet) *jp.WirePacket {
	t.Helper()
	wire, err := jp.ToWire(p)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestBorderSnapshotTracksAllBorderPackets(t *testing.T) {
	c := client.New("unused", "test", false)
	client.NewSimulator(c).SetState(jp.StatePlay)
	m := New()
	m.Init(c)
	if m.BorderSnapshot() != nil {
		t.Fatal("snapshot before border initialization")
	}
	m.HandlePacket(borderPacket(t, &packets.S2CInitializeBorder{X: 1, Z: 2, OldDiameter: 100, NewDiameter: 100, PortalTeleportBoundary: 29999984, WarningBlocks: 5, WarningTime: 15}))
	m.HandlePacket(borderPacket(t, &packets.S2CSetBorderCenter{X: 10, Z: -10}))
	m.HandlePacket(borderPacket(t, &packets.S2CSetBorderLerpSize{OldDiameter: 100, NewDiameter: 50, Speed: 10}))
	m.HandlePacket(borderPacket(t, &packets.S2CSetBorderWarningDistance{WarningBlocks: 7}))
	m.HandlePacket(borderPacket(t, &packets.S2CSetBorderWarningDelay{WarningTime: 20}))
	m.TickBorder()
	got := m.BorderSnapshot()
	want := packets.S2CInitializeBorder{X: 10, Z: -10, OldDiameter: 95, NewDiameter: 50, Speed: 9, PortalTeleportBoundary: 29999984, WarningBlocks: 7, WarningTime: 20}
	if got == nil || *got != want {
		t.Fatalf("snapshot = %+v, want %+v", got, want)
	}
	client.NewSimulator(c).Transfer()
	if m.BorderSnapshot() != nil {
		t.Fatal("border survived a transfer")
	}
}

// Run with -race: border updates from the packet/physics goroutines must not
// race with collision queries from planner goroutines.
func TestBorderUpdatesConcurrentWithQueries(t *testing.T) {
	c := client.New("unused", "test", false)
	client.NewSimulator(c).SetState(jp.StatePlay)
	m := New()
	m.Init(c)
	m.world = collisionWorld{}
	lerp := borderPacket(t, &packets.S2CSetBorderLerpSize{OldDiameter: 10, NewDiameter: 6, Speed: 1000})
	center := borderPacket(t, &packets.S2CSetBorderCenter{X: 0.25, Z: 0})
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 200 {
			m.HandlePacket(lerp)
			m.HandlePacket(center)
			m.TickBorder()
			m.SetPlayerContext(false, 0, false)
		}
	})
	wg.Go(func() {
		for range 200 {
			m.CollideMovement(2.5, 64, 0, PlayerWidth, PlayerHeight, 1, 0, 0, false, 0)
			m.NoCollision(EntityAABB(4.5, 64, 0, PlayerWidth, PlayerHeight))
			m.BorderSnapshot()
		}
	})
	wg.Wait()
}
