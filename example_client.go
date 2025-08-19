package main

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
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
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

var chatSigner *chat.ChatSigner

type MojangKeyPair struct {
	PrivateKey string `json:"privateKey"`
	PublicKey  string `json:"publicKey"`
}

type MojangCertificate struct {
	ExpiresAt            string        `json:"expiresAt"`
	KeyPair              MojangKeyPair `json:"keyPair"`
	PublicKeySignature   string        `json:"publicKeySignature"`
	PublicKeySignatureV2 string        `json:"publicKeySignatureV2"`
	RefreshedAfter       string        `json:"refreshedAfter"`
}

func main() {
	var serverAddr string
	var username string
	var verbose bool

	flag.StringVar(&serverAddr, "server", "localhost:25565", "Server address (host:port)")
	flag.StringVar(&username, "username", "", "Username for offline mode")
	flag.BoolVar(&verbose, "v", false, "Enable verbose packet logging")
	flag.Parse()

	offlineMode := username != ""
	if !offlineMode {
		log.Println("Online mode - authenticating...")
		runOnlineMode(serverAddr, verbose)
	} else {
		runOfflineMode(serverAddr, username, verbose)
	}
}

func runOfflineMode(serverAddr, username string, verbose bool) {
	log.Printf("Connecting to %s in offline mode as %s...\n", serverAddr, username)

	loginData := auth.LoginData{
		Username: username,
		UUID:     mc_crypto.MinecraftSHA1(username),
	}

	runClient(serverAddr, verbose, loginData, nil)
}

func fetchMojangCertificate(accessToken string) (*MojangCertificate, error) {
	req, err := http.NewRequest("POST", "https://api.minecraftservices.com/player/certificates", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch certificate: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("certificate request failed with status %d", resp.StatusCode)
	}

	var cert MojangCertificate
	if err := json.NewDecoder(resp.Body).Decode(&cert); err != nil {
		return nil, fmt.Errorf("failed to parse certificate response: %w", err)
	}

	return &cert, nil
}

func runOnlineMode(serverAddr string, verbose bool) {
	log.Println("Setting up authentication...")

	authClient := auth.NewClient(auth.AuthClientConfig{
		ClientID: os.Getenv("AZURE_CLIENT_ID"),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	loginData, err := authClient.Login(ctx)
	if err != nil {
		log.Fatalf("Authentication failed: %v", err)
	}

	log.Printf("Authenticated as %s (UUID: %s)\n", loginData.Username, loginData.UUID)

	// Fetch Mojang signing certificate
	log.Println("Fetching Mojang signing certificate...")
	cert, err := fetchMojangCertificate(loginData.AccessToken)
	if err != nil {
		log.Fatalf("Failed to fetch Mojang certificate: %v", err)
	}

	// Parse the RSA keys from PEM format
	privateKey, err := parseRSAPrivateKey(cert.KeyPair.PrivateKey)
	if err != nil {
		log.Fatalf("Failed to parse private key: %v", err)
	}

	publicKey, err := parseRSAPublicKey(cert.KeyPair.PublicKey)
	if err != nil {
		log.Fatalf("Failed to parse public key: %v", err)
	}

	// Parse certificate expiry time
	expiryTime, err := time.Parse(time.RFC3339Nano, cert.ExpiresAt)
	if err != nil {
		log.Fatalf("Failed to parse certificate expiry time: %v", err)
	}

	// Initialize chat signing system
	chatSigner = chat.NewChatSigner()
	chatSigner.SetKeys(privateKey, publicKey)

	// Set player UUID
	playerUUID, err := ns.NewUUID(loginData.UUID)
	if err != nil {
		log.Fatalf("Failed to parse player UUID: %v", err)
	}
	chatSigner.PlayerUUID = playerUUID
	chatSigner.AddPlayerPublicKey(playerUUID, publicKey)

	// Store public key in SPKI DER format as required by protocol
	block, _ := pem.Decode([]byte(cert.KeyPair.PublicKey))
	if block != nil {
		// Convert to proper SPKI DER format
		if rsaPubKey, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
			if rsaKey, ok := rsaPubKey.(*rsa.PublicKey); ok {
				properSpkiBytes, err := x509.MarshalPKIXPublicKey(rsaKey)
				if err == nil {
					chatSigner.X509PublicKey = properSpkiBytes
				}
			}
		}
	}

	// Store Mojang signature V2 (512 bytes)
	mojangSigBytes, err := base64.StdEncoding.DecodeString(cert.PublicKeySignatureV2)
	if err != nil {
		log.Fatalf("Failed to decode Mojang signature: %v", err)
	}
	chatSigner.SessionKey = mojangSigBytes
	chatSigner.KeyExpiry = expiryTime

	sessionClient := session_server.NewSessionServerClient()
	runClient(serverAddr, verbose, loginData, sessionClient)
}

func parseRSAPrivateKey(privateKeyPEM string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block containing private key")
	}

	// Try PKCS8 format first (modern format used by Mojang)
	privateKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err == nil {
		rsaPrivateKey, ok := privateKey.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("private key is not RSA")
		}
		return rsaPrivateKey, nil
	}

	// Fallback to PKCS1 format
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func parseRSAPublicKey(publicKeyPEM string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block containing public key")
	}

	// Try parsing as PKIX first (X.509)
	if publicKey, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if rsaPublicKey, ok := publicKey.(*rsa.PublicKey); ok {
			return rsaPublicKey, nil
		}
	}

	// Try parsing as PKCS#1
	return x509.ParsePKCS1PublicKey(block.Bytes)
}

func runClient(serverAddr string, verbose bool, loginData auth.LoginData, sessionClient *session_server.SessionServerClient) {
	client := jp.NewTCPClient()
	client.EnableDebug(verbose)

	host, port := splitHostPort(serverAddr)

	err := client.Connect(fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	log.Println("Connected! Sending handshake...")

	currentState := jp.StateHandshake

	// Send handshake packet
	handshakePacket, err := packets.C2SIntention.WithData(packets.C2SIntentionData{
		ProtocolVersion: protocolVersion,
		ServerAddress:   ns.String(host),
		ServerPort:      ns.UnsignedShort(port),
		Intent:          2, // login intent
	})
	if err != nil {
		log.Fatalf("Failed to build handshake: %v", err)
	}

	if err := client.WritePacket(handshakePacket); err != nil {
		log.Fatalf("Failed to send handshake: %v", err)
	}

	// Transition to login state
	currentState = jp.StateLogin
	client.SetState(currentState)
	log.Println("Starting login...")

	// Send login start packet
	uuid, _ := ns.NewUUID(loginData.UUID)
	loginStartPacket, err := packets.C2SHello.WithData(packets.C2SHelloData{
		Name:       ns.String(loginData.Username),
		PlayerUuid: uuid,
	})
	if err != nil {
		log.Fatalf("Failed to build login start: %v", err)
	}

	if err := client.WritePacket(loginStartPacket); err != nil {
		log.Fatalf("Failed to send login start: %v", err)
	}

	log.Println("Login start sent! Processing packets...")

	// Main packet loop
	for {
		packet, err := client.ReadPacket()
		if err != nil {
			log.Printf("Error reading packet: %v", err)
			break
		}

		switch currentState {
		case jp.StateLogin:
			if packet.PacketID == packets.S2CHello.PacketID && sessionClient != nil {
				handleEncryptionRequest(client, packet, &loginData, sessionClient)
			} else {
				handleLoginPacket(client, packet)
				if packet.PacketID == packets.S2CLoginFinished.PacketID {
					currentState = jp.StateConfiguration
				}
			}
		case jp.StateConfiguration:
			handleConfigurationPacket(client, packet)
			if packet.PacketID == packets.S2CLoginCompression.PacketID {
				currentState = jp.StatePlay
			}
		case jp.StatePlay:
			handlePlayPacket(client, packet)
		}
	}
}

func handleEncryptionRequest(client *jp.TCPClient, packet *jp.Packet, loginData *auth.LoginData, sessionClient *session_server.SessionServerClient) {
	log.Println("Received encryption request")

	data := ns.ByteArray(packet.Data)
	offset := 0

	// Parse server ID
	var serverID ns.String
	n, err := serverID.FromBytes(data[offset:])
	if err != nil {
		log.Fatalf("Failed to read server ID: %v", err)
	}
	offset += n

	// Parse public key
	var publicKeyLength ns.VarInt
	n, err = publicKeyLength.FromBytes(data[offset:])
	if err != nil {
		log.Fatalf("Failed to read public key length: %v", err)
	}
	offset += n

	publicKey := make([]byte, publicKeyLength)
	copy(publicKey, data[offset:offset+int(publicKeyLength)])
	offset += int(publicKeyLength)

	// Parse verify token
	var verifyTokenLength ns.VarInt
	n, err = verifyTokenLength.FromBytes(data[offset:])
	if err != nil {
		log.Fatalf("Failed to read verify token length: %v", err)
	}
	offset += n

	verifyToken := make([]byte, verifyTokenLength)
	copy(verifyToken, data[offset:offset+int(verifyTokenLength)])

	// Generate and encrypt shared secret
	encryption := client.GetEncryption()
	sharedSecret, err := encryption.GenerateSharedSecret()
	if err != nil {
		log.Fatalf("Failed to generate shared secret: %v", err)
	}

	encryptedSharedSecret, err := encryption.EncryptWithPublicKey(publicKey, sharedSecret)
	if err != nil {
		log.Fatalf("Failed to encrypt shared secret: %v", err)
	}

	encryptedVerifyToken, err := encryption.EncryptWithPublicKey(publicKey, verifyToken)
	if err != nil {
		log.Fatalf("Failed to encrypt verify token: %v", err)
	}

	// Authenticate with Mojang session server
	err = sessionClient.Join(loginData.AccessToken, loginData.UUID, string(serverID), sharedSecret, publicKey)
	if err != nil {
		log.Printf("Warning: Session server authentication failed: %v", err)
	}

	sharedSecretArray := make([]ns.Byte, len(encryptedSharedSecret))
	for i, b := range encryptedSharedSecret {
		sharedSecretArray[i] = ns.Byte(b)
	}
	verifyTokenArray := make([]ns.Byte, len(encryptedVerifyToken))
	for i, b := range encryptedVerifyToken {
		verifyTokenArray[i] = ns.Byte(b)
	}
	encryptionResponse, err := packets.C2SKey.WithData(packets.C2SKeyData{
		SharedSecret: sharedSecretArray,
		VerifyToken:  verifyTokenArray,
	})
	if err != nil {
		log.Fatalf("Failed to build encryption response: %v", err)
	}

	if err := client.WritePacket(encryptionResponse); err != nil {
		log.Fatalf("Failed to send encryption response: %v", err)
	}

	// Enable encryption
	err = encryption.EnableEncryption()
	if err != nil {
		log.Fatalf("Failed to enable encryption: %v", err)
	}

	log.Println("Encryption enabled!")
}

func handleLoginPacket(client *jp.TCPClient, packet *jp.Packet) {
	switch packet.PacketID {
	case packets.S2CLoginDisconnectLogin.PacketID:
		data := ns.ByteArray(packet.Data)
		var reason ns.String
		_, err := reason.FromBytes(data)
		if err != nil {
			log.Printf("Disconnected during login (failed to parse reason): %v", err)
		} else {
			log.Printf("Disconnected during login. Reason: %s", string(reason))
		}
		os.Exit(0)
	case packets.S2CLoginFinished.PacketID:
		log.Println("Login successful!")

		if err := client.WritePacket(packets.C2SLoginAcknowledged); err != nil {
			log.Printf("Failed to send login acknowledged: %v", err)
		}

		client.SetState(jp.StateConfiguration)
		log.Println("Entered configuration state")

		sendBrandPluginMessage(client, "vanilla")
		sendClientInformation(client)
	case packets.S2CLoginCompression.PacketID:
		data := ns.ByteArray(packet.Data)
		var threshold ns.VarInt
		_, err := threshold.FromBytes(data)
		if err != nil {
			log.Printf("Failed to read compression threshold: %v", err)
		} else {
			log.Printf("Compression enabled with threshold: %d", threshold)
			client.SetCompressionThreshold(int(threshold))
		}
	}
}

func handleConfigurationPacket(client *jp.TCPClient, packet *jp.Packet) {
	switch packet.PacketID {
	case packets.S2CDisconnectConfiguration.PacketID:
		data := []byte(packet.Data)
		msg := parseDisconnectReason(data)
		log.Printf("Disconnected during configuration. Reason: %s", msg)
		os.Exit(0)
	case packets.S2CFinishConfiguration.PacketID:
		log.Println("Configuration finished, acknowledging...")

		if err := client.WritePacket(packets.C2SFinishConfiguration); err != nil {
			log.Printf("Failed to send configuration acknowledgment: %v", err)
		}

		client.SetState(jp.StatePlay)
		log.Println("Entered play state!")

		// Send chat session data for signed messages
		time.Sleep(100 * time.Millisecond)
		if chatSigner != nil {
			sendChatSessionData(client)
		}
	case packets.S2CKeepAliveConfiguration.PacketID:
		var keepAliveData packets.S2CKeepAliveConfigurationData
		if err := jp.BytesToPacketData(packet.Data, &keepAliveData); err != nil {
			log.Printf("Failed to parse KeepAlive ID: %v", err)
			return
		}
		keepAlive, err := packets.C2SKeepAliveConfiguration.WithData(packets.C2SKeepAliveConfigurationData{
			KeepAliveId: keepAliveData.KeepAliveId,
		})
		if err != nil {
			log.Printf("Failed to build KeepAlive response: %v", err)
			return
		}
		if err := client.WritePacket(keepAlive); err != nil {
			log.Printf("Failed to send KeepAlive response: %v", err)
		}
	case packets.S2CSelectKnownPacks.PacketID:
		reply, err := packets.C2SSelectKnownPacks.WithData(packets.C2SSelectKnownPacksData{})
		if err != nil {
			log.Printf("Failed to build Known Packs: %v", err)
		}
		if err := client.WritePacket(reply); err != nil {
			log.Printf("Failed to send Known Packs: %v", err)
		}
	}
}

func handlePlayPacket(client *jp.TCPClient, packet *jp.Packet) {
	switch packet.PacketID {
	case packets.S2CDisconnectPlay.PacketID:
		var data packets.S2CDisconnectPlayData
		if err := jp.BytesToPacketData(packet.Data, &data); err != nil {
			log.Printf("Failed to parse disconnect play data: %v", err)
			return
		}
		log.Printf("Disconnected from play. Reason: %s", data.Reason)
		os.Exit(0)
	case packets.S2CLoginPlay.PacketID:
		log.Println("Received login play packet - player spawned in world! Ready!")
		sendChatMessage(client, "Hello, world!")

	case packets.S2CPlayerChat.PacketID:
		log.Println("Received player chat packet")
		var chatData packets.S2CPlayerChatData
		if err := jp.BytesToPacketData(packet.Data, &chatData); err != nil {
			// Fallback: parse around signature field and parse the tail
			sender, target, msg, ok := parsePlayerChatFast(ns.ByteArray(packet.Data))
			if ok {
				if target != "" {
					log.Printf("[PLAYER] %s -> %s: %s", sender, target, msg)
				} else {
					log.Printf("[PLAYER] %s: %s", sender, msg)
				}
				return
			}
			log.Printf("Failed to parse player chat data: %v", err)
			return
		}

		senderName := tcText(chatData.SenderName)
		messageText := string(chatData.Message)

		if chatData.TargetName.Present {
			targetName := tcText(chatData.TargetName.Value)
			log.Printf("[PLAYER] %s -> %s: %s", senderName, targetName, messageText)
		} else {
			log.Printf("[PLAYER] %s: %s", senderName, messageText)
		}

	case packets.S2CPlayerPosition.PacketID:
		log.Println("Received player position packet")

		teleportConfirm, err := packets.C2SAcceptTeleportation.WithData(packets.C2SAcceptTeleportationData{TeleportId: 0})
		if err != nil {
			log.Printf("Failed to build teleport confirmation: %v", err)
			return
		}

		if err := client.WritePacket(teleportConfirm); err != nil {
			log.Printf("Failed to send teleport confirmation: %v", err)
		}

	case packets.S2CKeepAlivePlay.PacketID:
		var keepAliveData packets.S2CKeepAlivePlayData
		if err := jp.BytesToPacketData(packet.Data, &keepAliveData); err != nil {
			log.Printf("Failed to read keep alive data: %v", err)
			return
		}

		keepAlive, err := packets.C2SKeepAlivePlay.WithData(packets.C2SKeepAlivePlayData{
			KeepAliveId: keepAliveData.KeepAliveId,
		})
		if err != nil {
			log.Printf("Failed to build keep alive: %v", err)
		}
		if err := client.WritePacket(keepAlive); err != nil {
			log.Printf("Failed to send keep alive: %v", err)
		}

	case packets.S2CSystemChat.PacketID:
		log.Println("Received system chat packet")
		var systemChatData packets.S2CSystemChatData
		if err := jp.BytesToPacketData(packet.Data, &systemChatData); err != nil {
			// Fallback: try parse as VarInt-prefixed JSON string + boolean
			msg, overlay, ok := parseSystemChatFast(ns.ByteArray(packet.Data))
			if ok {
				if overlay {
					log.Printf("[SYSTEM-ACTION] %s", msg)
				} else {
					log.Printf("[SYSTEM] %s", msg)
				}
				return
			}
			log.Printf("Failed to parse system chat data: %v", err)
			return
		}

		messageText := systemChatData.Content.GetText()
		if systemChatData.Overlay {
			log.Printf("[SYSTEM-ACTION] %s", messageText)
		} else {
			log.Printf("[SYSTEM] %s", messageText)
		}

	case packets.S2CDisguisedChat.PacketID:
		log.Println("Received disguised chat packet")
		var disguisedChatData packets.S2CDisguisedChatData
		if err := jp.BytesToPacketData(packet.Data, &disguisedChatData); err != nil {
			log.Printf("Failed to parse disguised chat data: %v", err)
			return
		}

		messageText := disguisedChatData.Message.GetText()
		senderName := disguisedChatData.SenderName.GetText()

		if disguisedChatData.TargetName.Present {
			targetName := disguisedChatData.TargetName.Value.GetText()
			log.Printf("[DISGUISED] %s -> %s: %s", senderName, targetName, messageText)
		} else {
			log.Printf("[DISGUISED] %s: %s", senderName, messageText)
		}
	}
}

func handleMessage(sender string, message string) {
	log.Printf("[%s] %s", sender, message)
}

func sendChatMessage(client *jp.TCPClient, message string) {
	if chatSigner != nil {
		log.Printf("Sending signed chat message: %s", message)

		// Generate salt for this message
		saltBytes := make([]byte, 8)
		rand.Read(saltBytes)
		salt := int64(binary.BigEndian.Uint64(saltBytes))

		timestamp := time.Now()

		// Get last seen messages for the chain
		lastSeenMessages := chatSigner.GetLastSeenMessages(20)

		// Sign the message
		signedMsg, err := chatSigner.SignMessage(message, timestamp, salt, lastSeenMessages)
		if err != nil {
			log.Printf("Failed to sign chat message: %v", err)
			return
		}

		// Build acknowledged bitset (empty for now)
		acknowledged := ns.FixedBitSet{Length: 20, Data: make([]byte, 3)}

		chatPacket, err := packets.C2SChat.WithData(packets.C2SChatData{
			Message:      ns.String(message),
			Timestamp:    ns.Long(timestamp.UnixMilli()),
			Salt:         ns.Long(salt),
			Signature:    ns.PrefixedOptional[ns.ByteArray]{Present: true, Value: ns.ByteArray(signedMsg.Signature)},
			MessageCount: ns.VarInt(len(lastSeenMessages)),
			Acknowledged: acknowledged,
			Checksum:     ns.Byte(0),
		})
		if err != nil {
			log.Printf("Failed to build signed chat message: %v", err)
			return
		}

		if err := client.WritePacket(chatPacket); err != nil {
			log.Printf("Failed to send signed chat message: %v", err)
		}
	} else {
		// Send unsigned chat message
		log.Printf("Sending unsigned chat message: %s", message)
		chatPacket, err := packets.C2SChat.WithData(packets.C2SChatData{
			Message:      ns.String(message),
			Timestamp:    ns.Long(time.Now().UnixMilli()),
			Salt:         ns.Long(0),
			Signature:    ns.PrefixedOptional[ns.ByteArray]{},
			MessageCount: ns.VarInt(0),
			Acknowledged: ns.FixedBitSet{Length: 20, Data: make([]byte, 3)},
			Checksum:     ns.Byte(0),
		})
		if err != nil {
			log.Printf("Failed to build unsigned chat message: %v", err)
			return
		}

		if err := client.WritePacket(chatPacket); err != nil {
			log.Printf("Failed to send unsigned chat message: %v", err)
		}
	}
}

func sendChatSessionData(client *jp.TCPClient) {
	log.Println("Sending chat session data...")

	// Generate a random session UUID (NOT the player UUID)
	var sessionID ns.UUID
	rand.Read(sessionID[:])
	chatSigner.SessionUUID = sessionID

	// Get public key in SPKI DER format
	publicKeyBytes := chatSigner.X509PublicKey
	if len(publicKeyBytes) == 0 {
		log.Printf("No public key available for chat session")
		return
	}

	// Get expiry time in milliseconds
	expiryTime := chatSigner.KeyExpiry
	expiresAt := ns.Long(expiryTime.UnixMilli())

	// Get Mojang's signature (V2 - 512 bytes)
	mojangSignature := chatSigner.SessionKey

	// Manual serialization for Player Session packet
	var buf bytes.Buffer

	// Session UUID (16 bytes)
	buf.Write(sessionID[:])

	// Expires At (8 bytes, big endian)
	expiresAtBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(expiresAtBytes, uint64(expiresAt))
	buf.Write(expiresAtBytes)

	// Public Key (VarInt length + data)
	writeVarInt(&buf, len(publicKeyBytes))
	buf.Write(publicKeyBytes)

	// Key Signature (VarInt length + data)
	writeVarInt(&buf, len(mojangSignature))
	buf.Write(mojangSignature)

	// Create packet with manual data
	type manualPacketStruct struct {
		Data ns.ByteArray
	}

	manualData := manualPacketStruct{Data: ns.ByteArray(buf.Bytes())}
	sessionPacket, err := jp.NewPacket(jp.StatePlay, jp.C2S, 0x09).WithData(manualData)
	if err != nil {
		log.Printf("Failed to build session packet: %v", err)
		return
	}

	if err := client.WritePacket(sessionPacket); err != nil {
		log.Printf("Failed to send chat session data: %v", err)
	} else {
		log.Println("Chat session data sent successfully")
	}
}

func writeVarInt(buf *bytes.Buffer, value int) {
	for value >= 0x80 {
		buf.WriteByte(byte(value&0x7F | 0x80))
		value >>= 7
	}
	buf.WriteByte(byte(value))
}

func sendClientInformation(client *jp.TCPClient) {
	info := packets.C2SClientInformationConfigurationData{
		Locale:              ns.String("en_us"),
		ViewDistance:        ns.Byte(12),
		ChatMode:            ns.VarInt(0),
		ChatColors:          ns.Boolean(true),
		DisplayedSkinParts:  ns.UnsignedByte(0x7F),
		MainHand:            ns.VarInt(1),
		EnableTextFiltering: ns.Boolean(true),
		AllowServerListings: ns.Boolean(true),
		ParticleStatus:      ns.VarInt(2),
	}
	pkt, err := packets.C2SClientInformationConfiguration.WithData(info)
	if err != nil {
		log.Printf("Failed to build Client Information: %v", err)
		return
	}
	if err := client.WritePacket(pkt); err != nil {
		log.Printf("Failed to send Client Information: %v", err)
	}
}

func sendBrandPluginMessage(client *jp.TCPClient, brand string) {
	dataBytes, err := ns.String(brand).ToBytes()
	if err != nil {
		log.Printf("Failed to build brand payload: %v", err)
		return
	}
	pkt, err := packets.C2SCustomPayloadConfiguration.WithData(packets.C2SCustomPayloadConfigurationData{
		Channel: ns.Identifier("minecraft:brand"),
		Data:    ns.ByteArray(dataBytes),
	})
	if err != nil {
		log.Printf("Failed to build brand plugin message: %v", err)
		return
	}
	if err := client.WritePacket(pkt); err != nil {
		log.Printf("Failed to send brand plugin message: %v", err)
	}
}

func parseDisconnectReason(data []byte) string {
	if v, ok := extractNBTTextValue(data, "text"); ok && v != "" {
		return v
	}

	var str ns.String
	if _, err := str.FromBytes(ns.ByteArray(data)); err == nil {
		txt := string(str)
		if len(txt) > 0 && (txt[0] == '{' || txt[0] == '[' || txt[0] == '"') {
			var m map[string]any
			if json.Unmarshal([]byte(txt), &m) == nil {
				if v, ok := m["text"].(string); ok && v != "" {
					return v
				}
			}
		}
		if txt != "" && txt != "color" {
			return txt
		}
	}

	return "<unknown reason>"
}

func extractNBTTextValue(data []byte, key string) (string, bool) {
	keyBytes := []byte(key)
	for i := 0; i+7 < len(data); i++ {
		if data[i] == 0x08 { // TAG_String
			if i+3+len(keyBytes) >= len(data) {
				continue
			}
			nameLen := int(data[i+1])<<8 | int(data[i+2])
			if nameLen == len(keyBytes) {
				nameStart := i + 3
				nameEnd := nameStart + nameLen
				if nameEnd <= len(data) && bytes.Equal(data[nameStart:nameEnd], keyBytes) {
					if nameEnd+2 > len(data) {
						return "", false
					}
					valLen := int(data[nameEnd])<<8 | int(data[nameEnd+1])
					valStart := nameEnd + 2
					valEnd := valStart + valLen
					if valEnd <= len(data) {
						return string(data[valStart:valEnd]), true
					}
					return "", false
				}
			}
		}
	}
	return "", false
}

func splitHostPort(addr string) (string, uint16) {
	host := addr
	port := uint16(25565)
	if h, p, err := net.SplitHostPort(addr); err == nil {
		host = h
		var parsed int
		fmt.Sscanf(p, "%d", &parsed)
		if parsed > 0 && parsed <= 65535 {
			port = uint16(parsed)
		}
	}
	return host, port
}

// HACK: move all of below to go-mclib/data and go-mclib/protocol

// tcText extracts readable text from a TextComponent with multiple fallbacks
func tcText(tc ns.TextComponent) string {
	if t := tc.GetText(); strings.TrimSpace(t) != "" {
		return t
	}
	if raw := getTextComponentData(tc); len(raw) > 0 {
		var s ns.String
		if n, err := s.FromBytes(raw); err == nil && n > 0 {
			str := string(s)
			if strings.TrimSpace(str) != "" {
				return str
			}
		}
	}
	// Fallback: attempt to use NBT if available
	if nbt := tc.GetNBT(); nbt != nil {
		if t := nbt.ExtractTextFromNBT(); strings.TrimSpace(t) != "" {
			return t
		}
	}
	return ""
}

// getTextComponentData safely accesses the underlying bytes if available
func getTextComponentData(tc ns.TextComponent) ns.ByteArray {
	type dataCarrier interface{}
	_ = dataCarrier(tc)

	return tc.Data
}

// parseSystemChatFast parses as VarInt-prefixed JSON string + boolean.
func parseSystemChatFast(data ns.ByteArray) (string, bool, bool) {
	var l ns.VarInt
	n, err := l.FromBytes(data)
	if err != nil {
		return "", false, false
	}
	if len(data) < n+int(l)+1 {
		return "", false, false
	}
	payload := string(data[n : n+int(l)])
	overlay := data[n+int(l)] != 0
	if comp, err := ns.ParseTextComponentFromString(payload); err == nil {
		return comp.String(), overlay, true
	}
	return payload, overlay, true
}

func parsePlayerChatFast(data ns.ByteArray) (string, string, string, bool) {
	offset := 0

	var vi ns.VarInt
	n, err := vi.FromBytes(data[offset:])
	if err != nil {
		return "", "", "", false
	}
	offset += n

	var uuid ns.UUID
	n, err = uuid.FromBytes(data[offset:])
	if err != nil {
		return "", "", "", false
	}
	offset += n
	var idx ns.VarInt
	n, err = idx.FromBytes(data[offset:])
	if err != nil {
		return "", "", "", false
	}
	offset += n
	var present ns.Boolean
	n, err = present.FromBytes(data[offset:])
	if err != nil {
		return "", "", "", false
	}
	offset += n
	if bool(present) {
		if len(data) < offset+256 {
			return "", "", "", false
		}
		offset += 256
	}
	var msgStr ns.String
	n, err = msgStr.FromBytes(data[offset:])
	if err != nil {
		return "", "", "", false
	}
	msg := string(msgStr)
	return "", "", msg, true
}
