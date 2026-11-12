package protocol

import (
	"slices"
	"testing"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

func TestPostEffectsReplaceAcrossConfigurationAndPlay(t *testing.T) {
	c := client.New("unused", "test", false)
	m := New()
	c.Register(m)
	sim := client.NewSimulator(c)
	sim.Connect(nil, jp.StateConfiguration)
	var observed []ns.Identifier
	m.OnPostEffects(func(effects []ns.Identifier) {
		observed = slices.Clone(effects)
		if len(effects) > 0 {
			effects[0] = "test:callback_mutation"
		}
	})
	deliver := func(state jp.State, packet jp.Packet) {
		t.Helper()
		sim.SetState(state)
		wire, err := jp.ToWire(packet)
		if err != nil {
			t.Fatal(err)
		}
		m.HandlePacket(wire)
	}
	deliver(jp.StateConfiguration, &packets.S2CPostEffectsConfiguration{Effects: []ns.Identifier{"test:first", "test:second"}})
	if !slices.Equal(m.PostEffects(), []ns.Identifier{"test:first", "test:second"}) {
		t.Fatal("configuration effects lost or callback mutated active list")
	}
	snapshot := m.PostEffects()
	snapshot[0] = "test:snapshot_mutation"
	if !slices.Equal(m.PostEffects(), []ns.Identifier{"test:first", "test:second"}) {
		t.Fatal("snapshot mutation changed active effects")
	}
	deliver(jp.StatePlay, &packets.S2CPostEffectsPlay{Effects: []ns.Identifier{"test:replacement"}})
	if !slices.Equal(m.PostEffects(), []ns.Identifier{"test:replacement"}) || !slices.Equal(observed, []ns.Identifier{"test:replacement"}) {
		t.Fatal("play effects appended instead of replacing configuration list")
	}
	deliver(jp.StatePlay, &packets.S2CPostEffectsPlay{})
	if len(m.PostEffects()) != 0 || len(observed) != 0 {
		t.Fatal("empty effects did not clear state and observers")
	}
	deliver(jp.StatePlay, &packets.S2CPostEffectsPlay{Effects: []ns.Identifier{"test:reset"}})
	m.Reset()
	if len(m.PostEffects()) != 0 || len(observed) != 0 {
		t.Fatal("effects leaked into the next connection")
	}
}
