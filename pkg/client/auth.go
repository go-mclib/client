package client

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/go-mclib/client/pkg/chat"
	auth "github.com/go-mclib/protocol/auth"
	mc_crypto "github.com/go-mclib/protocol/crypto"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
	session_server "github.com/go-mclib/protocol/java_protocol/session_server"
)

func (c *Client) initializeAuth(ctx context.Context) error {
	if !c.OnlineMode {
		if c.Username == "" {
			c.Username = "GoMclibPlayer"
			c.Logger.Println("Warning: no username provided for offline mode, defaulting to 'GoMclibPlayer'")
		}
		uuid := mc_crypto.MinecraftSHA1(c.Username)
		c.LoginData = auth.LoginData{Username: c.Username, UUID: uuid}
		return nil
	}

	authClient := auth.NewClient(auth.AuthClientConfig{
		ClientID: c.ClientID,
		Username: c.Username,
	})
	loginCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ld, err := authClient.Login(loginCtx)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	c.LoginData = ld

	if c.Username != "" && c.Username != ld.Username {
		c.Logger.Printf("Warning: authenticated as '%s' but requested username was '%s' (credentials may have changed)", ld.Username, c.Username)
	}
	c.Username = ld.Username

	cert, err := auth.FetchMojangCertificate(ld.AccessToken)
	if err != nil {
		return fmt.Errorf("fetch certificate: %w", err)
	}

	c.ChatSigner = chat.NewChatSigner()
	c.ChatSigner.SetKeys(cert.PrivateKey, cert.PublicKey)

	playerUUID, err := ns.UUIDFromString(ld.UUID)
	if err != nil {
		return fmt.Errorf("parse player uuid: %w", err)
	}
	c.ChatSigner.PlayerUUID = playerUUID
	c.ChatSigner.AddPlayerPublicKey(playerUUID, cert.PublicKey)

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
