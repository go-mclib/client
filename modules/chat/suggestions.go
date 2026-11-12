package chat

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

const commandSuggestionTimeout = 5 * time.Second

// playPhase is the session captured at OnPlay. Its context also ends on
// module reset, which cancels pending requests.
type playPhase struct {
	session *client.Session
	ctx     context.Context
	cancel  context.CancelFunc
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
	if session := m.client.Session(); session != nil {
		ctx, cancel := context.WithCancel(session.Context())
		m.play = &playPhase{session: session, ctx: ctx, cancel: cancel}
	}
}

func (m *Module) resetSuggestions() {
	m.suggestionMu.Lock()
	defer m.suggestionMu.Unlock()
	m.resetSuggestionsLocked()
}

func (m *Module) resetSuggestionsLocked() {
	if m.play != nil {
		m.play.cancel()
		m.play = nil
	}
	clear(m.pendingSuggestions)
	// IDs are never reused, including across resets: late replies cannot match
	// a new request on a transfer that retains the same transport.
}

// SuggestCommandContext requests server-specific completions without executing a
// command. Input is the exact slash-prefixed partial command, including trailing
// spaces and unfinished quoted arguments. Start and Length remain UTF-16 code
// units in that input. Suggestions are neither exhaustive nor permission proof.
// A non-nil session must be the current play session, so a caller bound to an
// old connection cannot submit on a replacement; nil selects the current one.
// This method is safe concurrently with HandlePacket and Reset. Do not hold a
// packet-dispatch or chat lock while waiting for it. Requests time out after five
// seconds, or earlier if ctx is canceled or the play session ends.
func (m *Module) SuggestCommandContext(ctx context.Context, session *client.Session, input string) (packets.S2CCommandSuggestions, error) {
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
	play := m.play
	if play == nil || play.ctx.Err() != nil || (session != nil && session != play.session) {
		m.suggestionMu.Unlock()
		return empty, errInactiveSession
	}
	if err := ctx.Err(); err != nil {
		m.suggestionMu.Unlock()
		return empty, err
	}
	if m.nextSuggestionID > math.MaxInt32 {
		m.suggestionMu.Unlock()
		return empty, errors.New("command suggestion transaction IDs exhausted")
	}
	id := ns.VarInt(m.nextSuggestionID)
	m.nextSuggestionID++
	pending := &pendingSuggestion{inputLength: utf16Len(input), result: make(chan suggestionResult, 1)}
	if m.pendingSuggestions == nil {
		m.pendingSuggestions = make(map[ns.VarInt]*pendingSuggestion)
	}
	m.pendingSuggestions[id] = pending
	stopSession := context.AfterFunc(play.ctx, cancel)
	m.suggestionMu.Unlock()
	defer stopSession()
	defer func() {
		m.suggestionMu.Lock()
		delete(m.pendingSuggestions, id)
		m.suggestionMu.Unlock()
	}()

	// The write targets only the session captured at OnPlay; no module or
	// packet-loop lock spans I/O. Canceling a blocked write closes only that
	// session's connection; canceling while awaiting the reply does not close
	// a healthy connection.
	if err := play.session.Write(ctx, &packets.C2SCommandSuggestion{TransactionID: id, Text: ns.String(input)}); err != nil {
		return empty, err
	}
	// the play phase ending cancels ctx
	select {
	case <-ctx.Done():
		return empty, ctx.Err()
	case result := <-pending.result:
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		return result.packet, result.err
	}
}

func validSuggestionInput(input string) error {
	if !strings.HasPrefix(input, "/") || utf16Len(input) > 32500 {
		return errors.New("command completion requires a slash-prefixed input of at most 32500 UTF-16 code units")
	}
	return validChatLine(input)
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
			result.err = errors.New("server returned an invalid command suggestion UTF-16 replacement range")
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
