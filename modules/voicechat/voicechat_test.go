package voicechat

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/packetids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
)

func TestVoiceJavaGCMEnvelope(t *testing.T) {
	// NIST AES-128/GCM: zero key, 96-bit zero IV, one zero plaintext block.
	// Java prepends that IV to ciphertext || 128-bit tag, without AAD.
	block, err := aes.NewCipher(make([]byte, 16))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := hex.DecodeString("0388dace60b6a392f328c2b971b2fe78ab6e47d42cec13bdf53a67b21257bddf")
	if err != nil {
		t.Fatal(err)
	}
	frame := append([]byte{0xff, 44}, make([]byte, 12)...)
	frame = append(frame, ciphertext...)
	s := &Session{crypto: aead}
	if got := s.parseInbound(bytes.Clone(frame)); !bytes.Equal(got, make([]byte, 16)) {
		t.Fatalf("Java/NIST frame decrypted to %x", got)
	}
	frame[len(frame)-1] ^= 1
	if got := s.parseInbound(frame); got != nil {
		t.Fatalf("accepted a forged authentication tag: %x", got)
	}
	for _, malformed := range [][]byte{
		{0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}, // overflowing VarInt
		{0xff, 0x81, 0x10},                   // encrypted payload exceeds 2048 bytes
		{0xff, 44, 0},                        // truncated nonce/ciphertext
	} {
		if got := s.parseInbound(malformed); got != nil {
			t.Fatalf("accepted malformed envelope %x", malformed)
		}
	}
}

func TestVoiceEndpointOverrides(t *testing.T) {
	for _, tc := range []struct{ override, want string }{
		{"", "127.0.0.1:24454"},
		{"24455", "127.0.0.1:24455"},
		{"70000", "127.0.0.1:24454"},
		{"voice.example", "voice.example:24454"},
		{"voice.example:24456", "voice.example:24456"},
		{"[::1]:24457", "[::1]:24457"},
	} {
		got, err := resolveEndpoint(tc.override, "127.0.0.1", 24454)
		if err != nil || got != tc.want {
			t.Errorf("override %q: got %q, %v; want %q", tc.override, got, err, tc.want)
		}
	}
	if _, err := resolveEndpoint("voice.example:65536", "127.0.0.1", 24454); err == nil {
		t.Fatal("accepted out-of-range port")
	}
}

// Exercise the actual UDP socket and plugin-channel exchange, including a lost
// authentication datagram, a cancelled caller, control replies, end-of-speech,
// and a server which stops sending keepalives. No Minecraft server is required.
func TestVoiceLoopbackCancellationHandshakeAndLiveness(t *testing.T) {
	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	secret := &credentials{port: server.LocalAddr().(*net.UDPAddr).Port, mtu: 1275, keepAlive: 100 * time.Millisecond}
	copy(secret.key[:], []byte("0123456789abcdef"))
	copy(secret.player[:], []byte("fedcba9876543210"))
	block, err := aes.NewCipher(secret.key[:])
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	queue := make(chan jp.Packet, 1)
	life, stopLife := context.WithCancel(ctx)
	v := newPlayModule(t)
	v.connection = &connection{ctx: life, cancel: stopLife, host: "127.0.0.1", send: queueSend(queue), secretReady: make(chan struct{})}
	defer v.Close()
	caller, stopCaller := context.WithCancel(ctx)
	first := make(chan error, 1)
	go func() { _, err := v.Connect(caller); first <- err }()
	select {
	case packet := <-queue:
		request, ok := packet.(*packets.C2SCustomPayloadPlay)
		if !ok || request.Channel != "voicechat:request_secret" || !bytes.Equal(request.Data, []byte{0, 0, 0, 20}) {
			t.Fatalf("invalid secret request: %#v", packet)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	stopCaller()
	select {
	case err := <-first:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled caller returned %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	type result struct {
		session *Session
		err     error
	}
	connected := make(chan result, 1)
	go func() { s, err := v.Connect(ctx); connected <- result{s, err} }()
	// Construct the Java Secret field order independently of the parser.
	payload := append([]byte(nil), secret.key[:]...)
	payload = binary.BigEndian.AppendUint32(payload, uint32(secret.port))
	payload = append(payload, secret.player[:]...)
	payload = append(payload, 0) // codec
	payload = binary.BigEndian.AppendUint32(payload, 1275)
	payload = binary.BigEndian.AppendUint64(payload, 0) // distance
	payload = binary.BigEndian.AppendUint32(payload, 100)
	payload = append(payload, 1, 0, 0) // groups, empty voice host, recording
	channel := "voicechat:secret"
	data := append([]byte{byte(len(channel))}, channel...)
	data = append(data, payload...)
	v.HandlePacket(&jp.WirePacket{PacketID: packetids.S2CCustomPayloadPlayID, Data: data})

	var peer *net.UDPAddr
	readPacket := func(want byte) []byte {
		t.Helper()
		if err := server.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		var buffer [4096]byte
		n, addr, err := server.ReadFromUDP(buffer[:])
		if err != nil {
			t.Fatal(err)
		}
		peer = addr
		frame := buffer[:n]
		if n < 18 || frame[0] != 0xff || !bytes.Equal(frame[1:17], secret.player[:]) {
			t.Fatalf("invalid outbound voice envelope: %x", frame)
		}
		length, used := binary.Uvarint(frame[17:])
		if used <= 0 || int(length)+17+used != n || length < 29 {
			t.Fatalf("invalid encrypted length: %x", frame[17:])
		}
		encrypted := frame[17+used:]
		plain, err := aead.Open(nil, encrypted[:12], encrypted[12:], nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(plain) == 0 || plain[0] != want {
			t.Fatalf("got packet %x, want id %d", plain, want)
		}
		return plain[1:]
	}
	var nonceCounter uint64
	sendPacket := func(id byte, body []byte) {
		t.Helper()
		var nonce [12]byte
		nonceCounter++
		binary.BigEndian.PutUint64(nonce[4:], nonceCounter)
		plain := append([]byte{id}, body...)
		encrypted := append([]byte(nil), nonce[:]...)
		encrypted = aead.Seal(encrypted, nonce[:], plain, nil)
		frame := binary.AppendUvarint([]byte{0xff}, uint64(len(encrypted)))
		frame = append(frame, encrypted...)
		if _, err := server.WriteToUDP(frame, peer); err != nil {
			t.Fatal(err)
		}
	}
	wantAuth := append(bytes.Clone(secret.player[:]), secret.key[:]...)
	if body := readPacket(5); !bytes.Equal(body, wantAuth) {
		t.Fatalf("authentication body = %x", body)
	}
	// Drop the first authentication request; the second must still carry the key.
	if body := readPacket(5); !bytes.Equal(body, wantAuth) {
		t.Fatalf("authentication retry body = %x", body)
	}
	// Real servers can include authenticated padding in bodyless control packets.
	// The Java client ignores it; requiring an empty body breaks authentication.
	padding := bytes.Repeat([]byte{0xa5}, 255)
	sendPacket(6, padding)
	if body := readPacket(9); len(body) != 0 {
		t.Fatalf("connection check has body %x", body)
	}
	sendPacket(10, padding)
	var session *Session
	select {
	case result := <-connected:
		if result.err != nil {
			t.Fatal(result.err)
		}
		session = result.session
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	ping := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	sendPacket(7, ping)
	if body := readPacket(7); !bytes.Equal(body, ping) {
		t.Fatalf("ping was not echoed unchanged: %x", body)
	}
	sendPacket(8, padding)
	if body := readPacket(8); len(body) != 0 {
		t.Fatalf("keepalive reply has body %x", body)
	}
	for seq, opus := range [][]byte{{0xf8, 0xff, 0xfe}, nil} {
		if err := session.SendAudio(opus); err != nil {
			t.Fatal(err)
		}
		body := readPacket(1)
		want := binary.AppendUvarint(nil, uint64(len(opus)))
		want = append(want, opus...)
		want = binary.BigEndian.AppendUint64(want, uint64(seq))
		want = append(want, 0)
		if !bytes.Equal(body, want) {
			t.Fatalf("microphone frame %d = %x, want %x", seq, body, want)
		}
	}
	select {
	case <-session.done:
	case <-ctx.Done():
		t.Fatal("silent server did not expire voice transport")
	}
	if err := session.SendAudio([]byte{0xf8, 0xff, 0xfe}); err == nil {
		t.Fatal("accepted audio after transport timeout")
	}
	v.Close()
	if _, err := v.Connect(ctx); err == nil {
		t.Fatal("connected after close without entering Minecraft play again")
	}
	select {
	case <-queue:
		t.Fatal("cancelled caller caused a second single-use secret request")
	default:
	}
}

func newPlayModule(t *testing.T) *Module {
	t.Helper()
	c := client.New("localhost:25565", "voice", false)
	v := New()
	c.Register(v)
	client.NewSimulator(c).Connect(nil, jp.StatePlay)
	return v
}

// queueSend is a session send that hands packets to the test without a transport.
func queueSend(queue chan jp.Packet) func(context.Context, jp.Packet) (<-chan error, error) {
	return func(_ context.Context, p jp.Packet) (<-chan error, error) {
		select {
		case queue <- p:
			done := make(chan error, 1)
			done <- nil
			return done, nil
		default:
			return nil, client.ErrQueueFull
		}
	}
}

func TestVoiceConnectionFollowsPlaySession(t *testing.T) {
	c := client.New("localhost:25565", "voice", false)
	v := New()
	c.Register(v)
	sim := client.NewSimulator(c)
	session := sim.Connect(nil, jp.StatePlay)
	sim.Play()
	v.mu.Lock()
	conn := v.connection
	v.mu.Unlock()
	if conn == nil || conn.send == nil {
		t.Fatal("play did not open a voice connection")
	}
	sim.Transfer()
	if session.Context().Err() == nil || conn.ctx.Err() == nil {
		t.Fatal("transfer did not end the voice connection")
	}
	if _, err := v.Connect(context.Background()); err == nil {
		t.Fatal("connected outside play")
	}
}

func TestVoiceRejectedSecretRequestIsRetried(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	queue := make(chan jp.Packet, 1)
	queue <- nil // full: the first request is rejected
	life, stopLife := context.WithCancel(ctx)
	v := newPlayModule(t)
	v.connection = &connection{ctx: life, cancel: stopLife, host: "127.0.0.1", send: queueSend(queue), secretReady: make(chan struct{})}
	defer v.Close()
	if _, err := v.Connect(ctx); !errors.Is(err, client.ErrQueueFull) {
		t.Fatalf("rejected request returned %v", err)
	}
	<-queue
	caller, stopCaller := context.WithCancel(ctx)
	failed := make(chan error, 1)
	go func() { _, err := v.Connect(caller); failed <- err }()
	select {
	case packet := <-queue:
		if request, ok := packet.(*packets.C2SCustomPayloadPlay); !ok || request.Channel != "voicechat:request_secret" {
			t.Fatalf("invalid secret request: %#v", packet)
		}
	case <-ctx.Done():
		t.Fatal("secret request was not retried after a full queue")
	}
	stopCaller()
	<-failed
}
