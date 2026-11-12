package physics

import (
	"testing"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/client/modules/world"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

func TestDismountReadsSelfVehicleAndWaitsForServerPosition(t *testing.T) {
	c := client.New("unused", "test", false)
	client.NewSimulator(c).SetState(jp.StatePlay)
	s := self.New()
	c.Register(s)
	c.Register(world.New())
	c.Register(collisions.New())
	m := New()
	c.Register(m)
	if err := m.Wire(); err != nil {
		t.Fatal(err)
	}
	passengers := func(vehicle int32, riders ...ns.VarInt) {
		wire, err := jp.ToWire(&packets.S2CSetPassengers{EntityID: ns.VarInt(vehicle), Passengers: riders})
		if err != nil {
			t.Fatal(err)
		}
		s.HandlePacket(wire)
	}
	passengers(99, ns.VarInt(s.EntityID()))
	if s.VehicleID() != 99 {
		t.Fatalf("mounted vehicle = %d", s.VehicleID())
	}
	m.positionKnown = true
	passengers(99)
	if s.VehicleID() != 0 || m.positionKnown {
		t.Fatalf("dismount: vehicle=%d positionKnown=%v", s.VehicleID(), m.positionKnown)
	}
}
