package playerlist_test

import (
	"testing"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/playerlist"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// feeding a player_info_update with a textures property and rebuilding the
// snapshot must preserve the property (skin) so a proxy can replay it.
func TestSnapshotPreservesSkin(t *testing.T) {
	uuid := ns.UUID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

	// crafted wire body: add_player | update_game_mode | update_listed | update_latency
	in := ns.NewWriter()
	_ = in.WriteByte(byte(packets.PlayerInfoAddPlayer | packets.PlayerInfoUpdateListed | packets.PlayerInfoUpdateGameMode | packets.PlayerInfoUpdateLatency))
	_ = in.WriteVarInt(1)
	_ = in.WriteUUID(uuid)
	_ = in.WriteString("Steve")
	_ = in.WriteVarInt(1) // one property
	_ = in.WriteString("textures")
	_ = in.WriteString("base64-skin-blob")
	_ = in.WriteBool(true)
	_ = in.WriteString("mojang-signature")
	_ = in.WriteVarInt(1)  // gamemode = creative
	_ = in.WriteBool(true) // listed
	_ = in.WriteVarInt(42) // latency

	c := client.New("unused", "Grace", false)
	client.NewSimulator(c).SetState(jp.StatePlay)
	m := playerlist.New()
	c.Register(m)
	m.HandlePacket(&jp.WirePacket{PacketID: (&packets.S2CPlayerInfoUpdate{}).ID(), Data: in.Bytes()})

	p := m.GetPlayer([16]byte(uuid))
	if p == nil || len(p.Properties) != 1 || p.Properties[0].Value != "base64-skin-blob" || p.Gamemode != 1 || !p.Listed || p.Ping != 42 {
		t.Fatalf("entry not captured: %+v", p)
	}

	snap := m.BuildSnapshot()
	if snap == nil {
		t.Fatal("nil snapshot")
	}
	wire, err := jp.ToWire(snap)
	if err != nil {
		t.Fatalf("ToWire: %v", err)
	}
	var out packets.S2CPlayerInfoUpdate
	if err := wire.ReadInto(&out); err != nil {
		t.Fatal(err)
	}
	if !out.Actions.Has(packets.PlayerInfoAddPlayer) || len(out.Entries) != 1 {
		t.Fatalf("snapshot = %+v", out)
	}
	e := out.Entries[0]
	sig, hasSig := e.Properties[0].Signature.Get()
	if e.UUID != uuid || e.Name != "Steve" || len(e.Properties) != 1 || e.Properties[0].Name != "textures" ||
		e.Properties[0].Value != "base64-skin-blob" || !hasSig || sig != "mojang-signature" {
		t.Fatalf("texture property not preserved: %+v", e)
	}
}
