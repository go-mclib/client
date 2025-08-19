package main

import (
	"context"	
	"log"

	"github.com/go-mclib/client/client"
	packets "github.com/go-mclib/data/go/772/java_packets"
	jp "github.com/go-mclib/protocol/java_protocol"
)

func main() {
	c := client.NewClient("dyes.minehut.gg", 25565, "", false, true)
	c.RegisterDefaultHandlers()
	c.RegisterHandler(func(c *client.Client, pkt *jp.Packet) {
		if pkt.PacketID == packets.S2CLoginPlay.PacketID {
			c.SendChatMessage("hello")
		}
		if pkt.PacketID == packets.S2CPlayerChat.PacketID {
			log.Println(pkt.Data)
		}
	})
	if err := c.ConnectAndStart(context.Background()); err != nil {
		log.Fatal(err)
	}
}
