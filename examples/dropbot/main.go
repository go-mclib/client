package main

import (
	"flag"

	"github.com/go-mclib/client/pkg/client"
	"github.com/go-mclib/client/pkg/helpers"
	"github.com/go-mclib/data/pkg/data/packet_ids"
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
