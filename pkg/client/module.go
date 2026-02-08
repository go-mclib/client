package client

import jp "github.com/go-mclib/protocol/java_protocol"

// Module is a pluggable game-state component.
type Module interface {
	// Name returns a unique key for this module (e.g. "protocol", "self", "world", "chat").
	Name() string
	// Init is called once when the module is registered on a client.
	// Store the *Client reference for later use.
	Init(c *Client)
	// HandlePacket is called for every incoming packet in any connection state.
	HandlePacket(pkt *jp.WirePacket)
	// Reset is called on reconnect to clear module state.
	Reset()
}

// ConnectHandler is optionally implemented by modules that need to act
// after TCP connection is established but before the packet loop starts.
// The protocol module uses this to send handshake + login start.
type ConnectHandler interface {
	OnConnect()
}

// ChatSessionSender is optionally implemented by the chat module.
// The protocol module calls this during config -> play transition.
type ChatSessionSender interface {
	SendChatSessionData() error
}

// ChatMessageSender is optionally implemented by the chat module.
// The client forwards SendChatMessage/SendCommand through this for TUI support.
type ChatMessageSender interface {
	SendMessage(msg string) error
	SendCommand(cmd string) error
}

// Handler is a lightweight packet callback for one-off matching.
type Handler func(c *Client, pkt *jp.WirePacket)
