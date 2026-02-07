package client

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/go-mclib/client/chat"
	"github.com/go-mclib/client/tui"
	"github.com/go-mclib/data/pkg/packets"
	"github.com/go-mclib/protocol/auth"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
	"github.com/go-mclib/protocol/java_protocol/session_server"
)

const protocolVersion = 774 // 1.21.11

type Client struct {
	*jp.TCPClient

	// Server address to connect to (hostname/IP with optional port, e.g. "localhost:25565" or "example.com")
	Address string
	// Username behavior depends on OnlineMode:
	// - If OnlineMode=false: used as offline-mode username (defaults to "GoMclibPlayer" if empty)
	// - If OnlineMode=true && Username!="": looks up cached credentials for this username, falls back to auth if not found
	// - If OnlineMode=true && Username=="": performs fresh Microsoft auth and adds new account to cache
	Username string
	// Whether to log verbose output (raw packet data, can be noisy)
	Verbose bool
	// Whether to assume online-mode server. If true, uses Microsoft authentication with credential caching.
	OnlineMode bool
	// Whether to currently not implemented (currently not implemented, and has no effect, the bot hovers when in air)
	HasGravity bool // currently unused
	// Azure client ID for authentication
	ClientID string
	// Maximum number of reconnect attempts on EOF or server disconnect/kick.
	// 0 = no reconnect, -1 = infinite reconnects, >0 = specific number of attempts.
	// Default is 5
	MaxReconnectAttempts int
	// Whether to treat S2CStartConfiguration (server transfer in play state) as a disconnect, requiring reconnect.
	// This is useful for servers that transfer players to lobby/other server on disconnect/kick (e. g. Minehut and friends)
	// and you don't want the bot to hang out in the lobby, but instead attempt to reconnect to the original IP.
	//
	// Tip: if true and MaxReconnectAttempts == 0, the bot will exit on transfer.
	TreatTransferAsDisconnect bool
	// Whether to enable interactive mode with a chat bar at the bottom for sending messages/commands
	Interactive bool
	// Maximum number of log lines to keep in interactive mode.
	// 0 = unlimited (default), >0 = limit to this many lines
	MaxLogLines int
	// Whether to automatically respawn on death (default: true)
	AutoRespawn bool
	// Brand string sent to the server (default: "vanilla")
	//
	// Note: ACs can detect this
	Brand string

	// Runtime
	Handlers            []Handler
	Logger              *log.Logger
	OutgoingPacketQueue chan jp.Packet

	// Auth/session
	LoginData     auth.LoginData
	SessionClient *session_server.SessionServerClient
	ChatSigner    *chat.ChatSigner

	// Stores
	Self  *SelfStore
	World *WorldStore

	// Private
	shouldReconnect bool
	tuiProgram      *tea.Program
}

// NewClient creates a high-level client suitable for bots.
func NewClient(address string, username string, verbose bool, onlineMode bool, hasGravity bool, clientID string) *Client {
	logger := log.New(os.Stdout, "", log.LstdFlags)

	c := &Client{
		TCPClient:                 jp.NewTCPClient(),
		Address:                   address,
		Username:                  username,
		Verbose:                   verbose,
		OnlineMode:                onlineMode,
		HasGravity:                hasGravity,
		ClientID:                  clientID,
		MaxReconnectAttempts:      5,
		TreatTransferAsDisconnect: false,
		AutoRespawn:               true,
		Brand:                     "vanilla",
		OutgoingPacketQueue:       make(chan jp.Packet, 100),
		Logger:                    logger,
		Self:                      NewSelfStore(),
	}

	c.World = NewWorldStore(c)

	c.TCPClient.EnableDebug(verbose)
	return c
}

// RegisterHandler appends a custom handler to be invoked for every incoming packet
func (c *Client) RegisterHandler(handler Handler) {
	c.Handlers = append(c.Handlers, handler)
}

// RegisterDefaultHandlers loads built-in handlers that drive the client to play state
func (c *Client) RegisterDefaultHandlers() {
	c.RegisterHandler(defaultStateHandler)
	c.RegisterHandler(c.Self.HandlePacket)
	c.RegisterHandler(c.World.HandlePacket)
}

// ConnectAndStart connects, performs handshake/login, and enters the packet loop.
func (c *Client) ConnectAndStart(ctx context.Context) error {
	// start TUI if interactive mode is enabled
	if c.Interactive {
		tuiProgram, writer := tui.Start(c)
		c.tuiProgram = tuiProgram
		c.Logger = log.New(writer, "", log.LstdFlags)

		// ensure TUI is always cleaned up on exit
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
			// TUI exited (user pressed Ctrl+C), ensure client is disconnected
			if err != nil {
				return err
			}

			return c.Disconnect(true)
		case err := <-clientDone:
			// client exited (error/disconnect)
			return err
		}
	}

	return c.runConnectionLoop(ctx)
}

func (c *Client) runConnectionLoop(ctx context.Context) error {
	attempts := 0
	maxAttempts := c.MaxReconnectAttempts

	for {
		c.shouldReconnect = false // just (re)connected, reset
		err := c.connectAndStartOnce(ctx)
		if err == nil {
			return nil
		}

		// log error
		c.Logger.Printf("connection error: %v", err)

		// should reconnect
		if !c.shouldReconnect || maxAttempts == 0 {
			c.Logger.Printf("not reconnecting, exiting...")
			time.Sleep(500 * time.Millisecond) // give TUI time to display error
			return err
		}

		// reconnect attempts
		attempts++
		if maxAttempts > 0 && attempts > maxAttempts {
			c.Logger.Printf("max reconnect attempts (%d) reached, giving up", maxAttempts)
			time.Sleep(500 * time.Millisecond) // give TUI time to display error
			return err
		}
		if maxAttempts == -1 {
			c.Logger.Printf("reconnecting in 3 seconds... (attempt %d/∞)", attempts)
		} else {
			c.Logger.Printf("reconnecting in 3 seconds... (attempt %d/%d)", attempts, maxAttempts)
		}

		// delay reconnect
		time.Sleep(3 * time.Second)

		if maxAttempts == -1 {
			c.Logger.Printf("attempting to reconnect indefinitely... (attempt %d)", attempts)
		} else {
			c.Logger.Printf("attempting to reconnect... (attempt %d/%d)", attempts, maxAttempts)
		}
	}
}

func (c *Client) connectAndStartOnce(ctx context.Context) error {
	// reset TCP client for fresh connection
	c.TCPClient = jp.NewTCPClient()
	c.TCPClient.EnableDebug(c.Verbose)

	// clear world data on reconnect
	c.World.Clear()

	// connect and get canonical host/port
	resolvedHost, resolvedPort, err := c.Connect(c.Address)
	if err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}

	// init auth
	if err := c.initializeAuth(ctx); err != nil {
		return err
	}

	// hello (handshake)
	handshakePort, err := strconv.Atoi(resolvedPort)
	if err != nil {
		return fmt.Errorf("parse port: %w", err)
	}
	handshakePacket := &packets.C2SIntention{
		ProtocolVersion: protocolVersion,
		ServerAddress:   ns.String(resolvedHost),
		ServerPort:      ns.Uint16(handshakePort),
		Intent:          2,
	}
	if err := c.WritePacket(handshakePacket); err != nil {
		return fmt.Errorf("handshake send: %w", err)
	}

	c.SetState(jp.StateLogin)

	uuid, _ := ns.UUIDFromString(c.LoginData.UUID)
	loginStartPacket := &packets.C2SHello{Name: ns.String(c.LoginData.Username), PlayerUuid: uuid}
	if err := c.WritePacket(loginStartPacket); err != nil {
		return fmt.Errorf("login start send: %w", err)
	}

	// out
	go func() {
		for pkt := range c.OutgoingPacketQueue {
			if err := c.WritePacket(pkt); err != nil {
				c.Logger.Println("error writing packet from queue:", err)
			}
		}
	}()

	// in
	for {
		wire, err := c.ReadWirePacket()
		if err != nil {
			c.Logger.Println("read packet error:", err)
			c.shouldReconnect = true
			return err
		}
		for _, handler := range c.Handlers {
			handler(c, wire)
		}
	}
}

// Disconnect closes the connection to the server and triggers reconnect, if enabled.
// If force is true, will disconnect without reconnecting
func (c *Client) Disconnect(force bool) error {
	c.shouldReconnect = !force
	return c.TCPClient.Close()
}

// GetUsername returns the client's username (implements tui.ClientInterface)
func (c *Client) GetUsername() string {
	return c.Username
}

// GetAddress returns the server address (implements tui.ClientInterface)
func (c *Client) GetAddress() string {
	return c.Address
}

// GetMaxLogLines returns the maximum log lines setting (implements tui.ClientInterface)
func (c *Client) GetMaxLogLines() int {
	return c.MaxLogLines
}

// EnableInput enables the chat input in the TUI
func (c *Client) EnableInput() {
	tui.EnableInput(c.tuiProgram)
}
