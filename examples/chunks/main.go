package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	mcclient "github.com/go-mclib/client/client"
	"github.com/go-mclib/data/pkg/data/blocks"
)

func main() {
	var addr string
	var verbose bool
	var username string
	var online bool
	var hasGravity bool
	var interactive bool
	var treatTransferAsDisconnect bool

	flag.StringVar(&addr, "s", "localhost:25565", "server address (host:port)")
	flag.BoolVar(&verbose, "v", false, "verbose logging")
	flag.StringVar(&username, "u", "", "username (offline or online)")
	flag.BoolVar(&online, "online", true, "assume that the server is in online-mode")
	flag.BoolVar(&hasGravity, "gravity", true, "currently not implemented")
	flag.BoolVar(&interactive, "i", false, "enable interactive mode with chat input")
	flag.BoolVar(&treatTransferAsDisconnect, "d", false, "treat server transfer as disconnect")
	flag.Parse()

	clientID := os.Getenv("AZURE_CLIENT_ID")
	mcClient := mcclient.NewClient(addr, username, verbose, online, hasGravity, clientID)
	mcClient.MaxReconnectAttempts = -1
	mcClient.Interactive = interactive
	mcClient.TreatTransferAsDisconnect = treatTransferAsDisconnect
	mcClient.RegisterDefaultHandlers()

	go func() {
		var lastBlockHash string
		for {
			time.Sleep(1 * time.Second)
			blockID, blockProperties := blocks.StateProperties(int(mcClient.World.GetBlock(0, -59, -5)))

			if fmt.Sprintf("%d:%s", blockID, blockProperties) == lastBlockHash {
				continue
			}
			lastBlockHash = fmt.Sprintf("%d:%s", blockID, blockProperties)

			blockName := blocks.BlockName(blockID)
			mcClient.SendChatMessage(fmt.Sprintf("Block at (0, -59, -5): %s{%s} (protocol ID: %d)", blockName, blockProperties, blockID))
		}
	}()

	if err := mcClient.ConnectAndStart(context.Background()); err != nil {
		mcClient.Logger.Println(err)
	}
}
