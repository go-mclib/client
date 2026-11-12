package self

import (
	"testing"

	"github.com/go-mclib/data/packets"
)

func TestEyeHeightFollowsPoseAndScale(t *testing.T) {
	m := New()
	if got := m.CurrentEyeHeight(); got != float64(float32(EyeHeight)) {
		t.Fatalf("standing eye height = %v", got)
	}
	m.SetSneaking(true)
	if got := m.CurrentEyeHeight(); got != float64(float32(1.27)) {
		t.Fatalf("sneaking fallback eye height = %v", got)
	}
	m.attributes["minecraft:scale"] = Attribute(packets.AttributeSnapshot{Base: 100})
	if got := m.CurrentEyeHeight(); got != float64(float32(1.27)*16) {
		t.Fatalf("scale was not clamped: %v", got)
	}
	m.SetEyeHeight(0.4)
	m.SetPosition(1, 64, 2)
	if x, y, z := m.EyePosition(); x != 1 || y != 64.4 || z != 2 {
		t.Fatalf("published eye position = %v %v %v", x, y, z)
	}
}
