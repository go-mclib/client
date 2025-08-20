package main

import (
	"fmt"
	"strings"

	"github.com/go-mclib/client/client"
)

type commandHandler struct{}

func (commandHandler) handle(c *client.Client, sender string, msg string) bool {
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
		c.SendChatMessage("Commands: !help, !say <message>, !whoami, !disconnect")
		return true
	case "say":
		if arg == "" {
			c.SendChatMessage("Usage: !say <message>")
			return true
		}
		c.SendChatMessage(arg)
		return true
	case "whoami":
		c.SendChatMessage(fmt.Sprintf("You are %s", sender))
		return true
	case "disconnect":
		c.Disconnect()
		return true
	}

	return false
}
