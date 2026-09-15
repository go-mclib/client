package chat

import (
	"bytes"
	"log"
	"testing"

	"github.com/go-mclib/client"
	chatsign "github.com/go-mclib/client/chat"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func TestSuppressedChatLogsPreserveDeliveryAndSigningState(t *testing.T) {
	c := client.New("unused", "Grace", false)
	c.SetState(jp.StatePlay)
	var logs bytes.Buffer
	c.Logger = log.New(&logs, "", 0)
	c.ChatSigner = chatsign.NewChatSigner()
	m := New()
	m.SuppressChatLogs = true
	c.Register(m)
	const text = "content excluded by the application retention policy"
	player, system, disguised := 0, 0, 0
	m.OnPlayerChat(func(_, message string, _ bool) {
		if message == text {
			player++
		}
	})
	m.OnSystemChat(func(message string, _ bool) {
		if message == text {
			system++
		}
	})
	m.OnDisguisedChat(func(_, message string, _ bool) {
		if message == text {
			disguised++
		}
	})
	for _, packet := range []jp.Packet{
		&packets.S2CPlayerChat{Signature: ns.Some(packets.MessageSignature{1}), Body: packets.SignedMessageBody{Content: text}, ChatType: packets.ChatTypeBound{Name: ns.NewTextComponent("Alice")}},
		&packets.S2CSystemChat{Content: ns.NewTextComponent(text)},
		&packets.S2CDisguisedChat{Message: ns.NewTextComponent(text), ChatType: packets.ChatTypeBound{Name: ns.NewTextComponent("Alice")}},
	} {
		wire, err := jp.ToWire(packet)
		if err != nil {
			t.Fatal(err)
		}
		m.HandlePacket(wire)
	}
	if bytes.Contains(logs.Bytes(), []byte(text)) {
		t.Fatalf("raw chat escaped to logs: %s", logs.String())
	}
	if player != 1 || system != 1 || disguised != 1 {
		t.Fatalf("suppression lost delivery: player=%d system=%d disguised=%d", player, system, disguised)
	}
	if c.ChatSigner.Offset() != 1 {
		t.Fatal("suppressing chat lost the received signature acknowledgement")
	}
}
