package client

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-mclib/protocol/auth"
	jp "github.com/go-mclib/protocol/javaproto"
	"github.com/go-mclib/protocol/sessionserver"
)

var (
	// ErrNotConnected is returned when a packet is sent without an active connection.
	ErrNotConnected = errors.New("client not connected")
	// ErrNoChatModule is returned by SendChatMessage/SendCommand without a chat module.
	ErrNoChatModule = errors.New("chat module not registered")

	errDial = errors.New("connect failed")
)

type Client struct {
	// connection
	Address    string
	Username   string
	Verbose    bool
	OnlineMode bool
	ClientID   string
	Brand      string

	// Client information sent during configuration. Configure before
	// connecting; New sets vanilla-like defaults.
	Locale       string
	ViewDistance int8
	MainHand     int32 // 0 = left, 1 = right

	// TreatTransferAsDisconnect treats a server transfer (S2CStartConfiguration
	// in play) as a disconnect instead of returning to configuration.
	TreatTransferAsDisconnect bool

	// reconnection: MaxReconnectAttempts counts consecutive failed attempts
	// (-1 = unlimited, 0 = never reconnect); ReconnectDelay is the backoff.
	MaxReconnectAttempts int
	ReconnectDelay       time.Duration

	Logger *log.Logger

	// modules
	modules       []Module
	modulesByName map[string]Module
	handlers      []Handler
	packetMu      sync.Mutex
	wireOnce      sync.Once
	wireErr       error

	// lifecycle callbacks
	onConnect    []func()
	onTransfer   []func()
	onPlay       []func()
	onDisconnect []func()

	// sessionMu guards the fields below against concurrent readers.
	sessionMu     sync.RWMutex
	session       *Session // nil while disconnected
	loginData     auth.LoginData
	authClient    *auth.Client // nil in offline mode
	sessionClient *sessionserver.Client
	stopRun       context.CancelFunc // cancels the running ConnectAndStart

	stopped     atomic.Bool // set by Disconnect(true): do not reconnect
	playReached atomic.Bool // the current connection entered play

	// block action sequence counter (matches vanilla SequencedPredictiveAction)
	blockSequence atomic.Int32

	swarm *Swarm
}

// Debugf logs a message only when Verbose is enabled.
func (c *Client) Debugf(format string, args ...any) {
	if c.Verbose {
		c.Logger.Printf("[DEBUG] "+format, args...)
	}
}

// Decode reads pkt into dst. Decode failures are logged in verbose mode, so
// codec drift is visible instead of silently dropping packets.
func (c *Client) Decode(pkt *jp.WirePacket, dst jp.Packet) bool {
	if err := pkt.ReadInto(dst); err != nil {
		c.Debugf("decode %T: %v", dst, err)
		return false
	}
	return true
}

// New creates a minimal client. Register modules before calling ConnectAndStart.
func New(address, username string, onlineMode bool) *Client {
	return &Client{
		Address:              address,
		Username:             username,
		OnlineMode:           onlineMode,
		Brand:                "vanilla",
		Locale:               "en_us",
		ViewDistance:         32,
		MainHand:             1,
		MaxReconnectAttempts: 5,
		ReconnectDelay:       3 * time.Second,
		Logger:               log.New(os.Stdout, "", log.LstdFlags),
		modulesByName:        make(map[string]Module),
	}
}

// Register adds a module to the client. Panics on duplicate name.
func (c *Client) Register(m Module) {
	if _, exists := c.modulesByName[m.Name()]; exists {
		panic("module already registered: " + m.Name())
	}
	c.modules = append(c.modules, m)
	c.modulesByName[m.Name()] = m
	m.Init(c)
}

// Module returns a registered module by name, or nil.
func (c *Client) Module(name string) Module {
	return c.modulesByName[name]
}

// wireModules resolves inter-module dependencies once, after all modules are
// registered. Order-independent: every module sees the full registry.
func (c *Client) wireModules() error {
	c.wireOnce.Do(func() {
		for _, m := range c.modules {
			if w, ok := m.(Wirer); ok {
				if err := w.Wire(); err != nil {
					c.wireErr = fmt.Errorf("module %q: %w", m.Name(), err)
					return
				}
			}
		}
	})
	return c.wireErr
}

// RegisterHandler appends a lightweight packet callback (escape hatch).
func (c *Client) RegisterHandler(h Handler) {
	c.handlers = append(c.handlers, h)
}

// WithPacketState runs a game-state update atomically with packet dispatch.
// Like vanilla's main thread, a movement tick cannot observe half a teleport,
// attribute update or entity update. Do not call this from a packet callback.
func (c *Client) WithPacketState(update func()) {
	c.packetMu.Lock()
	defer c.packetMu.Unlock()
	update()
}

// Lifecycle callbacks run on the packet loop; Session returns the session
// they belong to. OnConnect runs after the transport connected, OnPlay when
// configuration finished, OnTransfer when the server returns the client to
// configuration (the new session is already current) and OnDisconnect after
// the connection closed (Session returns nil).
func (c *Client) OnConnect(cb func())    { c.onConnect = append(c.onConnect, cb) }
func (c *Client) OnTransfer(cb func())   { c.onTransfer = append(c.onTransfer, cb) }
func (c *Client) OnPlay(cb func())       { c.onPlay = append(c.onPlay, cb) }
func (c *Client) OnDisconnect(cb func()) { c.onDisconnect = append(c.onDisconnect, cb) }

func (c *Client) fireConnect() {
	for _, cb := range c.onConnect {
		cb()
	}
}
func (c *Client) fireTransfer() {
	for _, cb := range c.onTransfer {
		cb()
	}
}
func (c *Client) firePlay() {
	c.playReached.Store(true)
	for _, cb := range c.onPlay {
		cb()
	}
}
func (c *Client) fireDisconnect() {
	for _, cb := range c.onDisconnect {
		cb()
	}
}

// Session returns the current session, or nil while disconnected.
func (c *Client) Session() *Session {
	c.sessionMu.RLock()
	defer c.sessionMu.RUnlock()
	return c.session
}

// LoginData returns the account of the current (or last) connection.
func (c *Client) LoginData() auth.LoginData {
	c.sessionMu.RLock()
	defer c.sessionMu.RUnlock()
	return c.loginData
}

// AuthClient returns the Microsoft/Mojang auth client of an online-mode
// login, or nil in offline mode.
func (c *Client) AuthClient() *auth.Client {
	c.sessionMu.RLock()
	defer c.sessionMu.RUnlock()
	return c.authClient
}

// beginSession makes a new session on l current, ending the previous one.
func (c *Client) beginSession(parent context.Context, l *link) *Session {
	s := newSession(parent, l)
	c.sessionMu.Lock()
	old := c.session
	c.session = s
	c.sessionMu.Unlock()
	if old != nil {
		old.cancel()
	}
	return s
}

// endSession ends the current session.
func (c *Client) endSession() {
	c.sessionMu.Lock()
	old := c.session
	c.session = nil
	c.sessionMu.Unlock()
	if old != nil {
		old.cancel()
	}
}

// transport returns the current connection's transport, or nil.
func (c *Client) transport() *jp.TCPClient {
	if s := c.Session(); s != nil {
		return s.link.transport
	}
	return nil
}

// WritePacket writes p to the current connection. Safe for concurrent use.
func (c *Client) WritePacket(p jp.Packet) error {
	t := c.transport()
	if t == nil {
		return ErrNotConnected
	}
	return t.WritePacket(p)
}

// Conn returns the current connection, or nil while disconnected.
func (c *Client) Conn() *jp.Conn {
	if t := c.transport(); t != nil {
		return t.Conn()
	}
	return nil
}

// State returns the protocol state of the current connection
// (StateHandshake while disconnected).
func (c *Client) State() jp.State {
	if t := c.transport(); t != nil {
		return t.State()
	}
	return jp.StateHandshake
}

// SendPacket queues a packet on the current session without blocking; see
// Session.Send. It returns ErrNotConnected without an active connection.
func (c *Client) SendPacket(pkt jp.Packet) error {
	s := c.Session()
	if s == nil {
		return ErrNotConnected
	}
	_, err := s.Send(context.Background(), pkt)
	return err
}

// NextBISequence returns the next sequence number for block/item actions.
func (c *Client) NextBISequence() int32 {
	return c.blockSequence.Add(1)
}

// SendChatMessage forwards to the chat module.
func (c *Client) SendChatMessage(msg string) error {
	if cms, ok := c.Module("chat").(ChatMessageSender); ok {
		return cms.SendMessage(msg)
	}
	return ErrNoChatModule
}

// SendCommand forwards to the chat module.
func (c *Client) SendCommand(cmd string) error {
	if cms, ok := c.Module("chat").(ChatMessageSender); ok {
		return cms.SendCommand(cmd)
	}
	return ErrNoChatModule
}

// Disconnect closes the current connection. If force is true, the client
// stops: no reconnect is attempted and ConnectAndStart returns nil.
func (c *Client) Disconnect(force bool) error {
	if force {
		c.stopped.Store(true)
	}
	c.sessionMu.RLock()
	s, stop := c.session, c.stopRun
	c.sessionMu.RUnlock()
	if force && stop != nil {
		stop()
	}
	if s == nil {
		return nil
	}
	return s.link.transport.Close()
}

// Swarm returns the swarm this client belongs to, or nil.
func (c *Client) Swarm() *Swarm { return c.swarm }

// ConnectAndStart connects, performs auth, and runs the module dispatch loop,
// reconnecting per MaxReconnectAttempts. It returns when ctx is cancelled (with
// ctx.Err()), after Disconnect(true) (with nil), or when reconnecting stops.
func (c *Client) ConnectAndStart(ctx context.Context) error {
	if err := c.wireModules(); err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.stopped.Store(false)
	c.sessionMu.Lock()
	c.stopRun = cancel
	c.sessionMu.Unlock()
	return c.runConnectionLoop(runCtx)
}

func (c *Client) runConnectionLoop(ctx context.Context) error {
	attempts := 0
	maxAttempts := c.MaxReconnectAttempts

	for {
		c.playReached.Store(false)
		err := c.connectAndStartOnce(ctx)
		if c.stopped.Load() {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		c.Logger.Printf("connection error: %v", err)

		if c.playReached.Load() {
			attempts = 0
		}
		// The first dial or auth failure is final; once a session existed or a
		// reconnect is in progress, every failure counts as an attempt.
		retryable := !errors.Is(err, errDial) || attempts > 0
		if !retryable || maxAttempts == 0 {
			c.Logger.Printf("not reconnecting, exiting...")
			return err
		}

		attempts++
		if maxAttempts > 0 && attempts > maxAttempts {
			c.Logger.Printf("max reconnect attempts (%d) reached, giving up", maxAttempts)
			return err
		}
		if maxAttempts == -1 {
			c.Logger.Printf("reconnecting in %v... (attempt %d/∞)", c.ReconnectDelay, attempts)
		} else {
			c.Logger.Printf("reconnecting in %v... (attempt %d/%d)", c.ReconnectDelay, attempts, maxAttempts)
		}

		timer := time.NewTimer(c.ReconnectDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			if c.stopped.Load() {
				return nil
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) connectAndStartOnce(ctx context.Context) error {
	// reset all modules and client state
	c.blockSequence.Store(0)
	for _, m := range c.modules {
		m.Reset()
	}

	// auth first: an interactive login can outlast the server's login timeout
	if err := c.initializeAuth(ctx); err != nil {
		return fmt.Errorf("%w: %w", errDial, err)
	}

	transport := jp.NewTCPClient()
	transport.EnableDebug(c.Verbose)
	host, port, err := transport.Connect(ctx, c.Address)
	if err != nil {
		return fmt.Errorf("%w: %w", errDial, err)
	}
	l := newLink(transport, host, port, c.Logger.Printf)
	defer l.close()
	stopClose := context.AfterFunc(ctx, l.close)
	defer stopClose()

	// connCtx outlives transfers; each session derives from it
	connCtx, cancelConn := context.WithCancel(ctx)
	defer cancelConn()
	c.beginSession(connCtx, l)

	c.startLogin()
	c.fireConnect()

	for {
		wire, err := transport.ReadWirePacket()
		if err != nil {
			// Unblock writes from tick callbacks before disconnect handlers wait
			// for those ticks to finish.
			l.close()
			c.Logger.Println("read packet error:", err)
			c.endSession()
			c.fireDisconnect()
			return err
		}
		c.WithPacketState(func() {
			// modules see each packet in the state it arrived in; the
			// handshake then performs any state transition it causes
			state := transport.State()
			for _, m := range c.modules {
				m.HandlePacket(wire)
			}
			for _, h := range c.handlers {
				h(c, wire)
			}
			c.handleHandshake(connCtx, state, wire)
		})
	}
}
