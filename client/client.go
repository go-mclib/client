package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-mclib/client/chat"
	packets "github.com/go-mclib/data/go/772/java_packets"
	"github.com/go-mclib/protocol/auth"
	mc_crypto "github.com/go-mclib/protocol/crypto"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/net_structures"
	"github.com/go-mclib/protocol/session_server"
)

const protocolVersion = 772 // 1.21.8

type Client struct {
	*jp.TCPClient

	// Configuration
	Host       string
	Port       uint16
	Username   string
	Verbose    bool
	OnlineMode bool

	// Runtime
	state         jp.State
	Handlers      []Handler
	Logger        *log.Logger
	OutgoingQueue chan *jp.Packet

	// Auth/session
	LoginData     auth.LoginData
	SessionClient *session_server.SessionServerClient
	ChatSigner    *chat.ChatSigner
}

// NewClient creates a high-level client suitable for bots.
func NewClient(host string, port int, username string, verbose bool, onlineMode bool) *Client {
	if port <= 0 || port > 65535 {
		port = 25565
	}

	logger := log.New(os.Stdout, "", log.LstdFlags)

	c := &Client{
		TCPClient:     jp.NewTCPClient(),
		Host:          host,
		Port:          uint16(port),
		Username:      username,
		Verbose:       verbose,
		OnlineMode:    onlineMode,
		state:         jp.StateHandshake,
		OutgoingQueue: make(chan *jp.Packet, 100),
		Logger:        logger,
	}

	c.TCPClient.EnableDebug(verbose)
	return c
}

// RegisterHandler appends a custom handler to be invoked for every incoming packet
func (c *Client) RegisterHandler(handler Handler) { c.Handlers = append(c.Handlers, handler) }

// RegisterDefaultHandlers loads built-in handlers that drive the client to play state
func (c *Client) RegisterDefaultHandlers() { c.RegisterHandler(defaultStateHandler) }

// ConnectAndStart connects, performs handshake/login, and enters the packet loop.
func (c *Client) ConnectAndStart(ctx context.Context) error {
	addr := fmt.Sprintf("%s:%d", c.Host, c.Port)
	if err := c.Connect(addr); err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}

	if err := c.initializeAuth(ctx); err != nil {
		return err
	}

	handshakePacket, err := packets.C2SIntention.WithData(packets.C2SIntentionData{
		ProtocolVersion: protocolVersion,
		ServerAddress:   ns.String(c.Host),
		ServerPort:      ns.UnsignedShort(c.Port),
		Intent:          2,
	})
	if err != nil {
		return fmt.Errorf("handshake build: %w", err)
	}
	if err := c.WritePacket(handshakePacket); err != nil {
		return fmt.Errorf("handshake send: %w", err)
	}

	c.state = jp.StateLogin
	c.SetState(c.state)

	uuid, _ := ns.NewUUID(c.LoginData.UUID)
	loginStartPacket, err := packets.C2SHello.WithData(packets.C2SHelloData{Name: ns.String(c.LoginData.Username), PlayerUuid: uuid})
	if err != nil {
		return fmt.Errorf("login start build: %w", err)
	}
	if err := c.WritePacket(loginStartPacket); err != nil {
		return fmt.Errorf("login start send: %w", err)
	}

	go func() {
		for pkt := range c.OutgoingQueue {
			if err := c.WritePacket(pkt); err != nil {
				c.Logger.Println("error writing packet from queue:", err)
			}
		}
	}()

	for {
		pkt, err := c.ReadPacket()
		if err != nil {
			c.Logger.Println("read packet error:", err)
			return err
		}
		for _, handler := range c.Handlers {
			handler(c, pkt)
		}
	}
}

// initializeAuth performs online or offline auth and prepares chat/session structures
func (c *Client) initializeAuth(ctx context.Context) error {
	if !c.OnlineMode || c.Username != "" { // offline
		if c.Username == "" {
			c.Username = "Player"
		}
		uuid := mc_crypto.MinecraftSHA1(c.Username)
		c.LoginData = auth.LoginData{Username: c.Username, UUID: uuid}
		return nil
	}

	// online mode
	authClient := auth.NewClient(auth.AuthClientConfig{ClientID: os.Getenv("AZURE_CLIENT_ID")})
	loginCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ld, err := authClient.Login(loginCtx)
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	c.LoginData = ld

	cert, err := fetchMojangCertificate(ld.AccessToken)
	if err != nil {
		return fmt.Errorf("fetch certificate: %w", err)
	}

	priv, err := parseRSAPrivateKey(cert.KeyPair.PrivateKey)
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}
	pub, err := parseRSAPublicKey(cert.KeyPair.PublicKey)
	if err != nil {
		return fmt.Errorf("parse public key: %w", err)
	}

	c.ChatSigner = chat.NewChatSigner()
	c.ChatSigner.SetKeys(priv, pub)

	playerUUID, err := ns.NewUUID(ld.UUID)
	if err != nil {
		return fmt.Errorf("parse player uuid: %w", err)
	}
	c.ChatSigner.PlayerUUID = playerUUID
	c.ChatSigner.AddPlayerPublicKey(playerUUID, pub)

	// SPKI DER
	if block, _ := pem.Decode([]byte(cert.KeyPair.PublicKey)); block != nil {
		if anyPub, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
			if rsaKey, ok := anyPub.(*rsa.PublicKey); ok {
				if der, err := x509.MarshalPKIXPublicKey(rsaKey); err == nil {
					c.ChatSigner.X509PublicKey = der
				}
			}
		}
	}

	mojangSig, err := base64.StdEncoding.DecodeString(cert.PublicKeySignatureV2)
	if err != nil {
		return fmt.Errorf("decode mojang signature: %w", err)
	}
	c.ChatSigner.SessionKey = mojangSig
	if expiry, err := time.Parse(time.RFC3339Nano, cert.ExpiresAt); err == nil {
		c.ChatSigner.KeyExpiry = expiry
	}

	c.SessionClient = session_server.NewSessionServerClient()
	return nil
}

// Helper API

func (c *Client) SendChatMessage(message string) {
	if c.ChatSigner != nil {
		// signed message
		saltBytes := make([]byte, 8)
		rand.Read(saltBytes)
		salt := int64(binary.BigEndian.Uint64(saltBytes))
		timestamp := time.Now()
		lastSeen := c.ChatSigner.GetLastSeenMessages(20)
		signedMsg, err := c.ChatSigner.SignMessage(message, timestamp, salt, lastSeen)
		if err != nil {
			c.Logger.Println("sign chat message:", err)
			return
		}
		ack := ns.FixedBitSet{Length: 20, Data: make([]byte, 3)}
		pkt, err := packets.C2SChat.WithData(packets.C2SChatData{
			Message:      ns.String(message),
			Timestamp:    ns.Long(timestamp.UnixMilli()),
			Salt:         ns.Long(salt),
			Signature:    ns.PrefixedOptional[ns.ByteArray]{Present: true, Value: ns.ByteArray(signedMsg.Signature)},
			MessageCount: ns.VarInt(len(lastSeen)),
			Acknowledged: ack,
			Checksum:     ns.Byte(0),
		})
		if err != nil {
			c.Logger.Println("build signed chat:", err)
			return
		}
		_ = c.WritePacket(pkt)
		return
	}

	// unsigned
	pkt, err := packets.C2SChat.WithData(packets.C2SChatData{
		Message:      ns.String(message),
		Timestamp:    ns.Long(time.Now().UnixMilli()),
		Salt:         ns.Long(0),
		Signature:    ns.PrefixedOptional[ns.ByteArray]{},
		MessageCount: ns.VarInt(0),
		Acknowledged: ns.FixedBitSet{Length: 20, Data: make([]byte, 3)},
		Checksum:     ns.Byte(0),
	})
	if err != nil {
		c.Logger.Println("build unsigned chat:", err)
		return
	}
	_ = c.WritePacket(pkt)
}

func (c *Client) sendChatSessionData() {
	if c.ChatSigner == nil {
		return
	}

	var sessionID ns.UUID
	rand.Read(sessionID[:])
	c.ChatSigner.SessionUUID = sessionID

	pub := c.ChatSigner.X509PublicKey
	if len(pub) == 0 {
		return
	}
	expiresAt := ns.Long(c.ChatSigner.KeyExpiry.UnixMilli())
	mojangSig := c.ChatSigner.SessionKey

	var buf bytes.Buffer
	buf.Write(sessionID[:])
	be := make([]byte, 8)
	binary.BigEndian.PutUint64(be, uint64(expiresAt))
	buf.Write(be)
	writeVarInt(&buf, len(pub))
	buf.Write(pub)
	writeVarInt(&buf, len(mojangSig))
	buf.Write(mojangSig)

	type manualPacketStruct struct{ Data ns.ByteArray }
	manualData := manualPacketStruct{Data: ns.ByteArray(buf.Bytes())}
	pkt, err := jp.NewPacket(jp.StatePlay, jp.C2S, 0x09).WithData(manualData)
	if err != nil {
		c.Logger.Println("build chat session packet:", err)
		return
	}
	_ = c.WritePacket(pkt)
}

func writeVarInt(buf *bytes.Buffer, value int) {
	for value >= 0x80 {
		buf.WriteByte(byte(value&0x7F | 0x80))
		value >>= 7
	}
	buf.WriteByte(byte(value))
}

type mojangKeyPair struct {
	PrivateKey string `json:"privateKey"`
	PublicKey  string `json:"publicKey"`
}
type mojangCertificate struct {
	ExpiresAt            string        `json:"expiresAt"`
	KeyPair              mojangKeyPair `json:"keyPair"`
	PublicKeySignature   string        `json:"publicKeySignature"`
	PublicKeySignatureV2 string        `json:"publicKeySignatureV2"`
	RefreshedAfter       string        `json:"refreshedAfter"`
}

func fetchMojangCertificate(accessToken string) (*mojangCertificate, error) {
	req, err := http.NewRequest("POST", "https://api.minecraftservices.com/player/certificates", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cert status %d", resp.StatusCode)
	}
	var cert mojangCertificate
	if err := json.NewDecoder(resp.Body).Decode(&cert); err != nil {
		return nil, err
	}
	return &cert, nil
}

func parseRSAPrivateKey(privateKeyPEM string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("decode private key pem")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rk, ok := k.(*rsa.PrivateKey); ok {
			return rk, nil
		}
		return nil, fmt.Errorf("private key not RSA")
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func parseRSAPublicKey(publicKeyPEM string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("decode public key pem")
	}
	if pub, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if rk, ok := pub.(*rsa.PublicKey); ok {
			return rk, nil
		}
	}
	return x509.ParsePKCS1PublicKey(block.Bytes)
}
