package chat

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/packetids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

func suggestionSocket(t *testing.T) (*Module, net.Conn) {
	t.Helper()
	m, remote := commandSocket(t)
	client.NewSimulator(m.client).Play()
	t.Cleanup(m.Reset)
	return m, remote
}

func requestSuggestion(m *Module, ctx context.Context, input string) <-chan suggestionResult {
	result := make(chan suggestionResult, 1)
	go func() {
		packet, err := m.SuggestCommandContext(ctx, nil, input)
		result <- suggestionResult{packet: packet, err: err}
	}()
	return result
}

func readSuggestionRequest(t *testing.T, remote net.Conn) packets.C2SCommandSuggestion {
	t.Helper()
	wire, err := jp.ReadWirePacketFrom(remote, -1)
	if err != nil {
		t.Fatal(err)
	}
	if wire.PacketID != ns.VarInt(packetids.C2SCommandSuggestionID) {
		t.Fatalf("completion sent a non-suggestion packet: %d", wire.PacketID)
	}
	var request packets.C2SCommandSuggestion
	if err := wire.ReadInto(&request); err != nil {
		t.Fatal(err)
	}
	return request
}

func awaitSuggestion(t *testing.T, result <-chan suggestionResult) suggestionResult {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(time.Second):
		t.Fatal("suggestion caller did not unblock")
		return suggestionResult{}
	}
}

// Encode the server list independently of S2CCommandSuggestions.Write: each
// entry has its own string and presence byte, not a length-prefixed byte array.
func suggestionReply(id, start, length ns.VarInt, text string) *jp.WirePacket {
	b := ns.NewWriter()
	b.WriteVarInt(id)
	b.WriteVarInt(start)
	b.WriteVarInt(length)
	b.WriteVarInt(2)
	b.WriteString(ns.String(text))
	b.WriteBool(false)
	b.WriteString("second choice")
	b.WriteBool(true)
	// Anonymous NBT TAG_String tooltip, with the unsigned-short NBT length.
	b.Write([]byte{8, 0, 4, 'h', 'i', 'n', 't'})
	return &jp.WirePacket{PacketID: ns.VarInt(packetids.S2CCommandSuggestionsID), Data: b.Bytes()}
}

func TestSuggestionsMatchOutOfOrderRepliesAndPreserveInput(t *testing.T) {
	m, remote := suggestionSocket(t)
	first := requestSuggestion(m, t.Context(), "/pw ")
	a := readSuggestionRequest(t, remote)
	second := requestSuggestion(m, t.Context(), "/msg 😀 ")
	b := readSuggestionRequest(t, remote)
	if a.Text != "/pw " || b.Text != "/msg 😀 " {
		t.Fatalf("changed completion input: %q, %q", a.Text, b.Text)
	}
	m.HandlePacket(suggestionReply(b.TransactionID, 8, 0, "player"))
	m.HandlePacket(suggestionReply(a.TransactionID, 4, 0, "private warp"))
	one, two := awaitSuggestion(t, first), awaitSuggestion(t, second)
	if one.err != nil || two.err != nil {
		t.Fatalf("completion failed: %v, %v", one.err, two.err)
	}
	if one.packet.TransactionID != a.TransactionID || one.packet.Start != 4 || one.packet.Matches[0].Text != "private warp" || two.packet.TransactionID != b.TransactionID || two.packet.Start != 8 || two.packet.Length != 0 || two.packet.Matches[0].Text != "player" {
		t.Fatalf("response attached to wrong request or range: %+v, %+v", one.packet, two.packet)
	}
	if len(two.packet.Matches) != 2 || two.packet.Matches[0].Tooltip.Present || !two.packet.Matches[1].Tooltip.Present || two.packet.Matches[1].Tooltip.Value.String() != "hint" {
		t.Fatalf("lost typed optional tooltip: %+v", two.packet.Matches)
	}
}

func TestSuggestionsRejectByteBasedRangeAndMalformedMatchedReply(t *testing.T) {
	m, remote := suggestionSocket(t)
	for _, malformed := range []bool{false, true} {
		result := requestSuggestion(m, t.Context(), "/pw 😀")
		request := readSuggestionRequest(t, remote)
		reply := suggestionReply(request.TransactionID, 7, 0, "warp") // input has six UTF-16 units, eight bytes
		if malformed {
			reply = suggestionReply(request.TransactionID, 6, 0, "warp")
			reply.Data = reply.Data[:len(reply.Data)-1]
		}
		m.HandlePacket(reply)
		if got := awaitSuggestion(t, result); got.err == nil {
			t.Fatalf("invalid server response accepted: %+v", got.packet)
		}
	}
}

func TestSuggestionsCancellationAndResetIgnoreLateIDs(t *testing.T) {
	m, remote := suggestionSocket(t)
	ctx, cancel := context.WithCancel(t.Context())
	canceled := requestSuggestion(m, ctx, "/pw ")
	old := readSuggestionRequest(t, remote)
	cancel()
	if got := awaitSuggestion(t, canceled); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cancellation did not unblock caller: %v", got.err)
	}
	reset := requestSuggestion(m, t.Context(), "/pw ")
	beforeReset := readSuggestionRequest(t, remote)
	m.Reset()
	if got := awaitSuggestion(t, reset); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("reset did not unblock caller: %v", got.err)
	}
	client.NewSimulator(m.client).Play() // transfers may reuse the transport
	fresh := requestSuggestion(m, t.Context(), "/pw ")
	current := readSuggestionRequest(t, remote)
	if current.TransactionID == old.TransactionID || current.TransactionID == beforeReset.TransactionID {
		t.Fatal("session reset reused a live-or-late transaction ID")
	}
	m.HandlePacket(suggestionReply(old.TransactionID, 4, 0, "canceled secret"))
	m.HandlePacket(suggestionReply(beforeReset.TransactionID, 4, 0, "old session secret"))
	m.HandlePacket(suggestionReply(current.TransactionID, 4, 0, "current warp"))
	if got := awaitSuggestion(t, fresh); got.err != nil || got.packet.Matches[0].Text != "current warp" {
		t.Fatalf("late response contaminated new session: %+v, %v", got.packet, got.err)
	}
}

func TestSuggestionsDisconnectAndStaleTransportGuard(t *testing.T) {
	m, remote := suggestionSocket(t)
	result := requestSuggestion(m, t.Context(), "/pw ")
	readSuggestionRequest(t, remote)
	stale := m.client.Session()
	client.NewSimulator(m.client).Disconnect()
	if got := awaitSuggestion(t, result); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("disconnect did not cancel pending reply: %v", got.err)
	}
	if _, err := m.SuggestCommandContext(t.Context(), nil, "/pw "); err == nil {
		t.Fatal("disconnected completion accepted")
	}
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	client.NewSimulator(m.client).Connect(left, jp.StatePlay)
	client.NewSimulator(m.client).Play()
	if _, err := m.SuggestCommandContext(t.Context(), stale, "/pw "); err == nil {
		t.Fatal("old turn was allowed to submit on a different connection")
	}
}

func TestSuggestionsDeadlineDoesNotCloseHealthyConnection(t *testing.T) {
	m, remote := suggestionSocket(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	result := requestSuggestion(m, ctx, "/pw ")
	old := readSuggestionRequest(t, remote)
	if got := awaitSuggestion(t, result); !errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("deadline did not unblock caller: %v", got.err)
	}
	fresh := requestSuggestion(m, t.Context(), "/pw ")
	current := readSuggestionRequest(t, remote)
	m.HandlePacket(suggestionReply(old.TransactionID, 4, 0, "late"))
	m.HandlePacket(suggestionReply(current.TransactionID, 4, 0, "ready"))
	if got := awaitSuggestion(t, fresh); got.err != nil || got.packet.Matches[0].Text != "ready" {
		t.Fatalf("timeout damaged later completion: %+v, %v", got.packet, got.err)
	}
}

func TestSuggestionPartialQuotesAndUTF16Limit(t *testing.T) {
	m, remote := suggestionSocket(t)
	input := `/pw "unfinished name `
	result := requestSuggestion(m, t.Context(), input)
	request := readSuggestionRequest(t, remote)
	if string(request.Text) != input {
		t.Fatalf("unfinished argument changed: %q", request.Text)
	}
	m.HandlePacket(suggestionReply(request.TransactionID, 4, ns.VarInt(utf16Len(input)-4), `"finished name"`))
	if got := awaitSuggestion(t, result); got.err != nil || got.packet.Matches[0].Text != `"finished name"` {
		t.Fatalf("partial quoted argument rejected: %+v, %v", got.packet, got.err)
	}
	if err := validSuggestionInput("/" + strings.Repeat("😀", 16249) + " "); err != nil {
		t.Fatalf("valid 32500-unit input rejected: %v", err)
	}
	for _, input := range []string{"/" + strings.Repeat("😀", 16250), "pw ", "/pw\n", "/pw \xff"} {
		if _, err := m.SuggestCommandContext(t.Context(), nil, input); err == nil {
			t.Fatalf("invalid completion input accepted: %q", input)
		}
	}
}

func TestSuggestionPacketEncodesTypedListAndOptionalTooltip(t *testing.T) {
	expected := suggestionReply(17, 4, 0, "warp")
	packet := &packets.S2CCommandSuggestions{
		TransactionID: 17, Start: 4, Length: 0,
		Matches: []packets.CommandSuggestion{
			{Text: "warp"},
			{Text: "second choice", Tooltip: ns.PrefixedOptional[ns.TextComponent]{
				Present: true, Value: ns.TextComponent{Text: "hint"},
			}},
		},
	}
	wire, err := jp.ToWire(packet)
	if err != nil {
		t.Fatal(err)
	}
	// Component writers may choose a compound rather than the legal bare
	// NBT string used by the independent server fixture. Verify the prefix
	// through the tooltip presence byte, then decode the actual component.
	prefixLength := len(expected.Data) - 7
	if !bytes.Equal(wire.Data[:prefixLength], expected.Data[:prefixLength]) {
		t.Fatalf("wrong list/optional encoding: %x", wire.Data)
	}
	tooltip, err := ns.NewReader(wire.Data[prefixLength:]).ReadTextComponent()
	if err != nil || tooltip.String() != "hint" {
		t.Fatalf("tooltip was not a typed component: %+v, %v", tooltip, err)
	}
}

type suggestionBlockedConn struct {
	net.Conn
	started chan struct{}
}

func (c *suggestionBlockedConn) Write(data []byte) (int, error) {
	close(c.started)
	return c.Conn.Write(data)
}

func TestSuggestionsReconnectAbortsBlockedWriteOnlyOnCapturedTransport(t *testing.T) {
	m, _ := suggestionSocket(t)
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	blocked := &suggestionBlockedConn{Conn: left, started: make(chan struct{})}
	sim := client.NewSimulator(m.client)
	sim.Connect(blocked, jp.StatePlay)
	sim.Play()
	waiting := requestSuggestion(m, t.Context(), "/pw ")
	select {
	case <-blocked.started:
	case <-time.After(time.Second):
		t.Fatal("request never reached the captured transport")
	}
	// Reconnect while the old request is blocked. A cancellation callback that
	// looks up the current connection would close the new one.
	newLeft, newRight := net.Pipe()
	defer newLeft.Close()
	defer newRight.Close()
	newRight.SetDeadline(time.Now().Add(time.Second))
	sim.Connect(newLeft, jp.StatePlay)
	sim.Play()
	if got := awaitSuggestion(t, waiting); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("blocked old write did not cancel: %v", got.err)
	}
	fresh := requestSuggestion(m, t.Context(), "/pw ")
	request := readSuggestionRequest(t, newRight)
	m.HandlePacket(suggestionReply(request.TransactionID, 4, 0, "new connection"))
	if got := awaitSuggestion(t, fresh); got.err != nil || got.packet.Matches[0].Text != "new connection" {
		t.Fatalf("old cancellation damaged replacement: %+v, %v", got.packet, got.err)
	}
}
