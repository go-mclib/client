package playerlist

import (
	"testing"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// replay decodes a snapshot from its wire form into a fresh module state.
func replay(t *testing.T, m *Module, snap *packets.S2CPlayerInfoUpdate) {
	t.Helper()
	wire, err := jp.ToWire(snap)
	if err != nil {
		t.Fatal(err)
	}
	var d packets.S2CPlayerInfoUpdate
	if err := wire.ReadInto(&d); err != nil {
		t.Fatal(err)
	}
	m.applyPlayerInfoUpdate(&d)
}

func TestDisplaySnapshotReplayAndIsolation(t *testing.T) {
	m := New()
	uuid := [16]byte{1}
	m.applyPlayerInfoUpdate(&packets.S2CPlayerInfoUpdate{
		Actions: packets.PlayerInfoAddPlayer | packets.PlayerInfoUpdateDisplayName | packets.PlayerInfoUpdateListOrder | packets.PlayerInfoUpdateHat | packets.PlayerInfoUpdateListed,
		Entries: []packets.PlayerInfoEntry{{
			UUID: ns.UUID(uuid), Name: "Steve",
			Properties:  []packets.GameProfileProperty{{Name: "textures", Value: "skin"}},
			Listed:      true,
			DisplayName: ns.Some(ns.TextComponent{Text: "Knight ", Extra: []ns.TextComponent{{Text: "Steve", Color: "gold"}}}),
			ListOrder:   12, ShowHat: true,
		}},
	})
	p := m.GetAllPlayers()[0]
	if p.DisplayName == nil || *p.DisplayName != "Knight Steve" || p.ListOrder != 12 || !p.ShowHat || !p.Listed {
		t.Fatalf("retained fields = %+v", p)
	}
	*p.DisplayName = "mutated"
	p.Properties[0].Value = "mutated"
	p.Name = "mutated"
	replayed := New()
	replay(t, replayed, m.BuildSnapshot())
	p = replayed.GetPlayer(uuid)
	if p.Name != "Steve" || p.Properties[0].Value != "skin" || p.Properties[0].HasSignature || *p.DisplayName != "Knight Steve" || p.ListOrder != 12 || !p.ShowHat || !p.Listed {
		t.Fatalf("replayed fields = %+v", p)
	}
	// Clearing an optional display name must survive a later snapshot too.
	m.applyPlayerInfoUpdate(&packets.S2CPlayerInfoUpdate{Actions: packets.PlayerInfoUpdateDisplayName, Entries: []packets.PlayerInfoEntry{{UUID: ns.UUID(uuid)}}})
	replay(t, replayed, m.BuildSnapshot())
	if replayed.GetPlayer(uuid).DisplayName != nil {
		t.Fatal("cleared display name resurrected during replay")
	}
}

// Updates for unknown players are ignored; joins and updates fire their callbacks.
func TestPlayerInfoCallbacks(t *testing.T) {
	m := New()
	var joined, updated []string
	m.OnPlayerJoin(func(p *Player) { joined = append(joined, p.Name) })
	m.OnPlayerUpdate(func(p *Player) { updated = append(updated, p.Name) })
	m.applyPlayerInfoUpdate(&packets.S2CPlayerInfoUpdate{Actions: packets.PlayerInfoUpdateLatency, Entries: []packets.PlayerInfoEntry{{UUID: ns.UUID{2}, Latency: 5}}})
	if m.PlayerCount() != 0 || len(updated) != 0 {
		t.Fatal("update for an unknown player created an entry")
	}
	m.applyPlayerInfoUpdate(&packets.S2CPlayerInfoUpdate{Actions: packets.PlayerInfoAddPlayer, Entries: []packets.PlayerInfoEntry{{UUID: ns.UUID{2}, Name: "Alex"}}})
	m.applyPlayerInfoUpdate(&packets.S2CPlayerInfoUpdate{Actions: packets.PlayerInfoUpdateLatency, Entries: []packets.PlayerInfoEntry{{UUID: ns.UUID{2}, Latency: 5}}})
	if len(joined) != 1 || len(updated) != 1 || m.GetPlayer([16]byte{2}).Ping != 5 {
		t.Fatalf("joined %v updated %v player %+v", joined, updated, m.GetPlayer([16]byte{2}))
	}
}

func TestTabHeaderFooterReset(t *testing.T) {
	for _, reset := range []string{"transfer", "disconnect"} {
		t.Run(reset, func(t *testing.T) {
			c := client.New("unused", "Grace", false)
			client.NewSimulator(c).SetState(jp.StatePlay)
			m := New()
			c.Register(m)
			wire, err := jp.ToWire(&packets.S2CTabList{Header: ns.TextComponent{Text: "Welcome ", Extra: []ns.TextComponent{{Text: "hero", Color: "gold"}}}, Footer: ns.NewTextComponent("SteedsGate")})
			if err != nil {
				t.Fatal(err)
			}
			m.HandlePacket(wire)
			header, footer, known := m.TabList()
			if !known || header != "Welcome hero" || footer != "SteedsGate" {
				t.Fatalf("tab = %q, %q, %v", header, footer, known)
			}
			if reset == "transfer" {
				client.NewSimulator(c).Transfer()
			} else {
				client.NewSimulator(c).Disconnect()
			}
			if h, f, known := m.TabList(); known || h != "" || f != "" {
				t.Fatal("tab header/footer survived reset")
			}
		})
	}
}

func TestPlayerInfoRemoveRejectsBadCount(t *testing.T) {
	c := client.New("unused", "Grace", false)
	client.NewSimulator(c).SetState(jp.StatePlay)
	m := New()
	c.Register(m)
	uuid := [16]byte{9}
	left := 0
	m.OnPlayerLeave(func(*Player) { left++ })
	for _, count := range []ns.VarInt{-1, 1 << 30, 2} {
		m.players[uuid] = &Player{UUID: uuid}
		buf := ns.NewWriter()
		_ = buf.WriteVarInt(count)
		_ = buf.WriteUUID(ns.UUID(uuid))
		m.HandlePacket(&jp.WirePacket{PacketID: (&packets.S2CPlayerInfoRemove{}).ID(), Data: buf.Bytes()})
		if left != 0 || m.GetPlayer(uuid) == nil {
			t.Fatalf("count %d: malformed remove applied", count)
		}
	}
	wire, err := jp.ToWire(&packets.S2CPlayerInfoRemove{UUIDs: []ns.UUID{ns.UUID(uuid)}})
	if err != nil {
		t.Fatal(err)
	}
	m.HandlePacket(wire)
	if left != 1 || m.GetPlayer(uuid) != nil {
		t.Fatal("valid remove not applied")
	}
}
