package playerlist_test

import (
	"bytes"
	"testing"

	"github.com/go-mclib/client/modules/playerlist"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

// feeding a player_info_update with a textures property and rebuilding the
// snapshot must preserve the property (skin) so a proxy can replay it.
func TestSnapshotPreservesSkin(t *testing.T) {
	uuid := ns.UUID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

	in := ns.NewWriter()
	_ = in.WriteByte(playerlist.ActionAddPlayer | playerlist.ActionUpdateListed | playerlist.ActionUpdateGameMode | playerlist.ActionUpdateLatency)
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

	m := playerlist.New()
	m.HandlePlayerInfoUpdate(&jp.WirePacket{Data: ns.ByteArray(in.Bytes())})

	p := m.GetPlayer([16]byte(uuid))
	if p == nil || len(p.Properties) != 1 || p.Properties[0].Value != "base64-skin-blob" {
		t.Fatalf("property not captured: %+v", p)
	}

	snap := m.BuildSnapshot()
	if snap == nil {
		t.Fatal("nil snapshot")
	}

	// the wire body must be inline (no VarInt length prefix) — otherwise the
	// client reads the prefix as the actions bitset and crashes
	wire, err := jp.ToWire(snap)
	if err != nil {
		t.Fatalf("ToWire: %v", err)
	}
	if !bytes.Equal(wire.Data, snap.Data) {
		t.Fatalf("wire body has an unexpected prefix:\n in: %x\nout: %x", snap.Data, wire.Data)
	}

	// re-parse the snapshot and confirm the skin survived
	out := ns.NewReader(snap.Data)
	actions, _ := out.ReadByte()
	if actions&playerlist.ActionAddPlayer == 0 {
		t.Fatalf("snapshot missing add_player action: %#x", actions)
	}
	count, _ := out.ReadVarInt()
	if count != 1 {
		t.Fatalf("want 1 player, got %d", count)
	}
	gotUUID, _ := out.ReadUUID()
	if gotUUID != uuid {
		t.Fatalf("uuid mismatch")
	}
	name, _ := out.ReadString(16)
	propCount, _ := out.ReadVarInt()
	if name != "Steve" || propCount != 1 {
		t.Fatalf("name/propCount wrong: %q %d", name, propCount)
	}
	pName, _ := out.ReadString(64)
	pValue, _ := out.ReadString(32767)
	hasSig, _ := out.ReadBool()
	if pName != "textures" || pValue != "base64-skin-blob" || !hasSig {
		t.Fatalf("texture property not preserved: %q %q %v", pName, pValue, hasSig)
	}
	sig, _ := out.ReadString(1024)
	if sig != "mojang-signature" {
		t.Fatalf("signature not preserved: %q", sig)
	}
}
