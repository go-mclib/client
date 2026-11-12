// Package botcli bootstraps command-line bots: standard flags, a client with
// the default modules, and an optional interactive terminal UI.
package botcli

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/internal/tui"
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

// NewClient creates a client from parsed flags with the default modules:
// protocol, self, world, chat, playerlist, registry, collisions and physics.
func NewClient(f Flags) *client.Client {
	c := client.New(f.Address, f.Username, f.Online)
	c.Verbose = f.Verbose
	c.ClientID = os.Getenv("AZURE_CLIENT_ID")
	c.MaxReconnectAttempts = f.MaxReconnectAttempts
	c.TreatTransferAsDisconnect = f.TreatTransferAsDisconnect

	c.Register(protocol.New())
	c.Register(self.New())
	c.Register(world.New())
	c.Register(chat.New())
	c.Register(playerlist.New())
	c.Register(registry.New())
	c.Register(collisions.New())
	c.Register(physics.New())

	return c
}

// Run connects and starts the client (interactively with -i), logging errors.
func Run(c *client.Client, f Flags) {
	var err error
	if f.Interactive {
		err = RunInteractive(context.Background(), c, 0)
	} else {
		err = c.ConnectAndStart(context.Background())
	}
	if err != nil {
		c.Logger.Println(err)
	}
}

// RunInteractive runs c like ConnectAndStart inside a terminal UI that shows
// the log (keeping at most maxLogLines lines, 0 = all) and, once the client
// reaches play, sends typed lines as chat messages or /commands. It replaces
// c.Logger and returns when the UI exits or the client stops.
func RunInteractive(ctx context.Context, c *client.Client, maxLogLines int) error {
	program := tui.New(tui.Config{
		Username:    c.Username,
		Address:     c.Address,
		MaxLogLines: maxLogLines,
		SendChat:    c.SendChatMessage,
		SendCommand: c.SendCommand,
		Quit:        func() { _ = c.Disconnect(true) },
	})
	c.Logger = log.New(program.Writer(), "", log.LstdFlags)
	c.OnPlay(program.EnableInput)

	tuiDone := make(chan error, 1)
	go func() { tuiDone <- program.Run() }()
	clientDone := make(chan error, 1)
	go func() { clientDone <- c.ConnectAndStart(ctx) }()

	select {
	case err := <-tuiDone:
		_ = c.Disconnect(true)
		if clientErr := <-clientDone; err == nil {
			err = clientErr
		}
		return err
	case err := <-clientDone:
		program.Quit()
		<-tuiDone
		return err
	}
}
