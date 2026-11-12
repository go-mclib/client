package chat

import (
	"bytes"
	"log"
	"testing"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/chat/internal/signing"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

func TestSuppressedChatLogsPreserveDeliveryAndSigningState(t *testing.T) {
	c := client.New("unused", "Grace", false)
	client.NewSimulator(c).SetState(jp.StatePlay)
	var logs bytes.Buffer
	c.Logger = log.New(&logs, "", 0)
	m := New()
	m.signer = signing.NewSigner()
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
	if m.signer.Offset() != 1 {
		t.Fatal("suppressing chat lost the received signature acknowledgement")
	}
}

func TestChatEventsCarryMetadata(t *testing.T) {
	c := client.New("unused", "Grace", false)
	client.NewSimulator(c).SetState(jp.StatePlay)
	m := New()
	m.SuppressChatLogs = true
	c.Register(m)
	var player []PlayerChatEvent
	var system []SystemChatEvent
	var disguised []DisguisedChatEvent
	m.OnPlayerChatEvent(func(e PlayerChatEvent) { player = append(player, e) })
	m.OnSystemChatEvent(func(e SystemChatEvent) { system = append(system, e) })
	m.OnDisguisedChatEvent(func(e DisguisedChatEvent) { disguised = append(disguised, e) })

	sender := ns.UUID{1, 2, 3}
	death := ns.TextComponent{Translate: "death.attack.generic", With: []ns.TextComponent{ns.NewTextComponent("Bob")}}
	for _, packet := range []jp.Packet{
		&packets.S2CPlayerChat{Sender: sender, Body: packets.SignedMessageBody{Content: "hi"},
			FilterMask: packets.FilterMask{Type: packets.FilterMaskFullyFiltered},
			ChatType:   packets.ChatTypeBound{Name: ns.NewTextComponent("Alice"), TargetName: ns.Some(ns.NewTextComponent("Grace"))}},
		&packets.S2CSystemChat{Content: death, Overlay: true},
		&packets.S2CDisguisedChat{Message: ns.NewTextComponent("hello"), ChatType: packets.ChatTypeBound{Name: ns.NewTextComponent("Server")}},
	} {
		wire, err := jp.ToWire(packet)
		if err != nil {
			t.Fatal(err)
		}
		m.HandlePacket(wire)
	}
	if len(player) != 1 || player[0] != (PlayerChatEvent{Sender: sender, Targeted: true, Filtered: true, Body: "hi"}) {
		t.Fatalf("player chat event: %+v", player)
	}
	if len(system) != 1 || !system[0].Overlay || system[0].Content.Translate != "death.attack.generic" ||
		len(system[0].Content.With) != 1 || system[0].Content.With[0].String() != "Bob" {
		t.Fatalf("system chat event: %+v", system)
	}
	if len(disguised) != 1 || disguised[0] != (DisguisedChatEvent{Sender: "Server", Message: "hello"}) {
		t.Fatalf("disguised chat event: %+v", disguised)
	}
}
