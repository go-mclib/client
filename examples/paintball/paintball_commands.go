package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-mclib/client/client"
)

type commandHandler struct {
	scoreStore *scoreStore
}

func (ch commandHandler) handle(c *client.Client, sender string, msg string) bool {
	if !strings.HasPrefix(msg, "!") {
		return false
	}
	parts := strings.SplitN(strings.TrimSpace(msg[1:]), " ", 2)
	cmd := strings.ToLower(parts[0])
	arg := ""
	if len(parts) == 2 {
		arg = strings.TrimSpace(parts[1])
	}

	switch cmd {
	case "help":
		c.SendChatMessage("Commands: !help, !score [player], !top")
		return true
	case "disconnect":
		c.Disconnect(true)
		return true
	case "score":
		player := arg
		if player == "" {
			player = sender
		}
		score := ch.scoreStore.GetScore(player)
		c.SendChatMessage(fmt.Sprintf("%s has %d points", player, score))
		return true
	case "top":
		scores := ch.scoreStore.GetTopScores()
		for i, score := range scores {
			c.SendChatMessage(score)
			time.Sleep(1 * time.Second)

			if i >= 10 {
				break
			}
		}

		return true
	}

	return false
}
