package main

import (
	"flag"
	"fmt"
	"math"
	"strings"

	"github.com/go-mclib/client/pkg/client"
	"github.com/go-mclib/client/pkg/client/modules/chat"
	"github.com/go-mclib/client/pkg/client/modules/entities"
	"github.com/go-mclib/client/pkg/client/modules/pathfinding"
	"github.com/go-mclib/client/pkg/client/modules/self"
	"github.com/go-mclib/client/pkg/client/modules/world"
	"github.com/go-mclib/client/pkg/helpers"
	"github.com/go-mclib/data/pkg/data/packet_ids"
	"github.com/go-mclib/data/pkg/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
)

func main() {
	var f helpers.Flags
	helpers.RegisterFlags(&f)
	flag.Parse()

	f.MaxReconnectAttempts = -1

	c := helpers.NewClient(f)

	// register extended modules (order matters: dependencies first)
	c.Register(entities.New())
	c.Register(pathfinding.New())

	ents := entities.From(c)
	pf := pathfinding.From(c)
	ch := chat.From(c)

	pf.OnNavigationComplete(func(reached bool) {
		if reached {
			c.Logger.Println("arrived at destination")
			ch.SendMessage("I'm here!")
		} else {
			c.Logger.Println("navigation failed (stuck or no path)")
			ch.SendMessage("I got stuck!")
		}
	})

	// listen for "come" in player chat by intercepting the raw packet
	// to get the sender UUID and match it to a tracked entity
	c.RegisterHandler(func(c *client.Client, pkt *jp.WirePacket) {
		if pkt.PacketID != packet_ids.S2CPlayerChatID {
			return
		}

		var d packets.S2CPlayerChat
		if err := pkt.ReadInto(&d); err != nil {
			return
		}

		msg := strings.TrimSpace(string(d.Body.Content))
		if !strings.EqualFold(msg, "come") {
			return
		}

		senderName := d.ChatType.Name.Text
		senderUUID := [16]byte(d.Sender)

		// find the sender's entity
		e := ents.GetEntityByUUID(senderUUID)
		if e == nil {
			c.Logger.Printf("%s said 'come' but is not in render distance", senderName)
			ch.SendMessage(fmt.Sprintf("I can't see you, %s!", senderName))
			return
		}

		goalX := int(math.Floor(e.X))
		goalY := int(math.Floor(e.Y))
		goalZ := int(math.Floor(e.Z))

		s := self.From(c)
		w := world.From(c)
		c.Logger.Printf("%s said 'come' — entity at (%.2f, %.2f, %.2f) goal (%d, %d, %d)", senderName, e.X, e.Y, e.Z, goalX, goalY, goalZ)
		c.Logger.Printf("bot at (%.2f, %.2f, %.2f), chunks loaded: %d", float64(s.X), float64(s.Y), float64(s.Z), len(w.Chunks))
		ch.SendMessage(fmt.Sprintf("Coming to you, %s!", senderName))

		if err := pf.NavigateTo(goalX, goalY, goalZ); err != nil {
			c.Logger.Printf("pathfinding error: %v", err)
			ch.SendMessage(fmt.Sprintf("Can't find a path to you, %s!", senderName))
		}
	})

	helpers.Run(c)
}
