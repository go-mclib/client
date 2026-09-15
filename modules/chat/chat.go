package chat

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-mclib/client"
	chatsign "github.com/go-mclib/client/chat"
	"github.com/go-mclib/data/lang"
	"github.com/go-mclib/data/packet_ids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

const ModuleName = "chat"

type Module struct {
	// SuppressChatLogs disables raw chat-content logging, not callbacks or
	// signed-message acknowledgements. Configure before registering the module.
	// Applications with their own moderation/retention policy should enable it.
	SuppressChatLogs bool

	client             *client.Client
	commandMu          sync.RWMutex
	commandTree        *commandTree
	commandTreeError   error
	suggestionMu       sync.Mutex
	suggestionSession  *suggestionSession
	nextSuggestionID   int64
	pendingSuggestions map[ns.VarInt]*pendingSuggestion

	onPlayerChat    []func(sender, message string, isWhisper bool)
	onSystemChat    []func(message string, isOverlay bool)
	onDisguisedChat []func(sender, message string, isWhisper bool)
}

func New() *Module {
	return &Module{}
}

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c
	c.OnPlay(m.startSuggestions)
	c.OnTransfer(m.Reset)
	c.OnDisconnect(m.Reset)
}

func (m *Module) Reset() {
	m.commandMu.Lock()
	m.commandTree = nil
	m.commandTreeError = nil
	m.commandMu.Unlock()
	m.resetSuggestions()
}

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
	case packet_ids.S2CCommandSuggestionsID:
		m.handleCommandSuggestions(pkt)
	case packet_ids.S2CCommandsID:
		m.commandMu.Lock()
		defer m.commandMu.Unlock()
		var d packets.S2CCommands
		if err := pkt.ReadInto(&d); err != nil {
			m.commandTree = nil
			m.commandTreeError = err
			return
		}
		m.commandTree, m.commandTreeError = parseCommandTree(d.Data)
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
	if !m.SuppressChatLogs {
		if isWhisper {
			m.client.Logger.Printf("[CHAT-WHISPER] %s -> %s: %s", sender, d.ChatType.TargetName.Value.Text, msg)
		} else {
			m.client.Logger.Printf("[CHAT] %s: %s", sender, msg)
		}
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
	if m.SuppressChatLogs && len(m.onSystemChat) == 0 {
		return
	}
	var d packets.S2CSystemChat
	if err := pkt.ReadInto(&d); err != nil {
		return
	}
	txt := d.Content.Render(lang.Translate)
	if !m.SuppressChatLogs {
		if d.Overlay {
			m.client.Logger.Printf("[SYSTEM-ACTION] %s", txt)
		} else {
			m.client.Logger.Printf("[SYSTEM] %s", txt)
		}
	}
	for _, cb := range m.onSystemChat {
		cb(txt, bool(d.Overlay))
	}
}

func (m *Module) handleDisguisedChat(pkt *jp.WirePacket) {
	if m.SuppressChatLogs && len(m.onDisguisedChat) == 0 {
		return
	}
	var d packets.S2CDisguisedChat
	if err := pkt.ReadInto(&d); err != nil {
		return
	}
	isWhisper := d.ChatType.TargetName.Present
	sender := d.ChatType.Name.Render(lang.Translate)
	msg := d.Message.Render(lang.Translate)
	if !m.SuppressChatLogs {
		if isWhisper {
			m.client.Logger.Printf("[DISGUISED] %s -> %s: %s", sender, d.ChatType.TargetName.Value.Render(lang.Translate), msg)
		} else {
			m.client.Logger.Printf("[DISGUISED] %s: %s", sender, msg)
		}
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

// SendCommand signs minecraft:message arguments from the advertised graph.
// Commands without signable arguments retain the ordinary unsigned packet.
func (m *Module) SendCommand(command string) error {
	command = strings.TrimPrefix(command, "/")
	if err := validCommandInput(command); err != nil {
		return err
	}
	var arguments []signableArgument
	m.commandMu.RLock()
	tree, treeError := m.commandTree, m.commandTreeError
	m.commandMu.RUnlock()
	if tree != nil {
		var err error
		// Vanilla LevelBasedPermissionSet grants COMMANDS_ENTITY_SELECTORS at
		// GAMEMASTERS (level 2). Missing client permission state is not a grant.
		// self.OpLevel is a synchronized accessor, not an exported state field.
		permissions, known := m.client.Module("self").(interface{ OpLevel() int8 })
		allowSelectors := known && permissions.OpLevel() >= 2
		arguments, err = tree.arguments(command, allowSelectors)
		if err != nil {
			return err
		}
	} else if m.client.ChatSigner != nil {
		// Never guess which substrings to sign. Plain discovery still works
		// before a tree arrives, or when a custom parser prevents decoding.
		root := strings.SplitN(command, " ", 2)[0]
		if root != "help" && root != "plugins" && root != "version" && root != "list" {
			if treeError != nil {
				return fmt.Errorf("server command tree: %w", treeError)
			}
			return fmt.Errorf("server command tree has not arrived")
		}
	}
	if len(arguments) == 0 {
		return m.client.WritePacket(&packets.C2SChatCommand{Command: ns.String(command)})
	}
	if m.client.ChatSigner == nil {
		// Vanilla offline sessions send the signed-command envelope with an
		// empty signature list. The server decides whether unsigned chat is
		// permitted; never fabricate signatures or claim acceptance.
		return m.client.WritePacket(&packets.C2SChatCommandSigned{
			Command: ns.String(command), Timestamp: ns.Int64(time.Now().UnixMilli()),
			Acknowledged: ns.NewFixedBitSet(20),
		})
	}
	var saltBytes [8]byte
	if _, err := rand.Read(saltBytes[:]); err != nil {
		return fmt.Errorf("command salt: %w", err)
	}
	salt := int64(binary.BigEndian.Uint64(saltBytes[:]))
	timestamp := time.Now()
	update := m.client.ChatSigner.GenerateAndApplyUpdate()
	signatures := make([]packets.CommandArgumentSignature, 0, len(arguments))
	for _, argument := range arguments {
		signature, err := m.client.ChatSigner.SignMessage(argument.text, timestamp, salt, update.LastSeen)
		if err != nil {
			return fmt.Errorf("sign command argument %s: %w", argument.name, err)
		}
		if len(signature) != 256 {
			return fmt.Errorf("command signature must be 256 bytes")
		}
		entry := packets.CommandArgumentSignature{Name: ns.String(argument.name)}
		copy(entry.Signature[:], signature)
		signatures = append(signatures, entry)
	}
	ack := ns.NewFixedBitSet(20)
	for i, seen := range update.Acknowledged {
		if seen {
			ack.Set(i)
		}
	}
	return m.client.WritePacket(&packets.C2SChatCommandSigned{
		Command: ns.String(command), Timestamp: ns.Int64(timestamp.UnixMilli()), Salt: ns.Int64(salt),
		ArgumentSignatures: signatures, MessageCount: ns.VarInt(update.Offset), Acknowledged: ack, Checksum: ns.Int8(update.Checksum),
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
