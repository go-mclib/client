package chat

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/go-mclib/client"
	chatsign "github.com/go-mclib/client/chat"
	"github.com/go-mclib/data/packet_ids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

const ModuleName = "chat"

type Module struct {
	client *client.Client

	onPlayerChat    []func(sender, message string, isWhisper bool)
	onSystemChat    []func(message string, isOverlay bool)
	onDisguisedChat []func(sender, message string, isWhisper bool)
}

func New() *Module {
	return &Module{}
}

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) { m.client = c }

func (m *Module) Reset() {}

// From retrieves the chat module from a client.
func From(c *client.Client) *Module {
	mod := c.Module(ModuleName)
	if mod == nil {
		return nil
	}
	return mod.(*Module)
}

func (m *Module) OnPlayerChat(cb func(sender, message string, isWhisper bool)) {
	m.onPlayerChat = append(m.onPlayerChat, cb)
}
func (m *Module) OnSystemChat(cb func(message string, isOverlay bool)) {
	m.onSystemChat = append(m.onSystemChat, cb)
}
func (m *Module) OnDisguisedChat(cb func(sender, message string, isWhisper bool)) {
	m.onDisguisedChat = append(m.onDisguisedChat, cb)
}

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	if m.client.State() != jp.StatePlay {
		return
	}
	switch pkt.PacketID {
	case packet_ids.S2CPlayerChatID:
		m.handlePlayerChat(pkt)
	case packet_ids.S2CSystemChatID:
		m.handleSystemChat(pkt)
	case packet_ids.S2CDisguisedChatID:
		m.handleDisguisedChat(pkt)
	}
}

func (m *Module) handlePlayerChat(pkt *jp.WirePacket) {
	var d packets.S2CPlayerChat
	if err := pkt.ReadInto(&d); err != nil {
		return
	}
	if m.client.ChatSigner != nil && d.Signature.Present {
		m.acknowledgeReceived(d.Signature.Value[:])
	}
	isWhisper := d.ChatType.TargetName.Present
	sender := d.ChatType.Name.Text
	msg := string(d.Body.Content)
	if isWhisper {
		m.client.Logger.Printf("[CHAT-WHISPER] %s -> %s: %s", sender, d.ChatType.TargetName.Value.Text, msg)
	} else {
		m.client.Logger.Printf("[CHAT] %s: %s", sender, msg)
	}
	for _, cb := range m.onPlayerChat {
		cb(sender, msg, isWhisper)
	}
}

// acknowledgeReceived tracks a received message signature and sends a
// standalone acknowledgment once too many are pending, mirroring vanilla.
func (m *Module) acknowledgeReceived(signature []byte) {
	cs := m.client.ChatSigner
	if !cs.AddPending(signature) {
		return
	}
	if cs.Offset() > chatsign.MaxUnacknowledged {
		err := m.client.WritePacket(&packets.C2SChatAck{
			MessageCount: ns.VarInt(cs.GetAndClearOffset()),
		})
		if err != nil {
			m.client.Logger.Printf("[CHAT] failed to send ack: %v", err)
		}
	}
}

func (m *Module) handleSystemChat(pkt *jp.WirePacket) {
	var d packets.S2CSystemChat
	if err := pkt.ReadInto(&d); err != nil {
		return
	}
	txt := d.Content.String()
	if d.Overlay {
		m.client.Logger.Printf("[SYSTEM-ACTION] %s", txt)
	} else {
		m.client.Logger.Printf("[SYSTEM] %s", txt)
	}
	for _, cb := range m.onSystemChat {
		cb(txt, bool(d.Overlay))
	}
}

func (m *Module) handleDisguisedChat(pkt *jp.WirePacket) {
	var d packets.S2CDisguisedChat
	if err := pkt.ReadInto(&d); err != nil {
		return
	}
	isWhisper := d.ChatType.TargetName.Present
	sender := d.ChatType.Name.String()
	msg := d.Message.String()
	if isWhisper {
		m.client.Logger.Printf("[DISGUISED] %s -> %s: %s", sender, d.ChatType.TargetName.Value.String(), msg)
	} else {
		m.client.Logger.Printf("[DISGUISED] %s: %s", sender, msg)
	}
	for _, cb := range m.onDisguisedChat {
		cb(sender, msg, isWhisper)
	}
}

// SendMessage sends a chat message (signed if ChatSigner is available).
func (m *Module) SendMessage(message string) error {
	if len(message) > 256 {
		return fmt.Errorf("chat message too long: %d", len(message))
	}
	c := m.client

	if c.ChatSigner != nil {
		saltBytes := make([]byte, 8)
		rand.Read(saltBytes)
		salt := int64(binary.BigEndian.Uint64(saltBytes))
		timestamp := time.Now()
		update := c.ChatSigner.GenerateAndApplyUpdate()
		signature, err := c.ChatSigner.SignMessage(message, timestamp, salt, update.LastSeen)
		if err != nil {
			return err
		}
		acknowledged := ns.NewFixedBitSet(20)
		for i, seen := range update.Acknowledged {
			if seen {
				acknowledged.Set(i)
			}
		}
		return c.WritePacket(&packets.C2SChat{
			Message:      ns.String(message),
			Timestamp:    ns.Int64(timestamp.UnixMilli()),
			Salt:         ns.Int64(salt),
			Signature:    ns.PrefixedOptional[ns.ByteArray]{Present: true, Value: ns.ByteArray(signature)},
			MessageCount: ns.VarInt(update.Offset),
			Acknowledged: acknowledged,
			Checksum:     ns.Int8(update.Checksum),
		})
	}

	return c.WritePacket(&packets.C2SChat{
		Message:      ns.String(message),
		Timestamp:    ns.Int64(time.Now().UnixMilli()),
		Salt:         0,
		Signature:    ns.PrefixedOptional[ns.ByteArray]{},
		MessageCount: 0,
		Acknowledged: ns.NewFixedBitSet(20),
		Checksum:     0,
	})
}

// SendCommand sends a command (strips leading /).
func (m *Module) SendCommand(command string) error {
	command = strings.TrimPrefix(command, "/")
	return m.client.WritePacket(&packets.C2SChatCommand{
		Command: ns.String(command),
	})
}

// SendChatSessionData sends the chat session UUID, key, and expiry.
// Implements client.ChatSessionSender.
func (m *Module) SendChatSessionData() error {
	c := m.client
	if c.ChatSigner == nil {
		return fmt.Errorf("no chat signer")
	}

	var sessionID ns.UUID
	rand.Read(sessionID[:])
	c.ChatSigner.SessionUUID = sessionID

	pub := c.ChatSigner.X509PublicKey
	if len(pub) == 0 {
		return fmt.Errorf("no public key")
	}

	return c.WritePacket(&packets.C2SChatSessionUpdate{
		SessionId:    sessionID,
		ExpiresAt:    ns.Int64(c.ChatSigner.KeyExpiry.UnixMilli()),
		PublicKey:    ns.ByteArray(pub),
		KeySignature: ns.ByteArray(c.ChatSigner.SessionKey),
	})
}
