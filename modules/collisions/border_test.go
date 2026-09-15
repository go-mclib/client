package collisions

import "testing"

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
