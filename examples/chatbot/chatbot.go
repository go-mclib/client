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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	grok "github.com/SimonMorphy/grok-go"
	"github.com/go-mclib/client/client"
	packets "github.com/go-mclib/data/go/772/java_packets"
	jp "github.com/go-mclib/protocol/java_protocol"
)

const (
	bufferDuration = 3 * time.Second
	maxMessageLen  = 256
)

var (
	grokApiKey = os.Getenv("GROQ_API_KEY")
	// format: <[RANK] USERNAME> MESSAGE
	playerMessageRegex = regexp.MustCompile(`^\s*<\[([^\]]+)\]\s+([^>]+)>\s*(.*)$`)
)

// handleChatMessage processes a chat message from a player
func handleChatMessage(c *client.Client, sender, message string, chatBuffer *ChatBuffer) {
	if sender == c.Username {
		return
	}

	chatBuffer.Add(sender, message)
}

func main() {
	var addr string
	var verbose bool
	var username string
	var online bool
	var hasGravity bool

	flag.StringVar(&addr, "s", "localhost:25565", "server address (host:port)")
	flag.BoolVar(&verbose, "v", false, "verbose logging")
	flag.StringVar(&username, "u", "", "offline username (empty = Microsoft auth)")
	flag.BoolVar(&online, "online", true, "assume that the server is in online-mode")
	flag.BoolVar(&hasGravity, "gravity", true, "enable (cheap) gravity")
	flag.Parse()

	// grok client
	if grokApiKey == "" {
		log.Fatal("GROQ_API_KEY is not set")
	}
	grokClient, err := grok.NewClient(grokApiKey)
	if err != nil {
		log.Fatalf("Failed to initialize Grok client: %v", err)
	}
	grokClient.BaseUrl = "https://api.groq.com/openai/v1/"

	// mc client
	host, port := parseAddr(addr)
	clientID := os.Getenv("AZURE_CLIENT_ID")
	mcClient := client.NewClient(host, port, username, verbose, online, hasGravity, clientID)
	mcClient.RegisterDefaultHandlers()

	// prompts
	botName := mcClient.Username
	systemPrompt := fmt.Sprintf(`You are %s, a Minecraft player in the game chat. You should ONLY respond when:
1. Someone mentions your name (%s) or mentions a "chatbot"
2. Someone directly addresses you or asks you a question
3. You're already part of an ongoing conversation

If you should NOT respond (random chatter, conversations between others that don't involve you, spam, etc.), respond with exactly "none" (lowercase, no quotes).

When you DO respond:
- Keep it brief (max 200 chars)
- Be chaotic but lovable
- Do not respond if you are not part of the conversation
- Do not involve yourself in every chat message, your responses should be occasional`, botName, botName)
	conversationHistory := []grok.ChatCompletionMessage{
		{
			Role:    "system",
			Content: systemPrompt,
		},
	}

	chatBuffer := &ChatBuffer{
		messages: make([]string, 0),
	}
	// process buffered messages every 3 seconds
	go func() {
		ticker := time.NewTicker(bufferDuration)
		defer ticker.Stop()

		for range ticker.C {
			messages := chatBuffer.GetAndClear()
			if len(messages) == 0 {
				continue
			}

			combinedInput := strings.Join(messages, "\n")
			conversationHistory = append(conversationHistory, grok.ChatCompletionMessage{
				Role:    "user",
				Content: combinedInput,
			})

			request := &grok.ChatCompletionRequest{
				Model:       "gemma2-9b-it",
				Messages:    conversationHistory,
				Temperature: 0.8,
				MaxTokens:   60,
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			response, err := grok.CreateChatCompletion(ctx, grokClient, request)
			cancel()

			if err != nil {
				log.Printf("Grok API error: %v", err)
				conversationHistory = conversationHistory[:len(conversationHistory)-1]
				continue
			}

			if len(response.Choices) > 0 {
				aiResponse := response.Choices[0].Message.Content
				aiResponse = strings.TrimSpace(aiResponse)

				if strings.ToLower(aiResponse) == "none" {
					log.Println("Bot chose not to respond")
					conversationHistory = append(conversationHistory, grok.ChatCompletionMessage{
						Role:    "assistant",
						Content: "[no response]",
					})
				} else {
					aiResponse = sanitizeChatMessage(aiResponse)

					if err := mcClient.SendChatMessage(aiResponse); err != nil {
						log.Printf("Failed to send chat message: %v", err)
					} else {
						log.Printf("Bot responded: %s", aiResponse)

						conversationHistory = append(conversationHistory, grok.ChatCompletionMessage{
							Role:    "assistant",
							Content: aiResponse,
						})
					}
				}

				if len(conversationHistory) > 21 { // 1 AI system + 20 AI messages
					conversationHistory = append(conversationHistory[:1], conversationHistory[len(conversationHistory)-20:]...)
				}
			}
		}
	}()

	// PlayerChat
	mcClient.RegisterHandler(func(c *client.Client, pkt *jp.Packet) {
		if pkt.PacketID == packets.S2CPlayerChat.PacketID {
			var d packets.S2CPlayerChatData
			if err := jp.BytesToPacketData(pkt.Data, &d); err == nil {
				sender := d.SenderName.GetText()
				message := string(d.Message)
				handleChatMessage(c, sender, message, chatBuffer)
			}
		}
	})

	// SystemChat
	mcClient.RegisterHandler(func(c *client.Client, pkt *jp.Packet) {
		if pkt.PacketID == packets.S2CSystemChat.PacketID {
			var d packets.S2CSystemChatData
			if err := jp.BytesToPacketData(pkt.Data, &d); err == nil {
				content := d.Content.GetText()

				if matches := playerMessageRegex.FindStringSubmatch(content); matches != nil {
					sender := matches[2]
					message := matches[3]
					handleChatMessage(c, sender, message, chatBuffer)
				}
			}
		}
	})

	// start client
	done := make(chan error, 1)
	go func() { done <- mcClient.ConnectAndStart(context.Background()) }()

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

// ChatBuffer is a buffer for chat messages, so we don't
// spam the Grok API with too many messages at once
type ChatBuffer struct {
	mu       sync.Mutex
	messages []string
}

// Add adds a message to the buffer
func (cb *ChatBuffer) Add(sender, message string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	formatted := fmt.Sprintf("<%s> %s", sender, message)
	cb.messages = append(cb.messages, formatted)
}

// GetAndClear gets and clears the messages from the buffer
func (cb *ChatBuffer) GetAndClear() []string {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if len(cb.messages) == 0 {
		return nil
	}

	msgs := make([]string, len(cb.messages))
	copy(msgs, cb.messages)
	cb.messages = cb.messages[:0]
	return msgs
}

// sanitizeChatMessage removes all control characters and excessive whitespace
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
