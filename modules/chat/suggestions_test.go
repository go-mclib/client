package chat

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-mclib/data/packet_ids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func suggestionSocket(t *testing.T) (*Module, net.Conn) {
	t.Helper()
	m, remote := commandSocket(t)
	m.client.SetState(jp.StatePlay)
	m.client.FirePlay()
	t.Cleanup(m.Reset)
	return m, remote
}

func requestSuggestion(m *Module, ctx context.Context, input string) <-chan suggestionResult {
	result := make(chan suggestionResult, 1)
	go func() {
		packet, err := m.SuggestCommandContext(ctx, input)
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
	if wire.PacketID != ns.VarInt(packet_ids.C2SCommandSuggestionID) {
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
	return &jp.WirePacket{PacketID: ns.VarInt(packet_ids.S2CCommandSuggestionsID), Data: b.Bytes()}
}

func TestSuggestionsMatchOutOfOrderRepliesAndPreserveInput(t *testing.T) {
	m, remote := suggestionSocket(t)
	first := requestSuggestion(m, context.Background(), "/pw ")
	a := readSuggestionRequest(t, remote)
	second := requestSuggestion(m, context.Background(), "/msg 😀 ")
	b := readSuggestionRequest(t, remote)
	if a.Text != "/pw " || b.Text != "/msg 😀 " {
		t.Fatalf("changed completion input: %q, %q", a.Text, b.Text)
	}
	m.HandlePacket(suggestionReply(b.TransactionId, 8, 0, "player"))
	m.HandlePacket(suggestionReply(a.TransactionId, 4, 0, "private warp"))
	one, two := awaitSuggestion(t, first), awaitSuggestion(t, second)
	if one.err != nil || two.err != nil {
		t.Fatalf("completion failed: %v, %v", one.err, two.err)
	}
	if one.packet.Id != a.TransactionId || one.packet.Start != 4 || one.packet.Matches[0].Text != "private warp" || two.packet.Id != b.TransactionId || two.packet.Start != 8 || two.packet.Length != 0 || two.packet.Matches[0].Text != "player" {
		t.Fatalf("response attached to wrong request or range: %+v, %+v", one.packet, two.packet)
	}
	if len(two.packet.Matches) != 2 || two.packet.Matches[0].Tooltip.Present || !two.packet.Matches[1].Tooltip.Present || two.packet.Matches[1].Tooltip.Value.String() != "hint" {
		t.Fatalf("lost typed optional tooltip: %+v", two.packet.Matches)
	}
}

func TestSuggestionsRejectByteBasedRangeAndMalformedMatchedReply(t *testing.T) {
	m, remote := suggestionSocket(t)
	for _, malformed := range []bool{false, true} {
		result := requestSuggestion(m, context.Background(), "/pw 😀")
		request := readSuggestionRequest(t, remote)
		reply := suggestionReply(request.TransactionId, 7, 0, "warp") // input has six UTF-16 units, eight bytes
		if malformed {
			reply = suggestionReply(request.TransactionId, 6, 0, "warp")
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
	ctx, cancel := context.WithCancel(context.Background())
	canceled := requestSuggestion(m, ctx, "/pw ")
	old := readSuggestionRequest(t, remote)
	cancel()
	if got := awaitSuggestion(t, canceled); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cancellation did not unblock caller: %v", got.err)
	}
	reset := requestSuggestion(m, context.Background(), "/pw ")
	beforeReset := readSuggestionRequest(t, remote)
	m.Reset()
	if got := awaitSuggestion(t, reset); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("reset did not unblock caller: %v", got.err)
	}
	m.client.FirePlay() // transfers may reuse the transport
	fresh := requestSuggestion(m, context.Background(), "/pw ")
	current := readSuggestionRequest(t, remote)
	if current.TransactionId == old.TransactionId || current.TransactionId == beforeReset.TransactionId {
		t.Fatal("session reset reused a live-or-late transaction ID")
	}
	m.HandlePacket(suggestionReply(old.TransactionId, 4, 0, "canceled secret"))
	m.HandlePacket(suggestionReply(beforeReset.TransactionId, 4, 0, "old session secret"))
	m.HandlePacket(suggestionReply(current.TransactionId, 4, 0, "current warp"))
	if got := awaitSuggestion(t, fresh); got.err != nil || got.packet.Matches[0].Text != "current warp" {
		t.Fatalf("late response contaminated new session: %+v, %v", got.packet, got.err)
	}
}

func TestSuggestionsDisconnectAndStaleTransportGuard(t *testing.T) {
	m, remote := suggestionSocket(t)
	result := requestSuggestion(m, context.Background(), "/pw ")
	readSuggestionRequest(t, remote)
	m.client.FireDisconnect()
	if got := awaitSuggestion(t, result); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("disconnect did not cancel pending reply: %v", got.err)
	}
	if _, err := m.SuggestCommandContext(context.Background(), "/pw "); err == nil {
		t.Fatal("disconnected completion accepted")
	}
	m.client.FirePlay()
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	if _, err := m.SuggestCommandContextForConnection(context.Background(), "/pw ", jp.NewConn(left)); err == nil {
		t.Fatal("old turn was allowed to submit on a different connection")
	}
}

func TestSuggestionsDeadlineDoesNotCloseHealthyConnection(t *testing.T) {
	m, remote := suggestionSocket(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result := requestSuggestion(m, ctx, "/pw ")
	old := readSuggestionRequest(t, remote)
	if got := awaitSuggestion(t, result); !errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("deadline did not unblock caller: %v", got.err)
	}
	fresh := requestSuggestion(m, context.Background(), "/pw ")
	current := readSuggestionRequest(t, remote)
	m.HandlePacket(suggestionReply(old.TransactionId, 4, 0, "late"))
	m.HandlePacket(suggestionReply(current.TransactionId, 4, 0, "ready"))
	if got := awaitSuggestion(t, fresh); got.err != nil || got.packet.Matches[0].Text != "ready" {
		t.Fatalf("timeout damaged later completion: %+v, %v", got.packet, got.err)
	}
}

func TestSuggestionPartialQuotesAndUTF16Limit(t *testing.T) {
	m, remote := suggestionSocket(t)
	input := `/pw "unfinished name `
	result := requestSuggestion(m, context.Background(), input)
	request := readSuggestionRequest(t, remote)
	if string(request.Text) != input {
		t.Fatalf("unfinished argument changed: %q", request.Text)
	}
	m.HandlePacket(suggestionReply(request.TransactionId, 4, ns.VarInt(commandUTF16Length(input)-4), `"finished name"`))
	if got := awaitSuggestion(t, result); got.err != nil || got.packet.Matches[0].Text != `"finished name"` {
		t.Fatalf("partial quoted argument rejected: %+v, %v", got.packet, got.err)
	}
	if err := validSuggestionInput("/" + strings.Repeat("😀", 16249) + " "); err != nil {
		t.Fatalf("valid 32500-unit input rejected: %v", err)
	}
	for _, input := range []string{"/" + strings.Repeat("😀", 16250), "pw ", "/pw\n", "/pw \xff"} {
		if _, err := m.SuggestCommandContext(context.Background(), input); err == nil {
			t.Fatalf("invalid completion input accepted: %q", input)
		}
	}
}

func TestSuggestionPacketEncodesTypedListAndOptionalTooltip(t *testing.T) {
	expected := suggestionReply(17, 4, 0, "warp")
	packet := &packets.S2CCommandSuggestions{
		Id: 17, Start: 4, Length: 0,
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

func TestSuggestionsResetAbortsBlockedWriteOnlyOnCapturedTransport(t *testing.T) {
	m, _ := suggestionSocket(t)
	original := m.client.TCPClient
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	blocked := &suggestionBlockedConn{Conn: left, started: make(chan struct{})}
	oldTransport := jp.NewTCPClient()
	oldTransport.SetConn(jp.NewConn(blocked))
	oldTransport.SetState(jp.StatePlay)
	m.client.TCPClient = oldTransport
	m.client.FirePlay()
	waiting := requestSuggestion(m, context.Background(), "/pw ")
	select {
	case <-blocked.started:
	case <-time.After(time.Second):
		t.Fatal("request never reached the captured transport")
	}
	// Replace the transport while its old request is blocked. A cancellation
	// callback that looks up client.Conn() now would close the new connection.
	newLeft, newRight := net.Pipe()
	defer newLeft.Close()
	defer newRight.Close()
	newRight.SetDeadline(time.Now().Add(time.Second))
	replacement := jp.NewTCPClient()
	replacement.SetConn(jp.NewConn(newLeft))
	replacement.SetState(jp.StatePlay)
	m.client.TCPClient = replacement
	m.client.FirePlay()
	defer func() { m.client.TCPClient = original }()
	if got := awaitSuggestion(t, waiting); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("blocked old write did not cancel: %v", got.err)
	}
	fresh := requestSuggestion(m, context.Background(), "/pw ")
	request := readSuggestionRequest(t, newRight)
	m.HandlePacket(suggestionReply(request.TransactionId, 4, 0, "new connection"))
	if got := awaitSuggestion(t, fresh); got.err != nil || got.packet.Matches[0].Text != "new connection" {
		t.Fatalf("old cancellation damaged replacement: %+v, %v", got.packet, got.err)
	}
}
