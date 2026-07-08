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
	"github.com/go-mclib/client/helpers"
	"github.com/go-mclib/client/modules/self"
)

func main() {
	var f helpers.Flags
	helpers.RegisterFlags(&f)
	flag.Parse()

	c := helpers.NewClient(f)

	// start reading stdin once, after the first spawn (OnSpawn also fires on respawn)
	var once sync.Once
	self.From(c).OnSpawn(func() {
		once.Do(func() { go readInput(c) })
	})

	helpers.Run(c)
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
