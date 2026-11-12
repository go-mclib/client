package client

import (
	"context"
	"fmt"
	"time"

	auth "github.com/go-mclib/protocol/auth"
	ns "github.com/go-mclib/protocol/javaproto/wire"
	"github.com/go-mclib/protocol/sessionserver"
)

func (c *Client) initializeAuth(ctx context.Context) error {
	if !c.OnlineMode {
		if c.Username == "" {
			c.Username = "GoMclibPlayer"
			c.Logger.Println("Warning: no username provided for offline mode, defaulting to 'GoMclibPlayer'")
		}
		c.sessionMu.Lock()
		c.loginData = auth.LoginData{Username: c.Username, UUID: ns.OfflinePlayerUUID(c.Username)}
		c.authClient, c.sessionClient = nil, nil
		c.sessionMu.Unlock()
		return nil
	}

	authClient := auth.NewClient(auth.Config{
		ClientID: c.ClientID,
		Username: c.Username,
	})
	loginCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ld, err := authClient.Login(loginCtx)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	if c.Username != "" && c.Username != ld.Username {
		c.Logger.Printf("Warning: authenticated as '%s' but requested username was '%s' (credentials may have changed)", ld.Username, c.Username)
	}
	c.Username = ld.Username

	c.sessionMu.Lock()
	c.loginData = ld
	c.authClient = authClient
	c.sessionClient = sessionserver.New(nil)
	c.sessionMu.Unlock()
	return nil
}
