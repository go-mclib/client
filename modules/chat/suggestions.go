package chat

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

const commandSuggestionTimeout = 5 * time.Second

type suggestionSession struct {
	ctx       context.Context
	cancel    context.CancelFunc
	transport *jp.TCPClient
	conn      *jp.Conn
}

type pendingSuggestion struct {
	inputLength int
	result      chan suggestionResult
}

type suggestionResult struct {
	packet packets.S2CCommandSuggestions
	err    error
}

func (m *Module) startSuggestions() {
	m.suggestionMu.Lock()
	defer m.suggestionMu.Unlock()
	m.resetSuggestionsLocked()
	transport := m.client.TCPClient
	if transport == nil || transport.Conn() == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.suggestionSession = &suggestionSession{ctx: ctx, cancel: cancel, transport: transport, conn: transport.Conn()}
}

func (m *Module) resetSuggestions() {
	m.suggestionMu.Lock()
	defer m.suggestionMu.Unlock()
	m.resetSuggestionsLocked()
}

func (m *Module) resetSuggestionsLocked() {
	if m.suggestionSession != nil {
		m.suggestionSession.cancel()
		m.suggestionSession = nil
	}
	clear(m.pendingSuggestions)
	// IDs are never reused, including across resets: late replies cannot match
	// a new request on a transfer that retains the same transport.
}

// SuggestCommandContext requests server-specific completions without executing a
// command. Input is the exact slash-prefixed partial command, including trailing
// spaces and unfinished quoted arguments. Start and Length remain UTF-16 code
// units in that input. Suggestions are neither exhaustive nor permission proof.
// This method is safe concurrently with HandlePacket and Reset. Do not hold a
// packet-dispatch or chat lock while waiting for it. Requests time out after five
// seconds, or earlier if ctx is canceled or the play session ends.
func (m *Module) SuggestCommandContext(ctx context.Context, input string) (packets.S2CCommandSuggestions, error) {
	return m.SuggestCommandContextForConnection(ctx, input, nil)
}

// SuggestCommandContextForConnection additionally requires the captured play
// connection to equal expected. Session-bound callers can release their own chat
// lock before calling without allowing an old turn to submit on a replacement.
// A nil expected connection selects the current play session.
func (m *Module) SuggestCommandContextForConnection(ctx context.Context, input string, expected *jp.Conn) (packets.S2CCommandSuggestions, error) {
	var empty packets.S2CCommandSuggestions
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := validSuggestionInput(input); err != nil {
		return empty, err
	}
	ctx, cancel := context.WithTimeout(ctx, commandSuggestionTimeout)
	defer cancel()

	m.suggestionMu.Lock()
	session := m.suggestionSession
	if session == nil || session.ctx.Err() != nil || (expected != nil && expected != session.conn) {
		m.suggestionMu.Unlock()
		return empty, fmt.Errorf("command suggestion session is inactive or connection changed")
	}
	if err := ctx.Err(); err != nil {
		m.suggestionMu.Unlock()
		return empty, err
	}
	if m.nextSuggestionID > math.MaxInt32 {
		m.suggestionMu.Unlock()
		return empty, fmt.Errorf("command suggestion transaction IDs exhausted")
	}
	id := ns.VarInt(m.nextSuggestionID)
	m.nextSuggestionID++
	pending := &pendingSuggestion{inputLength: commandUTF16Length(input), result: make(chan suggestionResult, 1)}
	if m.pendingSuggestions == nil {
		m.pendingSuggestions = make(map[ns.VarInt]*pendingSuggestion)
	}
	m.pendingSuggestions[id] = pending
	stopSession := context.AfterFunc(session.ctx, cancel)
	m.suggestionMu.Unlock()
	defer stopSession()
	defer func() {
		m.suggestionMu.Lock()
		delete(m.pendingSuggestions, id)
		m.suggestionMu.Unlock()
	}()

	// Unlike the client's reconnectable queue, this write targets only the
	// transport captured at OnPlay. No module or packet-loop lock spans I/O.
	// Canceling a blocked write closes only that captured connection; canceling
	// while awaiting the reply does not close a healthy connection.
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if err := session.ctx.Err(); err != nil {
		return empty, err
	}
	stopWrite := context.AfterFunc(ctx, func() { _ = session.conn.Close() })
	err := session.transport.WritePacket(suggestionPacket{
		C2SCommandSuggestion: &packets.C2SCommandSuggestion{TransactionId: id, Text: ns.String(input)},
		ctx:                  ctx,
		sessionCtx:           session.ctx,
	})
	stopWrite()
	if ctx.Err() != nil {
		return empty, ctx.Err()
	}
	if err := session.ctx.Err(); err != nil {
		return empty, err
	}
	if err != nil {
		return empty, err
	}
	select {
	case <-ctx.Done():
		return empty, ctx.Err()
	case <-session.ctx.Done():
		return empty, session.ctx.Err()
	case result := <-pending.result:
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		if err := session.ctx.Err(); err != nil {
			return empty, err
		}
		return result.packet, result.err
	}
}

type suggestionPacket struct {
	*packets.C2SCommandSuggestion
	ctx        context.Context
	sessionCtx context.Context
}

func (p suggestionPacket) Write(buf *ns.PacketBuffer) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if err := p.sessionCtx.Err(); err != nil {
		return err
	}
	return p.C2SCommandSuggestion.Write(buf)
}

func validSuggestionInput(input string) error {
	if !utf8.ValidString(input) || !strings.HasPrefix(input, "/") || commandUTF16Length(input) > 32500 {
		return fmt.Errorf("command completion requires a slash-prefixed UTF-8 input of at most 32500 UTF-16 code units")
	}
	for _, r := range input {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return fmt.Errorf("commands must be one line without control characters")
		}
	}
	return nil
}

func (m *Module) handleCommandSuggestions(pkt *jp.WirePacket) {
	// Read just the transaction ID first; stale/unrequested replies need no
	// suggestion or component allocation and cannot fail another caller.
	id, err := ns.NewReader(pkt.Data).ReadVarInt()
	if err != nil {
		return
	}
	m.suggestionMu.Lock()
	pending := m.pendingSuggestions[id]
	m.suggestionMu.Unlock()
	if pending == nil {
		return
	}
	var result suggestionResult
	result.err = pkt.ReadInto(&result.packet)
	if result.err == nil {
		start, length := int64(result.packet.Start), int64(result.packet.Length)
		if start < 0 || length < 0 || start+length > int64(pending.inputLength) {
			result.err = fmt.Errorf("server returned an invalid command suggestion UTF-16 replacement range")
		}
	}
	m.suggestionMu.Lock()
	defer m.suggestionMu.Unlock()
	if m.pendingSuggestions[id] != pending {
		return
	}
	delete(m.pendingSuggestions, id)
	pending.result <- result // buffered once; never blocks packet dispatch
}
