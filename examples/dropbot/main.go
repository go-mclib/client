package main

import (
	"context"
	"flag"
	"net"
	"os"
	"strconv"

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

	flag.StringVar(&addr, "s", "localhost:25565", "server address (host:port)")
	flag.BoolVar(&verbose, "v", false, "verbose logging")
	flag.StringVar(&username, "u", "", "offline username (empty = Microsoft auth)")
	flag.BoolVar(&online, "online", true, "assume that the server is in online-mode")
	flag.BoolVar(&hasGravity, "gravity", true, "enable (cheap) gravity")
	flag.IntVar(&intervalSeconds, "interval", 5, "interval in seconds between dropping items")
	flag.Parse()

	// mc client
	host, port := parseAddr(addr)
	clientID := os.Getenv("AZURE_CLIENT_ID")
	mcClient := mcclient.NewClient(host, port, username, verbose, online, hasGravity, clientID)
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

	if err := mcClient.ConnectAndStart(context.Background()); err != nil {
		mcClient.Logger.Println(err)
	}
}

func parseAddr(addr string) (string, uint16) {
	host := addr
	port := uint16(25565)
	if h, p, err := net.SplitHostPort(addr); err == nil {
		host = h
		if convP, err := strconv.ParseUint(p, 10, 16); err == nil {
			port = uint16(convP)
		}
	}
	return host, port
}
