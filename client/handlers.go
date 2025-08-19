package client

import (
	"bytes"
	"encoding/json"
	"log"
	"time"

	packets "github.com/go-mclib/data/go/772/java_packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/net_structures"
)

type Handler func(c *Client, pkt *jp.Packet)

// defaultStateHandler drives the client through login -> configuration -> play
func defaultStateHandler(c *Client, pkt *jp.Packet) {
	switch c.state {
	case jp.StateLogin:
		if pkt.PacketID == packets.S2CHello.PacketID && c.SessionClient != nil {
			handleEncryptionRequest(c, pkt)
			return
		}
		handleLoginPacket(c, pkt)
		if pkt.PacketID == packets.S2CLoginFinished.PacketID {
			c.state = jp.StateConfiguration
		}
	case jp.StateConfiguration:
		handleConfigurationPacket(c, pkt)
		if pkt.PacketID == packets.S2CLoginCompression.PacketID {
			c.state = jp.StatePlay
		}
	case jp.StatePlay:
		handlePlayPacket(c, pkt)
	}
}

func handleEncryptionRequest(c *Client, pkt *jp.Packet) {
	c.Logger.Println("received encryption request")
	data := ns.ByteArray(pkt.Data)
	offset := 0

	var serverID ns.String
	n, err := serverID.FromBytes(data[offset:])
	if err != nil {
		c.Logger.Println("server id:", err)
		return
	}
	offset += n

	var publicKeyLength ns.VarInt
	n, err = publicKeyLength.FromBytes(data[offset:])
	if err != nil {
		c.Logger.Println("public key len:", err)
		return
	}
	offset += n

	publicKey := make([]byte, publicKeyLength)
	copy(publicKey, data[offset:offset+int(publicKeyLength)])
	offset += int(publicKeyLength)

	var verifyTokenLength ns.VarInt
	n, err = verifyTokenLength.FromBytes(data[offset:])
	if err != nil {
		c.Logger.Println("verify token len:", err)
		return
	}
	offset += n

	verifyToken := make([]byte, verifyTokenLength)
	copy(verifyToken, data[offset:offset+int(verifyTokenLength)])

	encryption := c.GetEncryption()
	sharedSecret, err := encryption.GenerateSharedSecret()
	if err != nil {
		c.Logger.Println("gen shared secret:", err)
		return
	}

	encryptedSharedSecret, err := encryption.EncryptWithPublicKey(publicKey, sharedSecret)
	if err != nil {
		c.Logger.Println("encrypt shared secret:", err)
		return
	}
	encryptedVerifyToken, err := encryption.EncryptWithPublicKey(publicKey, verifyToken)
	if err != nil {
		c.Logger.Println("encrypt verify token:", err)
		return
	}

	if c.SessionClient != nil {
		if err := c.SessionClient.Join(c.LoginData.AccessToken, c.LoginData.UUID, string(serverID), sharedSecret, publicKey); err != nil {
			c.Logger.Println("session join warn:", err)
		}
	}

	ss := make([]ns.Byte, len(encryptedSharedSecret))
	for i, b := range encryptedSharedSecret {
		ss[i] = ns.Byte(b)
	}
	vt := make([]ns.Byte, len(encryptedVerifyToken))
	for i, b := range encryptedVerifyToken {
		vt[i] = ns.Byte(b)
	}

	resp, err := packets.C2SKey.WithData(packets.C2SKeyData{SharedSecret: ss, VerifyToken: vt})
	if err != nil {
		c.Logger.Println("build enc resp:", err)
		return
	}
	if err := c.WritePacket(resp); err != nil {
		c.Logger.Println("send enc resp:", err)
	}

	if err := encryption.EnableEncryption(); err != nil {
		c.Logger.Println("enable encryption:", err)
		return
	}
	c.Logger.Println("encryption enabled")
}

func handleLoginPacket(c *Client, pkt *jp.Packet) {
	switch pkt.PacketID {
	case packets.S2CLoginDisconnectLogin.PacketID:
		data := ns.ByteArray(pkt.Data)
		var reason ns.String
		if _, err := reason.FromBytes(data); err != nil {
			c.Logger.Println("login disconnect (parse):", err)
		} else {
			c.Logger.Printf("login disconnect: %s", string(reason))
		}
	case packets.S2CLoginFinished.PacketID:
		c.Logger.Println("login successful")
		_ = c.WritePacket(packets.C2SLoginAcknowledged)
		c.SetState(jp.StateConfiguration)
		sendBrandPluginMessage(c, "vanilla")
		sendClientInformation(c)
	case packets.S2CLoginCompression.PacketID:
		data := ns.ByteArray(pkt.Data)
		var threshold ns.VarInt
		if _, err := threshold.FromBytes(data); err != nil {
			c.Logger.Println("compression threshold:", err)
		} else {
			c.TCPClient.SetCompressionThreshold(int(threshold))
			c.Logger.Printf("compression enabled: %d", threshold)
		}
	}
}

func handleConfigurationPacket(c *Client, pkt *jp.Packet) {
	switch pkt.PacketID {
	case packets.S2CDisconnectConfiguration.PacketID:
		msg := parseDisconnectReason([]byte(pkt.Data))
		c.Logger.Printf("disconnected during configuration: %s", msg)
	case packets.S2CFinishConfiguration.PacketID:
		_ = c.WritePacket(packets.C2SFinishConfiguration)
		c.SetState(jp.StatePlay)
		c.Logger.Println("entered play state")
		time.Sleep(100 * time.Millisecond)
		c.sendChatSessionData()
	case packets.S2CKeepAliveConfiguration.PacketID:
		var d packets.S2CKeepAliveConfigurationData
		if err := jp.BytesToPacketData(pkt.Data, &d); err == nil {
			reply, _ := packets.C2SKeepAliveConfiguration.WithData(packets.C2SKeepAliveConfigurationData{KeepAliveId: d.KeepAliveId})
			_ = c.WritePacket(reply)
		}
	case packets.S2CSelectKnownPacks.PacketID:
		if reply, err := packets.C2SSelectKnownPacks.WithData(packets.C2SSelectKnownPacksData{}); err == nil {
			_ = c.WritePacket(reply)
		}
	}
}

func handlePlayPacket(c *Client, pkt *jp.Packet) {
	switch pkt.PacketID {
	case packets.S2CDisconnectPlay.PacketID:
		var d packets.S2CDisconnectPlayData
		if err := jp.BytesToPacketData(pkt.Data, &d); err == nil {
			c.Logger.Printf("disconnect: %s", d.Reason)
		}
	case packets.S2CLoginPlay.PacketID:
		c.Logger.Println("spawned; ready")
	case packets.S2CPlayerChat.PacketID:
		var chatData packets.S2CPlayerChatData
		if err := jp.BytesToPacketData(pkt.Data, &chatData); err == nil {
			sender := chatData.SenderName.GetText()
			msg := string(chatData.Message)
			if chatData.TargetName.Present {
				c.Logger.Printf("[PLAYER] %s -> %s: %s", sender, chatData.TargetName.Value.GetText(), msg)
			} else {
				c.Logger.Printf("[PLAYER] %s: %s", sender, msg)
			}
		} else {
			_, _, msg, ok := parsePlayerChatFast(ns.ByteArray(pkt.Data))
			if ok {
				c.Logger.Printf("[PLAYER] %s", msg)
			} else {
				log.Println("player chat parse error:", err)
			}
		}
	case packets.S2CKeepAlivePlay.PacketID:
		var d packets.S2CKeepAlivePlayData
		if err := jp.BytesToPacketData(pkt.Data, &d); err == nil {
			reply, _ := packets.C2SKeepAlivePlay.WithData(packets.C2SKeepAlivePlayData{KeepAliveId: d.KeepAliveId})
			_ = c.WritePacket(reply)
		}
	case packets.S2CSystemChat.PacketID:
		var d packets.S2CSystemChatData
		if err := jp.BytesToPacketData(pkt.Data, &d); err == nil {
			txt := d.Content.GetText()
			if d.Overlay {
				c.Logger.Printf("[SYSTEM-ACTION] %s", txt)
			} else {
				c.Logger.Printf("[SYSTEM] %s", txt)
			}
		} else {
			if msg, overlay, ok := parseSystemChatFast(ns.ByteArray(pkt.Data)); ok {
				if overlay {
					c.Logger.Printf("[SYSTEM-ACTION] %s", msg)
				} else {
					c.Logger.Printf("[SYSTEM] %s", msg)
				}
			}
		}
	case packets.S2CDisguisedChat.PacketID:
		var d packets.S2CDisguisedChatData
		if err := jp.BytesToPacketData(pkt.Data, &d); err == nil {
			msg := d.Message.GetText()
			sender := d.SenderName.GetText()
			if d.TargetName.Present {
				c.Logger.Printf("[DISGUISED] %s -> %s: %s", sender, d.TargetName.Value.GetText(), msg)
			} else {
				c.Logger.Printf("[DISGUISED] %s: %s", sender, msg)
			}
		}
	}
}

func sendClientInformation(c *Client) {
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
	if pkt, err := packets.C2SClientInformationConfiguration.WithData(info); err == nil {
		_ = c.WritePacket(pkt)
	}
}

func sendBrandPluginMessage(c *Client, brand string) {
	dataBytes, err := ns.String(brand).ToBytes()
	if err != nil {
		return
	}
	if pkt, err := packets.C2SCustomPayloadConfiguration.WithData(packets.C2SCustomPayloadConfigurationData{
		Channel: ns.Identifier("minecraft:brand"),
		Data:    ns.ByteArray(dataBytes),
	}); err == nil {
		_ = c.WritePacket(pkt)
	}
}

// HACK: integrate funcs below to go-mclib/data and go-mclib/protocol:

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
