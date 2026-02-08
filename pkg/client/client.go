package client

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/go-mclib/client/pkg/chat"
	"github.com/go-mclib/client/pkg/tui"
	"github.com/go-mclib/protocol/auth"
	jp "github.com/go-mclib/protocol/java_protocol"
	session_server "github.com/go-mclib/protocol/java_protocol/session_server"
)

type Client struct {
	*jp.TCPClient

	// connection
	Address    string
	Username   string
	Verbose    bool
	OnlineMode bool
	ClientID   string
	Brand      string

	// reconnection
	MaxReconnectAttempts int
	shouldReconnect      bool

	// TUI
	Interactive bool
	MaxLogLines int

	Logger              *log.Logger
	OutgoingPacketQueue chan jp.Packet

	// auth/session (populated during connect)
	LoginData     auth.LoginData
	SessionClient *session_server.SessionServerClient
	ChatSigner    *chat.ChatSigner

	// modules
	modules       []Module
	modulesByName map[string]Module
	handlers      []Handler

	// populated after Connect()
	resolvedHost string
	resolvedPort string

	// private
	swarm      *Swarm
	tuiProgram *tea.Program
}

// ResolvedAddr returns the resolved host and port after Connect().
func (c *Client) ResolvedAddr() (host, port string) {
	return c.resolvedHost, c.resolvedPort
}

// New creates a minimal client. Register modules before calling ConnectAndStart.
func New(address, username string, onlineMode bool) *Client {
	return &Client{
		TCPClient:            jp.NewTCPClient(),
		Address:              address,
		Username:             username,
		OnlineMode:           onlineMode,
		Brand:                "vanilla",
		MaxReconnectAttempts: 5,
		OutgoingPacketQueue:  make(chan jp.Packet, 100),
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

// RegisterHandler appends a lightweight packet callback (escape hatch).
func (c *Client) RegisterHandler(h Handler) {
	c.handlers = append(c.handlers, h)
}

// SendPacket queues a packet for outgoing transmission.
func (c *Client) SendPacket(pkt jp.Packet) {
	c.OutgoingPacketQueue <- pkt
}

// SendChatMessage forwards to the chat module. Satisfies tui.ClientInterface.
func (c *Client) SendChatMessage(msg string) error {
	if m := c.Module("chat"); m != nil {
		if cms, ok := m.(ChatMessageSender); ok {
			return cms.SendMessage(msg)
		}
	}
	return fmt.Errorf("chat module not registered")
}

// SendCommand forwards to the chat module. Satisfies tui.ClientInterface.
func (c *Client) SendCommand(cmd string) error {
	if m := c.Module("chat"); m != nil {
		if cms, ok := m.(ChatMessageSender); ok {
			return cms.SendCommand(cmd)
		}
	}
	return fmt.Errorf("chat module not registered")
}

// GetUsername returns the client's username (satisfies tui.ClientInterface).
func (c *Client) GetUsername() string { return c.Username }

// GetAddress returns the server address (satisfies tui.ClientInterface).
func (c *Client) GetAddress() string { return c.Address }

// GetMaxLogLines returns the maximum log lines setting (satisfies tui.ClientInterface).
func (c *Client) GetMaxLogLines() int { return c.MaxLogLines }

// EnableInput enables the chat input in the TUI.
func (c *Client) EnableInput() {
	tui.EnableInput(c.tuiProgram)
}

// Disconnect closes the connection. If force is true, no reconnect is attempted.
func (c *Client) Disconnect(force bool) error {
	c.shouldReconnect = !force
	return c.TCPClient.Close()
}

// Swarm returns the swarm this client belongs to, or nil.
func (c *Client) Swarm() *Swarm { return c.swarm }

// ConnectAndStart connects, performs auth, and enters the module dispatch loop.
func (c *Client) ConnectAndStart(ctx context.Context) error {
	if c.Interactive {
		tuiProgram, writer := tui.Start(c)
		c.tuiProgram = tuiProgram
		c.Logger = log.New(writer, "", log.LstdFlags)

		defer func() {
			if c.tuiProgram != nil {
				c.tuiProgram.Quit()
				c.tuiProgram = nil
			}
		}()

		tuiDone := make(chan error, 1)
		go func() {
			_, err := tuiProgram.Run()
			tuiDone <- err
		}()

		clientDone := make(chan error, 1)
		go func() {
			clientDone <- c.runConnectionLoop(ctx)
		}()

		select {
		case err := <-tuiDone:
			if err != nil {
				return err
			}
			return c.Disconnect(true)
		case err := <-clientDone:
			return err
		}
	}

	return c.runConnectionLoop(ctx)
}

func (c *Client) runConnectionLoop(ctx context.Context) error {
	attempts := 0
	maxAttempts := c.MaxReconnectAttempts

	for {
		c.shouldReconnect = false
		err := c.connectAndStartOnce(ctx)
		if err == nil {
			return nil
		}

		c.Logger.Printf("connection error: %v", err)

		if !c.shouldReconnect || maxAttempts == 0 {
			c.Logger.Printf("not reconnecting, exiting...")
			time.Sleep(500 * time.Millisecond)
			return err
		}

		attempts++
		if maxAttempts > 0 && attempts > maxAttempts {
			c.Logger.Printf("max reconnect attempts (%d) reached, giving up", maxAttempts)
			time.Sleep(500 * time.Millisecond)
			return err
		}
		if maxAttempts == -1 {
			c.Logger.Printf("reconnecting in 3 seconds... (attempt %d/∞)", attempts)
		} else {
			c.Logger.Printf("reconnecting in 3 seconds... (attempt %d/%d)", attempts, maxAttempts)
		}

		time.Sleep(3 * time.Second)

		if maxAttempts == -1 {
			c.Logger.Printf("attempting to reconnect indefinitely... (attempt %d)", attempts)
		} else {
			c.Logger.Printf("attempting to reconnect... (attempt %d/%d)", attempts, maxAttempts)
		}
	}
}

func (c *Client) connectAndStartOnce(ctx context.Context) error {
	c.TCPClient = jp.NewTCPClient()
	c.TCPClient.EnableDebug(c.Verbose)

	// reset all modules
	for _, m := range c.modules {
		m.Reset()
	}

	// TCP connect
	resolvedHost, resolvedPort, err := c.Connect(c.Address)
	if err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}
	c.resolvedHost = resolvedHost
	c.resolvedPort = resolvedPort

	// auth
	if err := c.initializeAuth(ctx); err != nil {
		return err
	}

	// notify modules of connection
	for _, m := range c.modules {
		if ch, ok := m.(ConnectHandler); ok {
			ch.OnConnect()
		}
	}

	// outgoing queue worker
	go func() {
		for pkt := range c.OutgoingPacketQueue {
			if err := c.WritePacket(pkt); err != nil {
				c.Logger.Println("error writing packet from queue:", err)
			}
		}
	}()

	// packet loop
	for {
		wire, err := c.ReadWirePacket()
		if err != nil {
			c.Logger.Println("read packet error:", err)
			c.shouldReconnect = true
			return err
		}
		for _, m := range c.modules {
			m.HandlePacket(wire)
		}
		for _, h := range c.handlers {
			h(c, wire)
		}
	}
}
