package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"github.com/go-mclib/protocol/auth"
	mc_crypto "github.com/go-mclib/protocol/crypto"
	jp "github.com/go-mclib/protocol/java_protocol"
	"github.com/go-mclib/protocol/java_protocol/packets"
	ns "github.com/go-mclib/protocol/net_structures"
	"github.com/go-mclib/protocol/session_server"
)

const protocolVersion = 772 // 1.21.8

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
		log.Println("Online mode requires authentication setup. Use -offline flag for testing.")
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

	sessionClient := session_server.NewSessionServerClient()
	runClient(serverAddr, verbose, loginData, sessionClient)
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

	handshakePacket, err := packets.C2SIntentionPacket.WithData(packets.C2SIntentionPacketData{
		ProtocolVersion: protocolVersion,
		ServerAddress:   ns.String(host),
		ServerPort:      ns.UnsignedShort(port),
		Intent:          packets.IntentLogin,
	})
	if err != nil {
		log.Fatalf("Failed to build handshake: %v", err)
	}

	if err := client.WritePacket(handshakePacket); err != nil {
		log.Fatalf("Failed to send handshake: %v", err)
	}

	currentState = jp.StateLogin
	client.SetState(currentState)
	log.Println("Handshake sent! Starting login...")

	uuid, _ := ns.NewUUID(loginData.UUID)
	loginStartPacket, err := packets.C2SHelloPacket.WithData(packets.C2SHelloPacketData{
		Name:       ns.String(loginData.Username),
		PlayerUUID: uuid,
	})
	if err != nil {
		log.Fatalf("Failed to build login start: %v", err)
	}

	if err := client.WritePacket(loginStartPacket); err != nil {
		log.Fatalf("Failed to send login start: %v", err)
	}

	log.Println("Login start sent! Waiting for server response...")

	for {
		packet, err := client.ReadPacket()
		if err != nil {
			log.Printf("Error reading packet: %v", err)
			break
		}

		// log.Printf("Received packet: ID=0x%02X, Current State=%v", packet.PacketID, currentState)

		switch currentState {
		case jp.StateLogin:
			if packet.PacketID == 0x01 && sessionClient != nil {
				handleEncryptionRequest(client, packet, &loginData, sessionClient)
			} else {
				handleLoginPacket(client, packet)
				if packet.PacketID == 0x02 {
					currentState = jp.StateConfiguration
				}
			}
		case jp.StateConfiguration:
			handleConfigurationPacket(client, packet)
			if packet.PacketID == 0x03 {
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

	var serverID ns.String
	n, err := serverID.FromBytes(data[offset:])
	if err != nil {
		log.Fatalf("Failed to read server ID: %v", err)
	}
	offset += n

	var publicKeyLength ns.VarInt
	n, err = publicKeyLength.FromBytes(data[offset:])
	if err != nil {
		log.Fatalf("Failed to read public key length: %v", err)
	}
	offset += n

	publicKey := make([]byte, publicKeyLength)
	copy(publicKey, data[offset:offset+int(publicKeyLength)])
	offset += int(publicKeyLength)

	var verifyTokenLength ns.VarInt
	n, err = verifyTokenLength.FromBytes(data[offset:])
	if err != nil {
		log.Fatalf("Failed to read verify token length: %v", err)
	}
	offset += n

	verifyToken := make([]byte, verifyTokenLength)
	copy(verifyToken, data[offset:offset+int(verifyTokenLength)])

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

	err = sessionClient.Join(loginData.AccessToken, loginData.UUID, string(serverID), sharedSecret, publicKey)
	if err != nil {
		log.Printf("Warning: Session server authentication failed: %v", err)
	}

	encryptionResponse, err := packets.C2SKeyPacket.WithData(packets.C2SKeyPacketData{
		SharedSecret: ns.PrefixedByteArray(encryptedSharedSecret),
		VerifyToken:  ns.PrefixedByteArray(encryptedVerifyToken),
	})
	if err != nil {
		log.Fatalf("Failed to build encryption response: %v", err)
	}

	if err := client.WritePacket(encryptionResponse); err != nil {
		log.Fatalf("Failed to send encryption response: %v", err)
	}

	err = encryption.EnableEncryption()
	if err != nil {
		log.Fatalf("Failed to enable encryption: %v", err)
	}

	log.Println("Encryption enabled!")
}

func handleLoginPacket(client *jp.TCPClient, packet *jp.Packet) {
	switch packet.PacketID {
	case 0x00:
		data := ns.ByteArray(packet.Data)
		var reason ns.String
		_, err := reason.FromBytes(data)
		if err != nil {
			log.Printf("Disconnected during login (failed to parse reason): %v", err)
		} else {
			log.Printf("Disconnected during login. Reason: %s", string(reason))
		}
		os.Exit(0)
	case 0x02:
		log.Println("Login successful!")

		if err := client.WritePacket(packets.C2SLoginAcknowledgedPacket); err != nil {
			log.Printf("Failed to send login acknowledged: %v", err)
		}

		client.SetState(jp.StateConfiguration)
		log.Println("Entered configuration state")

		sendBrandPluginMessage(client, "vanilla")
		sendClientInformation(client)
	case 0x03:
		data := ns.ByteArray(packet.Data)
		var threshold ns.VarInt
		_, err := threshold.FromBytes(data)
		if err != nil {
			log.Printf("Failed to read compression threshold: %v", err)
		} else {
			log.Printf("Compression enabled with threshold: %d", threshold)
			client.SetCompressionThreshold(int(threshold))
		}
	case 0x04:
		log.Println("Received login plugin request")
	}
}

func handleConfigurationPacket(client *jp.TCPClient, packet *jp.Packet) {
	switch packet.PacketID {
	case 0x02:
		data := []byte(packet.Data)
		msg := parseDisconnectReason(data)
		log.Printf("Disconnected during configuration. Reason: %s", msg)
		os.Exit(0)
	case 0x03:
		log.Println("Configuration finished, acknowledging...")

		ackPacket, err := packets.C2SFinishConfigurationPacket.WithData(struct{}{})
		if err != nil {
			log.Printf("Failed to build configuration acknowledgment: %v", err)
		}

		if err := client.WritePacket(ackPacket); err != nil {
			log.Printf("Failed to send configuration acknowledgment: %v", err)
		}

		client.SetState(jp.StatePlay)
		log.Println("Entered play state!")
	case 0x04:
		var keepAliveData packets.S2CKeepAliveConfigurationPacketData
		if err := jp.BytesToPacketData(packet.Data, &keepAliveData); err != nil {
			log.Printf("Failed to parse KeepAlive ID: %v", err)
			return
		}
		keepAlive, err := packets.C2SKeepAliveConfigurationPacket.WithData(packets.C2SKeepAliveConfigurationPacketData{KeepAliveID: keepAliveData.ID})
		if err != nil {
			log.Printf("Failed to build KeepAlive response: %v", err)
			return
		}
		if err := client.WritePacket(keepAlive); err != nil {
			log.Printf("Failed to send KeepAlive response: %v", err)
		}
	case 0x0E:
		reply, err := packets.C2SSelectKnownPacksPacket.WithData(packets.C2SSelectKnownPacksPacketData{})
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
	case 0x2B:
		log.Println("Received login play packet - player spawned in world!")
	case 0x40:
		log.Println("Received player position packet")

		teleportConfirm, err := packets.C2STeleportConfirmPacket.WithData(packets.C2STeleportConfirmPacketData{TeleportID: 0})
		if err != nil {
			log.Printf("Failed to build teleport confirmation: %v", err)
			return
		}

		if err := client.WritePacket(teleportConfirm); err != nil {
			log.Printf("Failed to send teleport confirmation: %v", err)
		}
	case 0x26:
		log.Println("Received keep alive packet")

		var keepAliveData packets.S2CKeepAlivePlayPacketData
		if err := jp.BytesToPacketData(packet.Data, &keepAliveData); err != nil {
			log.Printf("Failed to read keep alive data: %v", err)
			return
		}

		keepAlive, err := packets.C2SKeepAlivePlayPacket.WithData(packets.C2SKeepAlivePlayPacketData{KeepAliveID: keepAliveData.KeepAliveID})
		if err != nil {
			log.Printf("Failed to build keep alive: %v", err)
		}
		if err := client.WritePacket(keepAlive); err != nil {
			log.Printf("Failed to send keep alive: %v", err)
		}

		fmt.Println("Replied to keep alive")
	}
}

func sendClientInformation(client *jp.TCPClient) {
	info := packets.C2SClientInformationPacketData{
		Locale:              ns.String("en_us"),
		ViewDistance:        ns.Byte(12),
		ChatMode:            ns.VarInt(0),
		ChatColors:          ns.Boolean(true),
		DisplayedSkinParts:  ns.UnsignedByte(0x7F),
		MainHand:            ns.VarInt(1),
		EnableTextFiltering: ns.Boolean(true),
		AllowServerListings: ns.Boolean(true),
	}
	pkt, err := packets.C2SClientInformationPacket.WithData(info)
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
	pkt, err := packets.C2SCustomPayloadPacket.WithData(packets.C2SCustomPayloadPacketData{
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

// parseDisconnectReason attempts to extract a human-readable text from either
// a JSON Text Component (String) or an NBT-based text component payload.
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

// extractNBTTextValue performs a minimal scan for an NBT String tag with the given key name
// and returns its value. This is not a full NBT parser; it only handles the common case
// of a compound containing a String tag named "text".
func extractNBTTextValue(data []byte, key string) (string, bool) {
	// HACK: add support for generic NBT parsing in gomc-lib/protocol
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

// splitHostPort parses host and port from the address string. If port is missing, defaults to 25565.
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
