package physics

import (
	"math"
	"testing"

	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

func TestServerVelocityLengthIsCapped(t *testing.T) {
	m := New()
	m.self = self.New()
	// knockback 255 with a huge attack_knockback, near LpVec3's 1.7e10 limit
	wire, err := jp.ToWire(&packets.S2CSetEntityMotion{EntityID: ns.VarInt(m.self.EntityID()), Velocity: ns.LpVec3{X: 1.7e10, Y: 4e9, Z: -9e9}})
	if err != nil {
		t.Fatal(err)
	}
	m.handleEntityMotion(wire)
	length := math.Sqrt(m.velX*m.velX + m.velY*m.velY + m.velZ*m.velZ)
	if math.Abs(length-maxServerVelocity) > 1e-9 || m.velX <= 0 || m.velY <= 0 || m.velZ >= 0 {
		t.Fatalf("capped velocity = %g,%g,%g (length %g)", m.velX, m.velY, m.velZ, length)
	}

	m.setVelocity(3.9, -1, 0.5)
	if m.velX != 3.9 || m.velY != -1 || m.velZ != 0.5 {
		t.Fatalf("in-range velocity changed: %g,%g,%g", m.velX, m.velY, m.velZ)
	}
}

func TestTeleportDropsNonFiniteVelocity(t *testing.T) {
	m := New()
	m.self = self.New()
	m.velX, m.velY, m.velZ = 0.1, 0.2, 0.3
	m.handleTeleport(&packets.S2CPlayerPosition{VelocityX: ns.Float64(math.NaN())}, 0, 0)
	if m.velX != 0.1 || m.velY != 0.2 || m.velZ != 0.3 {
		t.Fatalf("non-finite teleport velocity applied: %g,%g,%g", m.velX, m.velY, m.velZ)
	}
	m.handleTeleport(&packets.S2CPlayerPosition{VelocityX: 1e12}, 0, 0)
	if m.velX != maxServerVelocity || m.velY != 0 || m.velZ != 0 {
		t.Fatalf("teleport velocity not capped: %g,%g,%g", m.velX, m.velY, m.velZ)
	}
}
