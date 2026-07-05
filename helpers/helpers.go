package helpers

import (
	"context"
	"flag"
	"os"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/chat"
	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/physics"
	"github.com/go-mclib/client/modules/playerlist"
	"github.com/go-mclib/client/modules/protocol"
	"github.com/go-mclib/client/modules/registry"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/client/modules/world"
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

// RegisterFlags registers the standard CLI flags on the default flag set:
// -s server address, -u username, -v verbose logging, -online online-mode,
// -i interactive mode, -d treat server transfer as disconnect (e.g. minehut
// sending the player to a lobby), -reconnects max reconnect attempts.
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
	c.Register(playerlist.New())
	c.Register(registry.New())
	c.Register(collisions.New())
	c.Register(physics.New())

	return c
}

// Run connects and starts the client, logging errors.
func Run(c *client.Client) {
	if err := c.ConnectAndStart(context.Background()); err != nil {
		c.Logger.Println(err)
	}
}
