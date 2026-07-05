package main

import (
	"flag"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/helpers"
	"github.com/go-mclib/data/packet_ids"
	jp "github.com/go-mclib/protocol/java_protocol"
)

func main() {
	var f helpers.Flags
	helpers.RegisterFlags(&f)
	flag.Parse()

	c := helpers.NewClient(f)

	// cheap - drop item in hand if any container slot changes
	c.RegisterHandler(func(c *client.Client, pkt *jp.WirePacket) {
		if pkt.PacketID == packet_ids.S2CContainerSetSlotID {
			c.DropItem(true)
		}
	})

	helpers.Run(c)
}
