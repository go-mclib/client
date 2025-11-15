package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/go-mclib/client/chat"
	packets "github.com/go-mclib/data/go/773/java_packets"
	"github.com/go-mclib/protocol/auth"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/net_structures"
	"github.com/go-mclib/protocol/session_server"
)

const protocolVersion = 773 // 1.21.9-1.21.10

type Client struct {
	*jp.TCPClient

	// Server hostname/IP address to connect to
	Host string
	// Server port to use
	Port uint16
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
	// Maximum number of reconnect attempts on EOF or server disconnect/kick
	// 0 = no reconnect (default), -1 = infinite reconnects, >0 = specific number of attempts
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

	// Runtime
	Handlers            []Handler
	Logger              *log.Logger
	OutgoingPacketQueue chan *jp.Packet

	// Auth/session
	LoginData     auth.LoginData
	SessionClient *session_server.SessionServerClient
	ChatSigner    *chat.ChatSigner

	// Stores
	Self *SelfStore

	// Private
	shouldReconnect bool
	tuiProgram      *tea.Program
}

// NewClient creates a high-level client suitable for bots.
func NewClient(host string, port uint16, username string, verbose bool, onlineMode bool, hasGravity bool, clientID string) *Client {
	logger := log.New(os.Stdout, "", log.LstdFlags)

	c := &Client{
		TCPClient:                 jp.NewTCPClient(),
		Host:                      host,
		Port:                      port,
		Username:                  username,
		Verbose:                   verbose,
		OnlineMode:                onlineMode,
		HasGravity:                hasGravity,
		ClientID:                  clientID,
		MaxReconnectAttempts:      5,
		TreatTransferAsDisconnect: false,
		OutgoingPacketQueue:       make(chan *jp.Packet, 100),
		Logger:                    logger,
		Self:                      NewSelfStore(),
	}

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
}

// ConnectAndStart connects, performs handshake/login, and enters the packet loop.
func (c *Client) ConnectAndStart(ctx context.Context) error {
	// start TUI if interactive mode is enabled
	if c.Interactive {
		tuiProgram, writer := c.StartTUI()
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
			c.Disconnect()
			if err != nil {
				return err
			}
			return nil
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

	addr := fmt.Sprintf("%s:%d", c.Host, c.Port)
	if err := c.Connect(addr); err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}

	if err := c.initializeAuth(ctx); err != nil {
		return err
	}

	// hello (handshake)
	handshakePacket, err := packets.C2SIntention.WithData(packets.C2SIntentionData{
		ProtocolVersion: protocolVersion,
		ServerAddress:   ns.String(c.Host),
		ServerPort:      ns.UnsignedShort(c.Port),
		Intent:          2,
	})
	if err != nil {
		return fmt.Errorf("handshake build: %w", err)
	}
	if err := c.WritePacket(handshakePacket); err != nil {
		return fmt.Errorf("handshake send: %w", err)
	}

	c.SetState(jp.StateLogin)

	uuid, _ := ns.NewUUID(c.LoginData.UUID)
	loginStartPacket, err := packets.C2SHello.WithData(packets.C2SHelloData{Name: ns.String(c.LoginData.Username), PlayerUuid: uuid})
	if err != nil {
		return fmt.Errorf("login start build: %w", err)
	}
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
		pkt, err := c.ReadPacket()
		if err != nil {
			c.Logger.Println("read packet error:", err)
			if errors.Is(err, io.EOF) {
				c.shouldReconnect = true
			}
			return err
		}
		for _, handler := range c.Handlers {
			handler(c, pkt)
		}
	}
}

// Disconnect closes the connection to the server and triggers reconnect if enabled
func (c *Client) Disconnect() error {
	c.shouldReconnect = true
	return c.TCPClient.Close()
}
