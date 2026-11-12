// Package voicechat is a send-only Simple Voice Chat client: a client.Module
// that obtains the per-connection secret over the plugin channel and an
// AES-GCM UDP session that transmits Opus microphone frames.
package voicechat

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/packetids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
)

// ModuleName is the name the module registers under.
const ModuleName = "voicechat"

const (
	compatibility            = 20
	magic                    = 0xff
	packetMic                = 1
	packetAuthenticate       = 5
	packetAuthenticateAck    = 6
	packetPing               = 7
	packetKeepAlive          = 8
	packetConnectionCheck    = 9
	packetConnectionCheckAck = 10
	maxPayload               = 2048
	maxOpus                  = 1275
	handshakeTimeout         = 5 * time.Second
	retryInterval            = time.Second
)

var errClosed = errors.New("voice chat transport closed")

// Module owns one Minecraft connection's secret and UDP session. A server
// issues its secret only once, so cancelling a caller must not discard it.
type Module struct {
	mu         sync.Mutex
	lifecycle  sync.Mutex
	client     *client.Client
	connection *connection
}

type connection struct {
	ctx         context.Context
	cancel      context.CancelFunc
	host        string
	send        func(context.Context, jp.Packet) (<-chan error, error)
	requested   bool
	secretReady chan struct{}
	secret      *credentials
	secretErr   error
	attempt     *attempt
	session     *Session
}

type attempt struct {
	done    chan struct{}
	session *Session
	err     error
}

type credentials struct {
	key       [16]byte
	player    [16]byte
	port      int
	mtu       int
	keepAlive time.Duration
	host      string
}

var _ client.Module = (*Module)(nil)

// New returns a voice chat module; register it on the client and call
// Connect once the client is in play.
func New() *Module           { return &Module{} }
func (*Module) Name() string { return ModuleName }

func (v *Module) Init(c *client.Client) {
	v.client = c
	c.OnPlay(v.onPlay)
	c.OnTransfer(v.Close)
	c.OnDisconnect(v.Close)
}

func (v *Module) onPlay() {
	v.lifecycle.Lock()
	defer v.lifecycle.Unlock()
	v.closeConnection()
	session := v.client.Session()
	if session == nil {
		return
	}
	host, _ := session.ResolvedAddr()
	if conn := session.Conn(); conn != nil && conn.RemoteAddr() != nil {
		if remote, _, err := net.SplitHostPort(conn.RemoteAddr().String()); err == nil {
			host = remote
		}
	}
	ctx, cancel := context.WithCancel(session.Context())
	v.mu.Lock()
	v.connection = &connection{
		ctx: ctx, cancel: cancel, host: host, send: session.Send,
		secretReady: make(chan struct{}),
	}
	v.mu.Unlock()
}

func (v *Module) Reset() { v.Close() }

// Close ends the current connection's session; Connect fails until the next play state.
func (v *Module) Close() {
	v.lifecycle.Lock()
	defer v.lifecycle.Unlock()
	v.closeConnection()
}

func (v *Module) closeConnection() {
	v.mu.Lock()
	c := v.connection
	v.connection = nil
	var a *attempt
	if c != nil {
		c.cancel()
		a = c.attempt
	}
	v.mu.Unlock()
	if c == nil {
		return
	}
	// The attempt owns a not-yet-published UDP socket; cancellation closes it.
	if a != nil {
		<-a.done
	}
	v.mu.Lock()
	session := c.session
	v.mu.Unlock()
	if session != nil {
		session.Close()
	}
}

func (v *Module) HandlePacket(pkt *jp.WirePacket) {
	// Server-controlled plugin payloads are parsed inside the client dispatch loop.
	defer func() {
		if p := recover(); p != nil {
			log.Printf("voice chat handler panic on packet %#x: %v\n%s", pkt.PacketID, p, debug.Stack())
		}
	}()
	if v.client.State() != jp.StatePlay || pkt.PacketID != packetids.S2CCustomPayloadPlayID {
		return
	}
	// Decode the channel without letting a malicious length allocate memory.
	data := []byte(pkt.Data)
	n, used, ok := readLength(data, 256)
	if !ok || n > len(data)-used || string(data[used:used+n]) != "voicechat:secret" {
		return
	}
	secret, err := parseSecret(data[used+n:])
	v.mu.Lock()
	defer v.mu.Unlock()
	c := v.connection
	if c == nil || !c.requested || c.secret != nil || c.secretErr != nil {
		return
	}
	c.secret, c.secretErr = secret, err
	close(c.secretReady)
}

// Connect returns the live session, negotiating one if needed. Cancelling ctx
// abandons only this caller; the single-use secret request is kept.
func (v *Module) Connect(ctx context.Context) (*Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v.mu.Lock()
	c := v.connection
	if c == nil {
		v.mu.Unlock()
		return nil, errors.New("voice chat unavailable: Minecraft is not in play")
	}
	session := c.session
	v.mu.Unlock()
	if session != nil && session.available() == nil {
		if c.ctx.Err() != nil {
			return nil, errClosed
		}
		return session, nil
	}
	v.mu.Lock()
	if v.connection != c {
		v.mu.Unlock()
		return nil, errClosed
	}
	a := c.attempt
	if a == nil {
		a = &attempt{done: make(chan struct{})}
		c.attempt = a
		go v.negotiate(c, a)
	}
	v.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, errClosed
	case <-a.done:
		if c.ctx.Err() != nil {
			return nil, errClosed
		}
		return a.session, a.err
	}
}

func (v *Module) negotiate(c *connection, a *attempt) {
	ctx, cancel := context.WithTimeout(c.ctx, handshakeTimeout)
	defer cancel()
	v.mu.Lock()
	old := c.session
	c.session = nil
	v.mu.Unlock()
	if old != nil {
		old.Close()
	}
	s, err := v.openSession(ctx, c)
	if c.ctx.Err() != nil && err == nil {
		err = errClosed
	}
	if err != nil && s != nil {
		s.Close()
	}
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, errClosed) {
		log.Printf("voice chat unavailable: %v", err)
	}
	v.mu.Lock()
	if err == nil {
		c.session = s
		a.session = s
	}
	a.err = err
	c.attempt = nil
	close(a.done)
	v.mu.Unlock()
}

func (v *Module) openSession(ctx context.Context, c *connection) (*Session, error) {
	v.mu.Lock()
	request := !c.requested
	c.requested = true
	v.mu.Unlock()
	if request {
		// Bound to the connection, not this attempt: once queued, the request
		// is written unless the connection ends, so it is never repeated.
		if _, err := c.send(c.ctx, &packets.C2SCustomPayloadPlay{
			Channel: "voicechat:request_secret", Data: []byte{0, 0, 0, compatibility},
		}); err != nil {
			v.mu.Lock()
			c.requested = false
			v.mu.Unlock()
			return nil, fmt.Errorf("requesting voice secret: %w", err)
		}
	}
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("no voice secret (missing/incompatible Simple Voice Chat server): %w", ctx.Err())
	case <-c.secretReady:
	}
	if c.secretErr != nil {
		return nil, c.secretErr
	}
	endpoint, err := resolveEndpoint(c.secret.host, c.host, c.secret.port)
	if err != nil {
		return nil, err
	}
	s, err := dialSession(ctx, endpoint, c.secret)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// The fixed prefix is key | port | UUID | codec | MTU | distance | keepalive |
// groups. Strings use Minecraft VarInt byte lengths, not Go's signed varints.
func parseSecret(data []byte) (*credentials, error) {
	const fixed = 54
	if len(data) < fixed+2 || len(data) > fixed+3+32767*3+1 {
		return nil, errors.New("invalid voice secret size")
	}
	n, used, ok := readLength(data[fixed:], 32767*3)
	if !ok || fixed+used+n+1 != len(data) || !utf8.Valid(data[fixed+used:fixed+used+n]) {
		return nil, errors.New("invalid voice host encoding")
	}
	s := &credentials{
		port:      int(int32(binary.BigEndian.Uint32(data[16:20]))),
		mtu:       int(int32(binary.BigEndian.Uint32(data[37:41]))),
		keepAlive: time.Duration(int32(binary.BigEndian.Uint32(data[49:53]))) * time.Millisecond,
		host:      string(data[fixed+used : fixed+used+n]),
	}
	copy(s.key[:], data[:16])
	copy(s.player[:], data[20:36])
	// MTU is the reference encoder's maximum Opus payload, not the UDP size.
	s.mtu = min(max(s.mtu, 256), maxOpus)
	s.keepAlive = max(s.keepAlive, 100*time.Millisecond)
	return s, nil
}

func resolveEndpoint(override, gameHost string, port int) (string, error) {
	host := gameHost
	if override != "" {
		if n, err := strconv.ParseInt(override, 10, 32); err == nil {
			if n > 0 && n <= 65535 {
				port = int(n)
			}
		} else {
			u, err := url.Parse("voicechat://" + override)
			if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
				return "", errors.New("invalid voice server endpoint override")
			}
			host = u.Hostname()
			if u.Port() != "" {
				n, err := strconv.Atoi(u.Port())
				if err != nil || n < 0 || n > 65535 {
					return "", errors.New("invalid voice server port override")
				}
				if n > 0 {
					port = n
				}
			}
		}
	}
	if host == "" || port < 1 || port > 65535 || strings.ContainsAny(host, "/?#@") {
		return "", errors.New("invalid voice server endpoint")
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func readLength(data []byte, limit int) (int, int, bool) {
	var value uint32
	for i := range min(5, len(data)) {
		b := data[i]
		if i == 4 && b > 7 {
			return 0, 0, false
		}
		value |= uint32(b&127) << (7 * i)
		if b&128 == 0 {
			return int(value), i + 1, value <= uint32(limit)
		}
	}
	return 0, 0, false
}

// Session is an authenticated UDP transport. A single receive goroutine drives
// handshake retries and liveness deadlines. All writes share fixed buffers
// under mu; the caller alone paces audio.
type Session struct {
	mu            sync.Mutex
	conn          net.Conn
	crypto        cipher.AEAD
	secret        *credentials
	ready         chan struct{}
	done          chan struct{}
	state         byte
	err           error
	lastKeepAlive time.Time
	sequence      uint64
	plain         [maxPayload]byte
	frame         [maxPayload + 32]byte
}

func dialSession(ctx context.Context, endpoint string, secret *credentials) (*Session, error) {
	block, err := aes.NewCipher(secret.key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp", endpoint)
	if err != nil {
		return nil, fmt.Errorf("opening voice UDP socket: %w", err)
	}
	s := &Session{
		conn: conn, crypto: aead, secret: secret, ready: make(chan struct{}),
		done: make(chan struct{}), state: packetAuthenticate,
	}
	cancelled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		s.fail(ctx.Err())
		close(cancelled)
	})
	go s.receiveLoop()
	select {
	case <-ctx.Done():
		s.fail(ctx.Err())
	case <-s.ready:
	}
	if !stop() {
		<-cancelled
	}
	if err := s.available(); err != nil {
		s.Close()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("voice UDP handshake: %w", err)
	}
	log.Printf("voice chat connected to %s", conn.RemoteAddr())
	return s, nil
}

func (s *Session) available() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.availableLocked()
}

func (s *Session) availableLocked() error {
	if s.err != nil {
		return s.err
	}
	if s.state != packetConnectionCheckAck {
		return errors.New("voice chat not authenticated")
	}
	if time.Since(s.lastKeepAlive) >= s.secret.keepAlive*10 {
		return errors.New("voice chat keepalive timed out")
	}
	return nil
}

func (s *Session) fail(err error) {
	// Closing before locking interrupts an in-flight UDP write immediately.
	_ = s.conn.Close()
	s.mu.Lock()
	if s.err == nil {
		s.err = err
		if s.state != packetConnectionCheckAck {
			close(s.ready)
		}
	}
	s.mu.Unlock()
}

// Close stops the transport and waits for its receive goroutine.
func (s *Session) Close() {
	s.fail(errClosed)
	<-s.done
}

// SendAudio transmits one Opus frame; an empty frame marks end of speech.
func (s *Session) SendAudio(opus []byte) error {
	s.mu.Lock()
	if err := s.availableLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	if len(opus) > s.secret.mtu {
		s.mu.Unlock()
		return fmt.Errorf("Opus frame is %d bytes; voice server permits %d", len(opus), s.secret.mtu)
	}
	body := binary.AppendUvarint(s.plain[1:1], uint64(len(opus)))
	body = append(body, opus...)
	body = binary.BigEndian.AppendUint64(body, s.sequence)
	body = append(body, 0) // whispering=false
	err := s.sendLocked(packetMic, body)
	if err == nil {
		s.sequence++
	}
	s.mu.Unlock()
	if err != nil {
		s.fail(err)
	}
	return err
}

func (s *Session) send(packet byte, body []byte) error {
	s.mu.Lock()
	err := s.sendLocked(packet, body)
	s.mu.Unlock()
	return err
}

func (s *Session) sendLocked(packet byte, body []byte) error {
	if s.err != nil {
		return s.err
	}
	if len(body)+1+12+16 > maxPayload {
		return errors.New("voice payload exceeds protocol limit")
	}
	s.plain[0] = packet
	copy(s.plain[1:], body)
	frame := s.frame[:1]
	frame[0] = magic
	frame = append(frame, s.secret.player[:]...)
	frame = binary.AppendUvarint(frame, uint64(12+len(body)+1+16))
	nonceAt := len(frame)
	frame = frame[:nonceAt+12]
	if _, err := rand.Read(frame[nonceAt:]); err != nil {
		return err
	}
	frame = s.crypto.Seal(frame, frame[nonceAt:], s.plain[:len(body)+1], nil)
	if err := s.conn.SetWriteDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
		return err
	}
	_, err := s.conn.Write(frame)
	return err
}

func (s *Session) receiveLoop() {
	defer close(s.done)
	var buffer [4096]byte
	var auth [32]byte
	copy(auth[:16], s.secret.player[:])
	copy(auth[16:], s.secret.key[:])
	var nextRetry time.Time
	for {
		s.mu.Lock()
		state, failed, last := s.state, s.err, s.lastKeepAlive
		s.mu.Unlock()
		if failed != nil {
			return
		}
		now := time.Now()
		deadline := nextRetry
		if state == packetConnectionCheckAck {
			deadline = last.Add(s.secret.keepAlive * 10)
			if !now.Before(deadline) {
				s.fail(errors.New("voice chat keepalive timed out"))
				return
			}
		} else if !now.Before(nextRetry) {
			var body []byte
			if state == packetAuthenticate {
				body = auth[:]
			}
			if err := s.send(state, body); err != nil {
				s.fail(err)
				return
			}
			nextRetry = now.Add(retryInterval)
			deadline = nextRetry
		}
		if err := s.conn.SetReadDeadline(deadline); err != nil {
			s.fail(err)
			return
		}
		n, err := s.conn.Read(buffer[:])
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			s.fail(err)
			return
		}
		payload := s.parseInbound(buffer[:n])
		if len(payload) == 0 {
			continue
		}
		packet, body := payload[0], payload[1:]
		s.mu.Lock()
		if s.err != nil {
			s.mu.Unlock()
			return
		}
		// Bodyless control packets may carry authenticated padding. Like the
		// Java client, ignore it instead of rejecting a valid acknowledgement.
		switch {
		case packet == packetAuthenticateAck && s.state == packetAuthenticate:
			s.state = packetConnectionCheck
			nextRetry = time.Time{}
		case packet == packetConnectionCheckAck && s.state == packetConnectionCheck:
			s.state = packetConnectionCheckAck
			s.lastKeepAlive = time.Now()
			close(s.ready)
		case packet == packetKeepAlive:
			s.lastKeepAlive = time.Now()
			err = s.sendLocked(packetKeepAlive, nil)
		case packet == packetPing:
			err = s.sendLocked(packetPing, body)
		}
		s.mu.Unlock()
		if err != nil {
			s.fail(err)
			return
		}
	}
}

func (s *Session) parseInbound(frame []byte) []byte {
	if len(frame) < 2 || frame[0] != magic {
		return nil
	}
	n, used, ok := readLength(frame[1:], maxPayload)
	if !ok || n < 12+16+1 || 1+used+n != len(frame) {
		return nil
	}
	payload := frame[1+used:]
	// Open in place: no receive-side allocation, and invalid tags never reach handlers.
	plain, err := s.crypto.Open(payload[12:12], payload[:12], payload[12:], nil)
	if err != nil {
		return nil
	}
	return plain
}
