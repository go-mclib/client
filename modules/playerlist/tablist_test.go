package playerlist

import (
	"testing"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func TestDisplaySnapshotReplayAndIsolation(t *testing.T) {
	m := New()
	uuid := [16]byte{1}
	buf := ns.NewWriter()
	_ = buf.WriteByte(actionAddPlayer | actionUpdateDisplay | actionUpdateListOrder | actionUpdateHat | actionUpdateListed)
	_ = buf.WriteVarInt(1)
	_ = buf.WriteUUID(ns.UUID(uuid))
	_ = buf.WriteString("Steve")
	_ = buf.WriteVarInt(1)
	_ = buf.WriteString("textures")
	_ = buf.WriteString("skin")
	_ = buf.WriteBool(false)
	_ = buf.WriteBool(true)
	_ = buf.WriteBool(true)
	_ = buf.WriteTextComponent(ns.TextComponent{Text: "Knight ", Extra: []ns.TextComponent{{Text: "Steve", Color: "gold"}}})
	_ = buf.WriteVarInt(12)
	_ = buf.WriteBool(true)
	m.handlePlayerInfoUpdate(&jp.WirePacket{Data: buf.Bytes()})
	p := m.GetAllPlayers()[0]
	if p.DisplayName == nil || *p.DisplayName != "Knight Steve" || p.ListOrder != 12 || !p.ShowHat || !p.Listed {
		t.Fatalf("retained fields = %+v", p)
	}
	*p.DisplayName = "mutated"
	p.Properties[0].Value = "mutated"
	p.Name = "mutated"
	replay := New()
	wire, err := jp.ToWire(m.BuildSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	replay.handlePlayerInfoUpdate(wire)
	p = replay.GetPlayer(uuid)
	if p.Name != "Steve" || p.Properties[0].Value != "skin" || *p.DisplayName != "Knight Steve" || p.ListOrder != 12 || !p.ShowHat || !p.Listed {
		t.Fatalf("replayed fields = %+v", p)
	}
	// Clearing an optional display name must survive a later snapshot too.
	buf = ns.NewWriter()
	_ = buf.WriteByte(actionUpdateDisplay)
	_ = buf.WriteVarInt(1)
	_ = buf.WriteUUID(ns.UUID(uuid))
	_ = buf.WriteBool(false)
	m.handlePlayerInfoUpdate(&jp.WirePacket{Data: buf.Bytes()})
	wire, err = jp.ToWire(m.BuildSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	replay.handlePlayerInfoUpdate(wire)
	if replay.GetPlayer(uuid).DisplayName != nil {
		t.Fatal("cleared display name resurrected during replay")
	}
}

func TestTabHeaderFooterReplayAndReset(t *testing.T) {
	for _, reset := range []string{"transfer", "disconnect"} {
		t.Run(reset, func(t *testing.T) {
			c := client.New("unused", "Grace", false)
			c.SetState(jp.StatePlay)
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
			snapshot := m.BuildTabSnapshot()
			snapshot.Header.Extra[0].Text = "mutated"
			if m.BuildTabSnapshot().Header.Extra[0].Text != "hero" {
				t.Fatal("tab replay snapshot aliases stored data")
			}
			if reset == "transfer" {
				c.FireTransfer()
			} else {
				c.FireDisconnect()
			}
			if h, f, known := m.TabList(); known || h != "" || f != "" || m.BuildTabSnapshot() != nil {
				t.Fatal("tab header/footer survived reset")
			}
		})
	}
}
