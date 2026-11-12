// commandbot joins a server and forwards each line from stdin to the server:
// lines starting with '/' are sent as commands, everything else as chat.
// type interactively or pipe a script of commands in.
package main

import (
	"bufio"
	"flag"
	"os"
	"strings"
	"sync"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/botcli"
	"github.com/go-mclib/client/modules/self"
)

func main() {
	var f botcli.Flags
	botcli.RegisterFlags(&f)
	flag.Parse()

	c := botcli.NewClient(f)

	// start reading stdin once, after the first spawn (OnSpawn also fires on respawn)
	self.From(c).OnSpawn(sync.OnceFunc(func() { go readInput(c) }))

	botcli.Run(c, f)
}

// readInput forwards each stdin line: '/'-prefixed lines as commands, the rest as chat.
func readInput(c *client.Client) {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}

		var err error
		if strings.HasPrefix(line, "/") {
			err = c.SendCommand(line)
		} else {
			err = c.SendChatMessage(line)
		}
		if err != nil {
			c.Logger.Printf("send failed: %v", err)
		}
	}
}
