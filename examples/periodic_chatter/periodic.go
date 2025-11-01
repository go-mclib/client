package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/conneroisu/groq-go"
	"github.com/go-mclib/client/client"
	packets "github.com/go-mclib/data/go/773/java_packets"
	jp "github.com/go-mclib/protocol/java_protocol"
)

const (
	chatInterval  = 60 * time.Second
	initialDelay  = 3 * time.Second
	maxMessageLen = 255
)

var (
	groqApiKey = os.Getenv("GROQ_API_KEY")
	prompt     = os.Getenv("GROQ_SYSTEM_PROMPT")
)

type periodicChatter struct {
	groqClient *groq.Client
	ready      chan bool
	cancel     context.CancelFunc
	started    bool
}

func (pc *periodicChatter) startChatting(ctx context.Context, c *client.Client) {
	<-pc.ready
	log.Printf("starting periodic chat every %v with initial delay %v", chatInterval, initialDelay)

	time.Sleep(initialDelay)
	select {
	case <-ctx.Done():
		return
	default:
		pc.sendPeriodicMessage(c)
	}

	ticker := time.NewTicker(chatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("stopping periodic chatter")
			return
		case <-ticker.C:
			pc.sendPeriodicMessage(c)
		}
	}
}

func (pc *periodicChatter) sendPeriodicMessage(c *client.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	response, err := pc.groqClient.ChatCompletion(ctx, groq.ChatCompletionRequest{
		Model: groq.ModelLlama318BInstant,
		Messages: []groq.ChatCompletionMessage{
			{
				Role:    "system",
				Content: prompt,
			},
			{
				Role:    "user",
				Content: prompt,
			},
		},
		Temperature: 0.9,
		MaxTokens:   50,
	})
	if err != nil {
		log.Printf("Groq API error: %v", err)
		return
	}

	if len(response.Choices) > 0 {
		aiResponse := response.Choices[0].Message.Content
		aiResponse = sanitizeChatMessage(aiResponse)

		if err := c.SendChatMessage(aiResponse); err != nil {
			log.Printf("Failed to send chat message: %v", err)
		} else {
			log.Printf("Bot sent periodic message: %s", aiResponse)
		}
	}
}

func main() {
	var addr string
	var verbose bool
	var username string
	var online bool
	var hasGravity bool

	flag.StringVar(&addr, "s", "localhost:25565", "server address (host:port)")
	flag.BoolVar(&verbose, "v", false, "verbose logging")
	flag.StringVar(&username, "u", "", "offline username (offline or online)")
	flag.BoolVar(&online, "online", true, "assume that the server is in online-mode")
	flag.BoolVar(&hasGravity, "gravity", true, "currently not implemented")
	flag.Parse()

	if groqApiKey == "" {
		log.Fatal("GROQ_API_KEY environment variable is not set")
	}
	if prompt == "" {
		log.Fatal("no prompt, set env var `GROQ_SYSTEM_PROMPT`")
	}

	groqClient, err := groq.NewClient(groqApiKey)
	if err != nil {
		log.Fatalf("Failed to initialize Groq client: %v", err)
	}

	host, port := parseAddr(addr)
	clientID := os.Getenv("AZURE_CLIENT_ID")

	mcClient := client.NewClient(host, port, username, verbose, online, hasGravity, clientID)
	mcClient.RegisterDefaultHandlers()

	chatter := &periodicChatter{
		groqClient: groqClient,
		ready:      make(chan bool, 1),
	}

	mcClient.RegisterHandler(func(c *client.Client, pkt *jp.Packet) {
		if pkt.PacketID == packets.S2CSystemChat.PacketID || pkt.PacketID == packets.S2CPlayerChat.PacketID || pkt.PacketID == packets.S2CDisguisedChat.PacketID {
			if !chatter.started {
				select {
				case chatter.ready <- true:
					log.Println("first chat msg, ready to chat")
					chatter.started = true
				default:
				}
			}
		}
	})

	done := make(chan error, 1)
	go func() { done <- mcClient.ConnectAndStart(context.Background()) }()

	mcClient.RegisterHandler(func(c *client.Client, pkt *jp.Packet) {
		if pkt.PacketID == packets.S2CStartConfiguration.PacketID {
			if chatter.cancel != nil {
				log.Println("server transfer detected, resetting periodic chatter")
				chatter.cancel()
				chatter.started = false
				chatter.ready = make(chan bool, 1)
			}
			ctx, cancel := context.WithCancel(context.Background())
			chatter.cancel = cancel
			go func() {
				chatter.startChatting(ctx, mcClient)
			}()
		}
	})

	// graceful shutdown
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)

	select {
	case <-sigc:
		log.Println("Shutting down...")
		mcClient.Disconnect()
	case err := <-done:
		if err != nil {
			log.Fatal(err)
		}
	}
}

// sanitizeChatMessage removes control characters and limits message length
func sanitizeChatMessage(msg string) string {
	var result strings.Builder
	for _, r := range msg {
		if r >= 32 && r != 127 && r != '\n' && r != '\r' && r != '\t' {
			result.WriteRune(r)
		} else if r == '\n' || r == '\r' || r == '\t' {
			result.WriteRune(' ')
		}
	}

	cleaned := strings.TrimSpace(result.String())
	cleaned = strings.Join(strings.Fields(cleaned), " ")

	if len(cleaned) > maxMessageLen {
		cleaned = cleaned[:maxMessageLen-3] + "..."
	}

	return cleaned
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
