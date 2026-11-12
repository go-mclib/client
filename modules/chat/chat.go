package chat

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/chat/internal/signing"
	"github.com/go-mclib/client/modules/registry"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/data/lang"
	"github.com/go-mclib/data/packetids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

const (
	ModuleName = "chat"

	// maxChatLength is vanilla's ServerboundChatPacket limit in UTF-16 units.
	maxChatLength = 256
	// lastSeenBits is the size of the acknowledged-messages bit set.
	lastSeenBits = 20
)

type Module struct {
	// SuppressChatLogs disables raw chat-content logging, not callbacks or
	// signed-message acknowledgements. Configure before registering the module.
	// Applications with their own moderation/retention policy should enable it.
	SuppressChatLogs bool

	client *client.Client

	// sendLock (a one-slot semaphore, so waiting can be canceled) orders the
	// signing chain with the wire: it is held from last-seen update generation
	// through the packet write, so concurrent sends and acknowledgements reach
	// the server in index order. It also guards signer, which is nil without
	// an online-mode chat key.
	sendLock chan struct{}
	signer   *signing.Signer

	commandMu        sync.RWMutex
	commandTree      *commandTree
	commandTreeError error
	// suggestionMu guards play (nil outside play) and the suggestion
	// correlation state.
	suggestionMu       sync.Mutex
	play               *playPhase
	nextSuggestionID   int64
	pendingSuggestions map[ns.VarInt]*pendingSuggestion

	onPlayerChat         []func(sender, message string, isWhisper bool)
	onSystemChat         []func(message string, isOverlay bool)
	onDisguisedChat      []func(sender, message string, isWhisper bool)
	onPlayerChatEvent    []func(PlayerChatEvent)
	onSystemChatEvent    []func(SystemChatEvent)
	onDisguisedChatEvent []func(DisguisedChatEvent)
}

// errInactiveSession is returned for a session that is not (or no longer) the
// current one.
var errInactiveSession = errors.New("chat session is inactive or the connection changed")

func New() *Module {
	return &Module{sendLock: make(chan struct{}, 1)}
}

// lock acquires sendLock unless ctx is done first.
func (m *Module) lock(ctx context.Context) error {
	select {
	case m.sendLock <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		m.unlock()
		return err
	}
	return nil
}

func (m *Module) unlock() { <-m.sendLock }

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c
	c.OnConnect(m.loadSigner)
	c.OnPlay(m.startSuggestions)
	c.OnTransfer(m.Reset)
	c.OnDisconnect(m.Reset)
	c.OnDisconnect(func() { m.setSigner(nil) })
}

// loadSigner fetches the Mojang chat key for the authenticated account.
// Without one (offline mode, or a failed fetch) chat is sent unsigned.
func (m *Module) loadSigner() {
	signer, err := m.newSigner()
	if err != nil {
		m.client.Logger.Printf("[CHAT] chat signing unavailable: %v", err)
	}
	m.setSigner(signer)
}

func (m *Module) setSigner(signer *signing.Signer) {
	_ = m.lock(context.Background())
	m.signer = signer
	m.unlock()
}

func (m *Module) newSigner() (*signing.Signer, error) {
	authClient, ld, session := m.client.AuthClient(), m.client.LoginData(), m.client.Session()
	if authClient == nil || ld.AccessToken == "" || session == nil {
		return nil, nil
	}
	cert, err := authClient.FetchMojangCertificate(session.Context(), ld.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("fetch certificate: %w", err)
	}
	signer := signing.NewSigner()
	signer.SetPrivateKey(cert.PrivateKey)
	signer.PlayerUUID = ld.UUID
	signer.X509PublicKey = cert.PublicKeyBytes
	signer.SessionKey = cert.SignatureBytes
	signer.KeyExpiry = cert.ExpiryTime
	return signer, nil
}

func (m *Module) Reset() {
	m.commandMu.Lock()
	m.commandTree = nil
	m.commandTreeError = nil
	m.commandMu.Unlock()
	m.resetSuggestions()
}

// From retrieves the chat module from a client.
func From(c *client.Client) *Module { return client.ModuleAs[*Module](c, ModuleName) }

func (m *Module) OnPlayerChat(cb func(sender, message string, isWhisper bool)) {
	m.onPlayerChat = append(m.onPlayerChat, cb)
}
func (m *Module) OnSystemChat(cb func(message string, isOverlay bool)) {
	m.onSystemChat = append(m.onSystemChat, cb)
}
func (m *Module) OnDisguisedChat(cb func(sender, message string, isWhisper bool)) {
	m.onDisguisedChat = append(m.onDisguisedChat, cb)
}

// PlayerChatEvent is a signed player chat message with its authoritative
// metadata.
type PlayerChatEvent struct {
	Sender   [16]byte // profile UUID of the signing player
	ChatType string   // chat type registry ID ("" when the registry is unknown)
	Targeted bool     // the chat type names a target (e.g. a whisper)
	Filtered bool     // the server filtered (part of) the message
	Body     string   // the signed message body, without decoration
}

// SystemChatEvent is a system message; Content keeps translate keys and
// arguments.
type SystemChatEvent struct {
	Content ns.TextComponent
	Overlay bool // shown above the hotbar (action bar)
}

// DisguisedChatEvent is an unsigned chat message attributed by display name.
type DisguisedChatEvent struct {
	Sender   string // sender display name
	ChatType string // chat type registry ID ("" when the registry is unknown)
	Targeted bool   // the chat type names a target (e.g. a whisper)
	Message  string
}

// OnPlayerChatEvent, OnSystemChatEvent and OnDisguisedChatEvent observe chat
// with full metadata. Callbacks run on the packet loop, in packet order with
// other modules' callbacks.
func (m *Module) OnPlayerChatEvent(cb func(PlayerChatEvent)) {
	m.onPlayerChatEvent = append(m.onPlayerChatEvent, cb)
}
func (m *Module) OnSystemChatEvent(cb func(SystemChatEvent)) {
	m.onSystemChatEvent = append(m.onSystemChatEvent, cb)
}
func (m *Module) OnDisguisedChatEvent(cb func(DisguisedChatEvent)) {
	m.onDisguisedChatEvent = append(m.onDisguisedChatEvent, cb)
}

// chatTypeName resolves a chat type to its registry ID, "" when unknown.
func (m *Module) chatTypeName(bound packets.ChatTypeBound) string {
	if reg := registry.From(m.client); reg != nil {
		if entry, ok := reg.EntryByIndex("minecraft:chat_type", int(bound.ChatType)); ok {
			return entry.ID
		}
	}
	return ""
}

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	if m.client.State() != jp.StatePlay {
		return
	}
	switch pkt.PacketID {
	case packetids.S2CCommandSuggestionsID:
		m.handleCommandSuggestions(pkt)
	case packetids.S2CCommandsID:
		m.commandMu.Lock()
		defer m.commandMu.Unlock()
		var d packets.S2CCommands
		if err := pkt.ReadInto(&d); err != nil {
			m.client.Debugf("decode %T: %v", &d, err)
			m.commandTree = nil
			m.commandTreeError = err
			return
		}
		m.commandTree, m.commandTreeError = &commandTree{nodes: d.Nodes, root: int(d.RootIndex)}, nil
	case packetids.S2CPlayerChatID:
		m.handlePlayerChat(pkt)
	case packetids.S2CSystemChatID:
		m.handleSystemChat(pkt)
	case packetids.S2CDisguisedChatID:
		m.handleDisguisedChat(pkt)
	}
}

func (m *Module) handlePlayerChat(pkt *jp.WirePacket) {
	var d packets.S2CPlayerChat
	if !m.client.Decode(pkt, &d) {
		return
	}
	if d.Signature.Present {
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
	if len(m.onPlayerChatEvent) > 0 {
		event := PlayerChatEvent{Sender: [16]byte(d.Sender), ChatType: m.chatTypeName(d.ChatType), Targeted: isWhisper,
			Filtered: d.FilterMask.Type != packets.FilterMaskPassThrough, Body: msg}
		for _, cb := range m.onPlayerChatEvent {
			cb(event)
		}
	}
}

// acknowledgeReceived tracks a received message signature and sends a
// standalone acknowledgment once too many are pending, mirroring vanilla.
func (m *Module) acknowledgeReceived(signature []byte) {
	_ = m.lock(context.Background())
	defer m.unlock()
	cs := m.signer
	if cs == nil || !cs.AddPending(signature) {
		return
	}
	if cs.Offset() > signing.MaxUnacknowledged {
		err := m.client.WritePacket(&packets.C2SChatAck{
			MessageCount: ns.VarInt(cs.GetAndClearOffset()),
		})
		if err != nil {
			m.client.Logger.Printf("[CHAT] failed to send ack: %v", err)
		}
	}
}

func (m *Module) handleSystemChat(pkt *jp.WirePacket) {
	if m.SuppressChatLogs && len(m.onSystemChat) == 0 && len(m.onSystemChatEvent) == 0 {
		return
	}
	var d packets.S2CSystemChat
	if !m.client.Decode(pkt, &d) {
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
	for _, cb := range m.onSystemChatEvent {
		cb(SystemChatEvent{Content: d.Content, Overlay: bool(d.Overlay)})
	}
}

func (m *Module) handleDisguisedChat(pkt *jp.WirePacket) {
	if m.SuppressChatLogs && len(m.onDisguisedChat) == 0 && len(m.onDisguisedChatEvent) == 0 {
		return
	}
	var d packets.S2CDisguisedChat
	if !m.client.Decode(pkt, &d) {
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
	if len(m.onDisguisedChatEvent) > 0 {
		event := DisguisedChatEvent{Sender: d.ChatType.Name.String(), ChatType: m.chatTypeName(d.ChatType),
			Targeted: isWhisper, Message: d.Message.String()}
		for _, cb := range m.onDisguisedChatEvent {
			cb(event)
		}
	}
}

// SendMessage sends a chat message on the current session, signed when a chat
// key is available.
func (m *Module) SendMessage(message string) error {
	return m.SendMessageContext(context.Background(), nil, message)
}

// target returns the session to send on: session when it is still active,
// the current session when session is nil.
func (m *Module) target(session *client.Session) (*client.Session, error) {
	if session == nil {
		session = m.client.Session()
		if session == nil {
			return nil, client.ErrNotConnected
		}
	}
	if session.Context().Err() != nil {
		return nil, errInactiveSession
	}
	return session, nil
}

// writeSigned writes a packet that advanced the signing chain. A packet that
// was not written leaves the server's chain behind the signer, so the
// connection is closed rather than sending later messages the server rejects.
func writeSigned(ctx context.Context, session *client.Session, pkt jp.Packet) error {
	err := session.Write(ctx, pkt)
	if err != nil && session.Context().Err() == nil {
		if conn := session.Conn(); conn != nil {
			_ = conn.Close()
		}
	}
	return err
}

// SendMessageContext is SendMessage bound to ctx and session: it fails once
// ctx is done (including while waiting for a concurrent send) or when
// session has ended, so a caller bound to an old connection cannot send on a
// replacement. A nil session selects the current one.
func (m *Module) SendMessageContext(ctx context.Context, session *client.Session, message string) error {
	if n := utf16Len(message); n > maxChatLength {
		return fmt.Errorf("chat message too long: %d UTF-16 units", n)
	}
	if err := validChatLine(message); err != nil {
		return err
	}
	session, err := m.target(session)
	if err != nil {
		return err
	}
	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.unlock()
	if session.Context().Err() != nil {
		return errInactiveSession
	}

	if m.signer == nil {
		return session.Write(ctx, &packets.C2SChat{
			Message:      ns.String(message),
			Timestamp:    ns.Int64(time.Now().UnixMilli()),
			Acknowledged: ns.NewFixedBitSet(lastSeenBits),
		})
	}
	salt, err := newSalt()
	if err != nil {
		return err
	}
	timestamp := time.Now()
	update := m.signer.GenerateAndApplyUpdate()
	signature, err := m.signer.SignMessage(message, timestamp, salt, update.LastSeen)
	if err != nil {
		return err
	}
	return writeSigned(ctx, session, &packets.C2SChat{
		Message:      ns.String(message),
		Timestamp:    ns.Int64(timestamp.UnixMilli()),
		Salt:         ns.Int64(salt),
		Signature:    ns.PrefixedOptional[ns.ByteArray]{Present: true, Value: ns.ByteArray(signature)},
		MessageCount: ns.VarInt(update.Offset),
		Acknowledged: acknowledgedBits(update),
		Checksum:     ns.Int8(update.Checksum),
	})
}

// SendCommand sends a command on the current session. It signs
// minecraft:message arguments from the advertised graph; commands without
// signable arguments retain the ordinary unsigned packet.
func (m *Module) SendCommand(command string) error {
	return m.SendCommandContext(context.Background(), nil, command)
}

// SendCommandContext is SendCommand bound to ctx and session, like
// SendMessageContext.
func (m *Module) SendCommandContext(ctx context.Context, session *client.Session, command string) error {
	command = strings.TrimPrefix(command, "/")
	if err := validCommandInput(command); err != nil {
		return err
	}
	session, err := m.target(session)
	if err != nil {
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
		permissions, known := m.client.Module(self.ModuleName).(interface{ OpLevel() int8 })
		allowSelectors := known && permissions.OpLevel() >= 2
		arguments, err = tree.arguments(command, allowSelectors)
		if err != nil {
			return err
		}
	}

	if err := m.lock(ctx); err != nil {
		return err
	}
	defer m.unlock()
	if session.Context().Err() != nil {
		return errInactiveSession
	}
	if tree == nil && m.signer != nil {
		// Never guess which substrings to sign. Plain discovery still works
		// before a tree arrives, or when a custom parser prevents decoding.
		root, _, _ := strings.Cut(command, " ")
		if root != "help" && root != "plugins" && root != "version" && root != "list" {
			if treeError != nil {
				return fmt.Errorf("server command tree: %w", treeError)
			}
			return errors.New("server command tree has not arrived")
		}
	}
	if len(arguments) == 0 {
		return session.Write(ctx, &packets.C2SChatCommand{Command: ns.String(command)})
	}
	if m.signer == nil {
		// Vanilla offline sessions send the signed-command envelope with an
		// empty signature list. The server decides whether unsigned chat is
		// permitted; never fabricate signatures or claim acceptance.
		return session.Write(ctx, &packets.C2SChatCommandSigned{
			Command: ns.String(command), Timestamp: ns.Int64(time.Now().UnixMilli()),
			Acknowledged: ns.NewFixedBitSet(lastSeenBits),
		})
	}
	salt, err := newSalt()
	if err != nil {
		return fmt.Errorf("command salt: %w", err)
	}
	timestamp := time.Now()
	update := m.signer.GenerateAndApplyUpdate()
	signatures := make([]packets.CommandArgumentSignature, 0, len(arguments))
	for _, argument := range arguments {
		signature, err := m.signer.SignMessage(argument.text, timestamp, salt, update.LastSeen)
		if err != nil {
			return fmt.Errorf("sign command argument %s: %w", argument.name, err)
		}
		if len(signature) != 256 {
			return errors.New("command signature must be 256 bytes")
		}
		entry := packets.CommandArgumentSignature{Name: ns.String(argument.name)}
		copy(entry.Signature[:], signature)
		signatures = append(signatures, entry)
	}
	return writeSigned(ctx, session, &packets.C2SChatCommandSigned{
		Command: ns.String(command), Timestamp: ns.Int64(timestamp.UnixMilli()), Salt: ns.Int64(salt),
		ArgumentSignatures: signatures, MessageCount: ns.VarInt(update.Offset), Acknowledged: acknowledgedBits(update), Checksum: ns.Int8(update.Checksum),
	})
}

// SendChatSessionData starts a new chat session and sends its UUID, key and
// expiry. The client calls it when play begins on an encrypted connection.
func (m *Module) SendChatSessionData() error {
	_ = m.lock(context.Background())
	defer m.unlock()
	cs := m.signer
	if cs == nil {
		return errors.New("no chat signer")
	}
	if len(cs.X509PublicKey) == 0 {
		return errors.New("no public key")
	}
	var sessionID ns.UUID
	if _, err := rand.Read(sessionID[:]); err != nil {
		return fmt.Errorf("chat session id: %w", err)
	}
	// Vanilla's server starts a fresh message chain and last-seen validator
	// for every session update, including after reconfiguration.
	cs.StartSession(sessionID)
	return m.client.WritePacket(&packets.C2SChatSessionUpdate{
		SessionID:    sessionID,
		ExpiresAt:    ns.Int64(cs.KeyExpiry.UnixMilli()),
		PublicKey:    ns.ByteArray(cs.X509PublicKey),
		KeySignature: ns.ByteArray(cs.SessionKey),
	})
}

func newSalt() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(b[:])), nil
}

func acknowledgedBits(update signing.Update) *ns.FixedBitSet {
	bits := ns.NewFixedBitSet(lastSeenBits)
	for i, seen := range update.Acknowledged {
		if seen {
			bits.Set(i)
		}
	}
	return bits
}

// validChatLine applies vanilla's StringUtil.isAllowedChatCharacter (the
// server disconnects for '§', characters below 0x20 and DEL) plus the
// single-line rule: no other control, format or line/paragraph separators.
func validChatLine(s string) error {
	if !utf8.ValidString(s) {
		return errors.New("chat input is not valid UTF-8")
	}
	for _, r := range s {
		if r == '§' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return fmt.Errorf("chat input must be one line without control or formatting characters: %U", r)
		}
	}
	return nil
}

// utf16Len counts UTF-16 code units, the unit of vanilla's string limits.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}
