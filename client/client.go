package client

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/go-mclib/client/chat"
	packets "github.com/go-mclib/data/go/772/java_packets"
	"github.com/go-mclib/protocol/auth"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/net_structures"
	"github.com/go-mclib/protocol/session_server"
)

const protocolVersion = 772 // 1.21.8

type Client struct {
	*jp.TCPClient

	// Configuration
	Host       string
	Port       uint16
	Username   string
	Verbose    bool
	OnlineMode bool

	// Runtime
	state               jp.State
	Handlers            []Handler
	Logger              *log.Logger
	OutgoingPacketQueue chan *jp.Packet

	// Auth/session
	LoginData     auth.LoginData
	SessionClient *session_server.SessionServerClient
	ChatSigner    *chat.ChatSigner
}

// NewClient creates a high-level client suitable for bots.
func NewClient(host string, port uint16, username string, verbose bool, onlineMode bool) *Client {
	logger := log.New(os.Stdout, "", log.LstdFlags)

	c := &Client{
		TCPClient:           jp.NewTCPClient(),
		Host:                host,
		Port:                port,
		Username:            username,
		Verbose:             verbose,
		OnlineMode:          onlineMode,
		state:               jp.StateHandshake,
		OutgoingPacketQueue: make(chan *jp.Packet, 100),
		Logger:              logger,
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
}

// ConnectAndStart connects, performs handshake/login, and enters the packet loop.
func (c *Client) ConnectAndStart(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", c.Host, c.Port)
	if err := c.Connect(addr); err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}

	if err := c.initializeAuth(ctx); err != nil {
		return err
	}

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

	c.state = jp.StateLogin
	c.SetState(c.state)

	uuid, _ := ns.NewUUID(c.LoginData.UUID)
	loginStartPacket, err := packets.C2SHello.WithData(packets.C2SHelloData{Name: ns.String(c.LoginData.Username), PlayerUuid: uuid})
	if err != nil {
		return fmt.Errorf("login start build: %w", err)
	}
	if err := c.WritePacket(loginStartPacket); err != nil {
		return fmt.Errorf("login start send: %w", err)
	}

	go func() {
		for pkt := range c.OutgoingPacketQueue {
			if err := c.WritePacket(pkt); err != nil {
				c.Logger.Println("error writing packet from queue:", err)
			}
		}
	}()

	for {
		pkt, err := c.ReadPacket()
		if err != nil {
			c.Logger.Println("read packet error:", err)
			return err
		}
		for _, handler := range c.Handlers {
			handler(c, pkt)
		}
	}
}

// Disconnect closes the connection to the server
func (c *Client) Disconnect() {
	c.TCPClient.Close()
}
