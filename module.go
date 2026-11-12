package client

import jp "github.com/go-mclib/protocol/javaproto"

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

// ModuleAs returns the module registered under name as T, or the zero value
// when no module is registered or it has a different type (for example a
// wrapper registered under the same name).
func ModuleAs[T Module](c *Client, name string) T {
	m, _ := c.modulesByName[name].(T)
	return m
}

// Wirer is optionally implemented by modules that depend on sibling modules.
// Wire runs once after all modules are registered and before connecting, so a
// module can resolve typed handles to its dependencies and fail fast if a
// required one is missing. A returned error aborts startup.
type Wirer interface {
	Wire() error
}

// ChatMessageSender is optionally implemented by the chat module.
// The client forwards SendChatMessage/SendCommand through this.
type ChatMessageSender interface {
	SendMessage(msg string) error
	SendCommand(cmd string) error
}

// Handler is a lightweight packet callback for one-off matching.
type Handler func(c *Client, pkt *jp.WirePacket)
