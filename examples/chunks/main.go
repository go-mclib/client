package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/go-mclib/client/pkg/client/modules/chat"
	"github.com/go-mclib/client/pkg/client/modules/world"
	"github.com/go-mclib/client/pkg/helpers"
	"github.com/go-mclib/data/pkg/data/blocks"
)

func main() {
	var f helpers.Flags
	helpers.RegisterFlags(&f)
	flag.Parse()

	f.MaxReconnectAttempts = -1

	c := helpers.NewClient(f)
	w := world.From(c)
	ch := chat.From(c)

	go func() {
		var lastBlockHash string
		for {
			time.Sleep(1 * time.Second)
			blockID, blockProperties := blocks.StateProperties(int(w.GetBlock(0, -59, -5)))

			hash := fmt.Sprintf("%d:%s", blockID, blockProperties)
			if hash == lastBlockHash {
				continue
			}
			lastBlockHash = hash

			blockName := blocks.BlockName(blockID)
			ch.SendMessage(fmt.Sprintf("Block at (0, -59, -5): %s{%s} (protocol ID: %d)", blockName, blockProperties, blockID))
		}
	}()

	helpers.Run(c)
}
