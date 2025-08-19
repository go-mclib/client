package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"regexp"
	"syscall"

	"github.com/go-mclib/client/client"
	packets "github.com/go-mclib/data/go/772/java_packets"
	jp "github.com/go-mclib/protocol/java_protocol"
)

const (
	// greetStorePath is the path to the file that stores the greeted users
	// (if a player was already greeted on first known join, greet with welcome back message instead)
	greetStorePath = ".greeted_users.json"
)

var joinRegex = regexp.MustCompile(`multiplayer\.player\.joined\s+\[(\w{1,16})\]`)

func main() {
	var addr string
	var verbose bool
	var username string
	var online bool

	flag.StringVar(&addr, "s", "localhost:25565", "server address (host:port)")
	flag.BoolVar(&verbose, "v", false, "verbose logging")
	flag.StringVar(&username, "u", "", "offline username (empty = Microsoft auth)")
	flag.BoolVar(&online, "online", true, "assume that the server is in online-mode")
	flag.Parse()

	host, port := parseAddr(addr)
	c := client.NewClient(host, port, username, verbose, online)
	c.RegisterDefaultHandlers()

	gstore := newGreetStore(greetStorePath)
	gstore.Load()

	cmd := commandHandler{}

	c.RegisterHandler(func(c *client.Client, pkt *jp.Packet) {
		// greet on join
		if pkt.PacketID == packets.S2CSystemChat.PacketID {
			var d packets.S2CSystemChatData
			if err := jp.BytesToPacketData(pkt.Data, &d); err == nil {
				text := d.Content.GetText()
				if name, ok := extractJoinUsername(text); ok {
					// do not greet self
					if name == c.Username {
						return
					}

					if gstore.Has(name) {
						c.SendChatMessage("Welcome back, " + name + "!")
					} else {
						c.SendChatMessage("Welcome, " + name + " o/ (note: i am a bot)")
						gstore.Mark(name)
						gstore.Save()
					}
				}
			}
		}

		// commands
		if pkt.PacketID == packets.S2CPlayerChat.PacketID {
			var d packets.S2CPlayerChatData
			if err := jp.BytesToPacketData(pkt.Data, &d); err == nil {
				sender := d.SenderName.GetText()
				msg := string(d.Message)
				_ = sender
				if cmd.handle(c, sender, msg) {
					return
				}
			}
		}
	})

	// gracefully save greeted users
	done := make(chan error, 1)
	go func() { done <- c.ConnectAndStart(context.Background()) }()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)

	select {
	case <-sigc:
		gstore.Save()
		_ = c.Close()
	case err := <-done:
		gstore.Save()
		if err != nil {
			log.Fatal(err)
		}
	}
}

func extractJoinUsername(text string) (string, bool) {
	m := joinRegex.FindStringSubmatch(text)
	if len(m) == 2 {
		return m[1], true
	}
	return "", false
}

func parseAddr(addr string) (string, int) {
	host := addr
	port := 25565
	if h, p, err := net.SplitHostPort(addr); err == nil {
		host = h
		var parsed int
		_, _ = fmt.Sscanf(p, "%d", &parsed)
		if parsed > 0 && parsed <= 65535 {
			port = parsed
		}
	}
	return host, port
}
