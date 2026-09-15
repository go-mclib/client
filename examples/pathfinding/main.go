package main

import (
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/helpers"
	"github.com/go-mclib/client/modules/chat"
	"github.com/go-mclib/client/modules/entities"
	"github.com/go-mclib/client/modules/pathfinding"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/client/modules/world"
	"github.com/go-mclib/data/packet_ids"
	"github.com/go-mclib/data/packets"
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
		// Coordinates need not be in render distance: terrain is streamed in
		// bounded segments while this one operation retains the ultimate goal.
		fields := strings.Fields(msg)
		if len(fields) == 4 && strings.EqualFold(fields[0], "goto") {
			var goal [3]float64
			for i := range goal {
				value, err := strconv.ParseFloat(fields[i+1], 64)
				if err != nil {
					ch.SendMessage("Usage: goto <x> <y> <z>")
					return
				}
				goal[i] = value
			}
			if err := pf.NavigateTo(goal[0], goal[1], goal[2]); err != nil {
				ch.SendMessage(fmt.Sprintf("Navigation rejected: %v", err))
			}
			return
		}
		if strings.EqualFold(msg, "stop") {
			pf.Stop()
			return
		}
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

		s := self.From(c)
		w := world.From(c)
		bx, by, bz := s.Position()
		c.Logger.Printf("%s said 'come' — entity at (%.2f, %.2f, %.2f)", senderName, e.X, e.Y, e.Z)
		c.Logger.Printf("bot at (%.2f, %.2f, %.2f), chunks loaded: %d", bx, by, bz, w.GetLoadedChunkCount())
		ch.SendMessage(fmt.Sprintf("Coming to you, %s!", senderName))

		if err := pf.NavigateTo(e.X, e.Y, e.Z); err != nil {
			c.Logger.Printf("pathfinding error: %v", err)
			ch.SendMessage(fmt.Sprintf("Can't find a path to you, %s!", senderName))
		}
	})

	helpers.Run(c)
}
