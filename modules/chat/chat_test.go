package chat

import (
	"crypto/rand"
	"crypto/rsa"
	"net"
	"strings"
	"testing"

	"github.com/go-mclib/client/modules/chat/internal/signing"
	"github.com/go-mclib/data/packetids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

func TestSendMessageValidatesLikeVanilla(t *testing.T) {
	m, remote := commandSocket(t)
	for _, message := range []string{
		"color §ccode", "tab\there", "line\nbreak", "del\x7f", "nul\x00",
		"zero\u200bwidth", "sep\u2028", "bad\xffutf8",
		strings.Repeat("a", 257),
		strings.Repeat("😀", 129), // 258 UTF-16 units, 129 runes
	} {
		if err := m.SendMessage(message); err == nil {
			t.Fatalf("illegal chat message accepted: %q", message)
		}
	}
	for _, command := range []string{"say §chi", "say hi\x7f"} {
		if err := m.SendCommand(command); err == nil {
			t.Fatalf("illegal command accepted: %q", command)
		}
	}
	if _, err := m.SuggestCommandContext(t.Context(), nil, "/say §"); err == nil {
		t.Fatal("illegal suggestion input accepted")
	}

	// 128 surrogate pairs are exactly 256 UTF-16 units: the vanilla limit.
	allowed := strings.Repeat("😀", 128)
	if err := m.SendMessage(allowed); err != nil {
		t.Fatalf("message at the UTF-16 limit rejected: %v", err)
	}
	wire, err := jp.ReadWirePacketFrom(remote, -1)
	if err != nil {
		t.Fatal(err)
	}
	var packet packets.C2SChat
	if wire.PacketID != ns.VarInt(packetids.C2SChatID) || wire.ReadInto(&packet) != nil || string(packet.Message) != allowed {
		t.Fatalf("unexpected chat packet %d: %q", wire.PacketID, packet.Message)
	}
}

func TestChatSessionUpdateRestartsSigningChain(t *testing.T) {
	m, remote := commandSocket(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer := signing.NewSigner()
	signer.SetPrivateKey(key)
	signer.X509PublicKey = []byte{1}
	m.signer = signer

	if err := m.SendChatSessionData(); err != nil {
		t.Fatal(err)
	}
	first := readSessionUpdate(t, remote)
	pending := make([]byte, 256)
	pending[0] = 3
	signer.AddPending(pending)
	if err := m.SendMessage("hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := jp.ReadWirePacketFrom(remote, -1); err != nil {
		t.Fatal(err)
	}
	signer.AddPending(make([]byte, 256))

	// reconfiguration sends a new session: the chain and last-seen state restart
	if err := m.SendChatSessionData(); err != nil {
		t.Fatal(err)
	}
	second := readSessionUpdate(t, remote)
	if second.SessionID == first.SessionID || signer.SessionUUID() != second.SessionID {
		t.Fatal("session UUID not replaced")
	}
	if update := signer.GenerateAndApplyUpdate(); update.Offset != 0 || len(update.LastSeen) != 0 {
		t.Fatalf("last-seen state survived the new session: %+v", update)
	}
}

func readSessionUpdate(t *testing.T, remote net.Conn) packets.C2SChatSessionUpdate {
	t.Helper()
	wire, err := jp.ReadWirePacketFrom(remote, -1)
	if err != nil {
		t.Fatal(err)
	}
	var update packets.C2SChatSessionUpdate
	if wire.PacketID != ns.VarInt(packetids.C2SChatSessionUpdateID) || wire.ReadInto(&update) != nil {
		t.Fatalf("expected chat session update, got %d", wire.PacketID)
	}
	return update
}
