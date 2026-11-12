package pathfinding

import (
	"context"
	"errors"
	"testing"

	"github.com/go-mclib/client/modules/physics"
	"github.com/go-mclib/client/modules/self"
)

func TestFindPathRejectsUnknownTerrainEvenAtCurrentPosition(t *testing.T) {
	m, _ := navigationTerrain(t)
	m.world.Reset()
	m.self.SetPosition(0.5, 64, 0.5)
	if _, err := m.FindPath(0.5, 64, 0.5); err == nil {
		t.Fatalf("unknown terrain accepted as reachable: %v", err)
	}
}

func TestStopClearsMotionAndInvalidatesNavigation(t *testing.T) {
	m := New()
	m.self = self.New()
	m.physics = physics.New()
	m.navigating = true
	m.path = []Waypoint{{X: 10, Y: 64, Z: 10}}
	m.physics.SetInput(1, 0, true)
	m.Stop()
	m.navigationTick()
	forward, strafe, jump := m.physics.Input()
	if forward != 0 || strafe != 0 || jump || m.IsNavigating() {
		t.Fatal("stopped navigation kept driving physical input")
	}
}

func TestCancelledSearchStopsBeforeReadingTerrain(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := New().FindPathContext(ctx, 0, 64, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled path planning continued: %v", err)
	}
}
