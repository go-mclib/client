package main

import (
	"context"
	"flag"
	"math/rand"
	"os"
	"time"

	mcclient "github.com/go-mclib/client/client"
	jp "github.com/go-mclib/protocol/java_protocol"
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
	mcClient.Interactive = interactive
	mcClient.TreatTransferAsDisconnect = treatTransferAsDisconnect
	mcClient.RegisterDefaultHandlers()

	go func() {
		for {
			if mcClient.GetState() != jp.StatePlay {
				time.Sleep(50 * time.Millisecond)
				continue
			}

			time.Sleep(100 * time.Millisecond)
			yaw := rand.Float64()*360 - 180  // -180 to +180
			pitch := rand.Float64()*180 - 90 // -90 to +90
			_ = mcClient.SetRotation(yaw, pitch)
		}
	}()

	if err := mcClient.ConnectAndStart(context.Background()); err != nil {
		mcClient.Logger.Println(err)
	}
}
