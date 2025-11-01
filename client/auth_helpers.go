package client

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/go-mclib/client/chat"
	auth "github.com/go-mclib/protocol/auth"
	mc_crypto "github.com/go-mclib/protocol/crypto"
	ns "github.com/go-mclib/protocol/net_structures"
	session_server "github.com/go-mclib/protocol/session_server"
)

// initializeAuth performs online or offline auth and prepares chat/session structures
func (c *Client) initializeAuth(ctx context.Context) error {
	if !c.OnlineMode { // offline mode
		if c.Username == "" {
			c.Username = "GoMclibPlayer"
			c.Logger.Println("Warning: no username provided for offline mode, defaulting to 'GoMclibPlayer'")
		}
		uuid := mc_crypto.MinecraftSHA1(c.Username)
		c.LoginData = auth.LoginData{Username: c.Username, UUID: uuid}
		return nil
	}

	// online mode - use Microsoft auth with credential caching
	authClient := auth.NewClient(auth.AuthClientConfig{
		ClientID: c.ClientID,
		Username: c.Username, // pass username for cache lookup (empty string means new account)
	})
	loginCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ld, err := authClient.Login(loginCtx)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	c.LoginData = ld

	// Warn if authenticated username differs from requested username
	if c.Username != "" && c.Username != ld.Username {
		c.Logger.Printf("Warning: authenticated as '%s' but requested username was '%s' (credentials may have changed)", ld.Username, c.Username)
	}
	c.Username = ld.Username // update to canonical (authenticated) username

	cert, err := auth.FetchMojangCertificate(ld.AccessToken)
	if err != nil {
		return fmt.Errorf("fetch certificate: %w", err)
	}

	c.ChatSigner = chat.NewChatSigner()
	c.ChatSigner.SetKeys(cert.PrivateKey, cert.PublicKey)

	playerUUID, err := ns.NewUUID(ld.UUID)
	if err != nil {
		return fmt.Errorf("parse player uuid: %w", err)
	}
	c.ChatSigner.PlayerUUID = playerUUID
	c.ChatSigner.AddPlayerPublicKey(playerUUID, cert.PublicKey)

	// use SPKI DER
	c.ChatSigner.X509PublicKey = cert.PublicKeyBytes

	mojangSig, err := base64.StdEncoding.DecodeString(cert.Certificate.PublicKeySignatureV2)
	if err != nil {
		return fmt.Errorf("decode mojang signature: %w", err)
	}
	c.ChatSigner.SessionKey = mojangSig
	if expiry, err := time.Parse(time.RFC3339Nano, cert.Certificate.ExpiresAt); err == nil {
		c.ChatSigner.KeyExpiry = expiry
	}

	c.SessionClient = session_server.NewSessionServerClient()
	return nil
}
