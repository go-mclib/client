package main

import (
	"flag"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/botcli"
	"github.com/go-mclib/client/modules/interaction"
	"github.com/go-mclib/data/packetids"
	jp "github.com/go-mclib/protocol/javaproto"
)

func main() {
	var f botcli.Flags
	botcli.RegisterFlags(&f)
	flag.Parse()

	c := botcli.NewClient(f)

	// cheap - drop item in hand if any container slot changes
	c.RegisterHandler(func(c *client.Client, pkt *jp.WirePacket) {
		if pkt.PacketID == packetids.S2CContainerSetSlotID {
			_ = interaction.DropHeldStack(c.WritePacket)
		}
	})

	botcli.Run(c, f)
}
