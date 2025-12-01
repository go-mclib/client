package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"regexp"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-mclib/client/client"
	packets "github.com/go-mclib/data/go/773/java_packets"
	jp "github.com/go-mclib/protocol/java_protocol"
)

const (
	// greetStorePath is the path to the file that stores the greeted users
	// (if a player was already greeted on first known join, greet with welcome back message instead)
	greetStorePath = ".greeted_users.json"
	// scoreStorePath is the path to the file that stores player paintball scores
	scoreStorePath = ".paintball_scores.json"
)

var (
	spectatorCounter = atomic.Int32{}
	joinRegex        = regexp.MustCompile(`multiplayer\.player\.joined\s+\[(\w{1,16})\]`)
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
	flag.StringVar(&username, "u", "", "offline username (offline or online)")
	flag.BoolVar(&online, "online", true, "assume that the server is in online-mode")
	flag.BoolVar(&hasGravity, "gravity", true, "currently not implemented")
	flag.BoolVar(&interactive, "i", false, "enable interactive mode with chat input")
	flag.BoolVar(&treatTransferAsDisconnect, "d", false, "treat server transfer as disconnect")
	flag.Parse()

	clientID := os.Getenv("AZURE_CLIENT_ID")
	c := client.NewClient(addr, username, verbose, online, hasGravity, clientID)
	c.Interactive = interactive
	c.TreatTransferAsDisconnect = treatTransferAsDisconnect
	c.RegisterDefaultHandlers()

	gstore := newGreetStore(greetStorePath)
	gstore.Load()

	sstore := newScoreStore(scoreStorePath)
	sstore.Load()

	cmd := commandHandler{scoreStore: sstore}

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
						c.SendChatMessage(fmt.Sprintf("Welcome back, %s!", name))
					} else {
						c.SendChatMessage(fmt.Sprintf("Welcome, %s o/", name))
						gstore.Mark(name)
						gstore.Save()
					}
				}
			}
		}

		// commands and score tracking
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

		// paintball score tracking
		if pkt.PacketID == packets.S2CSystemChat.PacketID {
			var d packets.S2CSystemChatData
			if err := jp.BytesToPacketData(pkt.Data, &d); err == nil {
				text := d.Content.GetText()
				if sstore.ProcessChatMessage(c, text) {
					sstore.Save()
				}
			}
		}

		// gamemode change (just testing random stuff on our paintball server)
		if pkt.PacketID == packets.S2CGameEvent.PacketID {
			var d packets.S2CGameEventData
			if err := jp.BytesToPacketData(pkt.Data, &d); err != nil {
				log.Println("error parsing gamemode change event")
			}
			if d.Event == 3 { // change gamemode
				if d.Value == 3 { // spectator
					spectatorCounter.Add(1)
					// chance of 10 %
					if rand.Intn(10) == 0 {
						c.SendChatMessage(fmt.Sprintf("wowee, you shot me %d times!", spectatorCounter.Load()))
					}
				}
			}
		}
	})

	go func() {
		var waveTime float64 = 0

		for {
			// do not send packets in e. g. config state, it kicks the client
			if c.GetState() != jp.StatePlay {
				time.Sleep(50 * time.Millisecond)
				continue
			}

			time.Sleep(50 * time.Millisecond)
			waveTime += 0.05                          // 50 ms
			yaw := 180 * math.Sin(waveTime*math.Pi/2) // Full 360 degree rotation (-180 to +180)
			pitch := 30 * math.Sin(waveTime*math.Pi)

			if err := c.SetRotation(yaw, pitch); err != nil {
				log.Println("error rotating:", err)
			}

			if err := c.Use(0); err != nil {
				log.Println("error using item:", err)
			}
		}
	}()

	// gracefully save greeted users
	done := make(chan error, 1)
	go func() { done <- c.ConnectAndStart(context.Background()) }()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)

	select {
	case <-sigc:
		gstore.Save()
		sstore.Save()
		_ = c.Close()
	case err := <-done:
		gstore.Save()
		sstore.Save()
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
