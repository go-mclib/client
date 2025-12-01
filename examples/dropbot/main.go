package main

import (
	"context"
	"flag"
	"os"
	"strings"

	mcclient "github.com/go-mclib/client/client"
	packets "github.com/go-mclib/data/go/773/java_packets"
	jp "github.com/go-mclib/protocol/java_protocol"
)

func main() {
	var addr string
	var verbose bool
	var username string
	var online bool
	var hasGravity bool
	var intervalSeconds int
	var interactive bool
	var treatTransferAsDisconnect bool

	flag.StringVar(&addr, "s", "localhost:25565", "server address (host:port)")
	flag.BoolVar(&verbose, "v", false, "verbose logging")
	flag.StringVar(&username, "u", "", "username (offline or online)")
	flag.BoolVar(&online, "online", true, "assume that the server is in online-mode")
	flag.BoolVar(&hasGravity, "gravity", true, "currently not implemented")
	flag.IntVar(&intervalSeconds, "interval", 5, "interval in seconds between dropping items")
	flag.BoolVar(&interactive, "i", false, "enable interactive mode with chat input")
	flag.BoolVar(&treatTransferAsDisconnect, "d", false, "treat server transfer as disconnect")
	flag.Parse()

	// mc client
	clientID := os.Getenv("AZURE_CLIENT_ID")
	mcClient := mcclient.NewClient(addr, username, verbose, online, hasGravity, clientID)
	mcClient.Interactive = interactive
	mcClient.TreatTransferAsDisconnect = treatTransferAsDisconnect
	mcClient.RegisterDefaultHandlers()

	// cheap - drop item in hand if any item in any container changes
	// since the bot doesnt open any container on its own,
	// we are almost always guaranteed to drop the item when it appears in inv
	// (only available container, unless server opens one for us)
	mcClient.RegisterHandler(func(c *mcclient.Client, pkt *jp.Packet) {
		if pkt.PacketID == packets.S2CContainerSetSlot.PacketID {
			c.DropItem(true)
		}
	})

	// in case we get kicked, abort
	mcClient.RegisterHandler(func(c *mcclient.Client, pkt *jp.Packet) {
		if pkt.PacketID == packets.S2CSystemChat.PacketID {
			var data packets.S2CSystemChatData
			if err := jp.BytesToPacketData(pkt.Data, &data); err == nil {
				if strings.Contains(data.Content.GetText(), "disconnect") {
					c.Logger.Printf("encountered disconnect msg: %v", data)
					c.Disconnect(true)
				}
			}
		}
	})

	if err := mcClient.ConnectAndStart(context.Background()); err != nil {
		mcClient.Logger.Println(err)
	}
}
