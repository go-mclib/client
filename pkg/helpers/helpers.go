package helpers

import (
	"context"
	"flag"
	"os"

	"github.com/go-mclib/client/pkg/client"
	"github.com/go-mclib/client/pkg/client/modules/chat"
	"github.com/go-mclib/client/pkg/client/modules/protocol"
	"github.com/go-mclib/client/pkg/client/modules/self"
	"github.com/go-mclib/client/pkg/client/modules/world"
)

// Flags holds common CLI flags for example bots.
type Flags struct {
	Address                   string
	Username                  string
	Verbose                   bool
	Online                    bool
	Interactive               bool
	TreatTransferAsDisconnect bool
	MaxReconnectAttempts      int
}

// RegisterFlags registers the standard CLI flags on the default flag set.
func RegisterFlags(f *Flags) {
	flag.StringVar(&f.Address, "s", "localhost:25565", "server address (host:port)")
	flag.StringVar(&f.Username, "u", "", "username (offline or online)")
	flag.BoolVar(&f.Verbose, "v", false, "verbose logging")
	flag.BoolVar(&f.Online, "online", true, "assume online-mode server")
	flag.BoolVar(&f.Interactive, "i", false, "enable interactive mode with chat input")
	flag.BoolVar(&f.TreatTransferAsDisconnect, "d", false, "treat server transfer as disconnect")
	flag.IntVar(&f.MaxReconnectAttempts, "reconnects", 5, "max reconnect attempts (-1 = infinite, 0 = none)")
}

// NewClient creates a client from parsed flags with default modules (protocol, self, world, chat).
func NewClient(f Flags) *client.Client {
	clientID := os.Getenv("AZURE_CLIENT_ID")
	c := client.New(f.Address, f.Username, f.Online)
	c.Verbose = f.Verbose
	c.ClientID = clientID
	c.Interactive = f.Interactive
	c.MaxReconnectAttempts = f.MaxReconnectAttempts

	proto := protocol.New()
	proto.TreatTransferAsDisconnect = f.TreatTransferAsDisconnect
	c.Register(proto)
	c.Register(self.New())
	c.Register(world.New())
	c.Register(chat.New())

	return c
}

// Run connects and starts the client, logging errors.
func Run(c *client.Client) {
	if err := c.ConnectAndStart(context.Background()); err != nil {
		c.Logger.Println(err)
	}
}
