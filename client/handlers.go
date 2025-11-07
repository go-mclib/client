package client

import (
	"time"

	packets "github.com/go-mclib/data/go/773/java_packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/net_structures"
)

type Handler func(c *Client, pkt *jp.Packet)

// defaultStateHandler drives the client through login -> configuration -> play
func defaultStateHandler(c *Client, pkt *jp.Packet) {
	switch c.GetState() {
	case jp.StateLogin:
		if pkt.PacketID == packets.S2CHello.PacketID && c.SessionClient != nil {
			handleEncryptionRequest(c, pkt)
			return
		}
		handleLoginPacket(c, pkt)
	case jp.StateConfiguration:
		handleConfigurationPacket(c, pkt)
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
		c.shouldReconnect = true
	case packets.S2CLoginFinished.PacketID:
		c.Logger.Println("login successful")
		_ = c.WritePacket(packets.C2SLoginAcknowledged)
		sendBrandPluginMessage(c, "vanilla")
		sendClientInformation(c)

		c.SetState(jp.StateConfiguration)
		c.Logger.Println("switched from login -> configuration state")
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
		var data packets.S2CDisconnectConfigurationData
		if err := jp.BytesToPacketData(pkt.Data, &data); err != nil {
			c.Logger.Println("failed to parse disconnect configuration data:", err)
		}
		c.Logger.Printf("disconnected during configuration: %s", string(data.Reason.GetText()))
		c.shouldReconnect = true
	case packets.S2CFinishConfiguration.PacketID:
		_ = c.WritePacket(packets.C2SFinishConfiguration)
		c.SetState(jp.StatePlay)
		c.Logger.Println("switched from configuration -> play state")
		time.Sleep(100 * time.Millisecond)
		c.sendChatSessionData()
	case packets.S2CKeepAliveConfiguration.PacketID:
		var d packets.S2CKeepAliveConfigurationData
		if err := jp.BytesToPacketData(pkt.Data, &d); err == nil {
			reply, _ := packets.C2SKeepAliveConfiguration.WithData(packets.C2SKeepAliveConfigurationData(d))
			_ = c.WritePacket(reply)
		}
	case packets.S2CSelectKnownPacks.PacketID:
		if reply, err := packets.C2SSelectKnownPacks.WithData(packets.C2SSelectKnownPacksData{}); err == nil {
			_ = c.WritePacket(reply)
		}
	case packets.S2CResourcePackPushConfiguration.PacketID:
		var d packets.S2CResourcePackPushConfigurationData
		if err := jp.BytesToPacketData(pkt.Data, &d); err == nil {
			reply, _ := packets.C2SResourcePackConfiguration.WithData(packets.C2SResourcePackConfigurationData{
				Uuid:   d.Uuid,
				Result: 0, // Successfully downloaded
			})
			if err != nil {
				c.Logger.Println("failed to build resource pack response:", err)
				return
			}
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
		c.shouldReconnect = true
	case packets.S2CStartConfiguration.PacketID:
		if err := c.WritePacket(packets.C2SConfigurationAcknowledged); err != nil {
			c.Logger.Println("failed to send configuration_acknowledged:", err)
		}
		c.SetState(jp.StateConfiguration)
		c.Logger.Println("switched from play -> configuration phase, client is probably being transfered to another server")
	case packets.S2CLoginPlay.PacketID:
		var d packets.S2CLoginPlayData
		if err := jp.BytesToPacketData(pkt.Data, &d); err != nil {
			c.Logger.Println("failed to parse login play data:", err)
			return
		}
		c.Self.EntityID = ns.VarInt(d.EntityId)
		c.Logger.Println("spawned; ready")

		if err := c.WritePacket(packets.C2SPlayerLoaded); err != nil {
			c.Logger.Println("failed to send player loaded:", err)
		}

		c.Respawn() // health not available yet, just send the packet
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
		}
	case packets.S2CKeepAlivePlay.PacketID:
		var d packets.S2CKeepAlivePlayData
		if err := jp.BytesToPacketData(pkt.Data, &d); err == nil {
			reply, _ := packets.C2SKeepAlivePlay.WithData(packets.C2SKeepAlivePlayData(d))
			_ = c.WritePacket(reply)
		}
	// vanilla server doesn't use this, but some AC plugins like Grim do
	// and they kick bot if it doesn't respond to this packet (timed out):
	case packets.S2CPingPlay.PacketID:
		var d packets.S2CPingPlayData
		if err := jp.BytesToPacketData(pkt.Data, &d); err == nil {
			reply, _ := packets.C2SPongPlay.WithData(packets.C2SPongPlayData(d))
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
	case packets.S2CPlayerCombatKill.PacketID:
		// auto respawn on death
		var d packets.S2CPlayerCombatKillData
		if err := jp.BytesToPacketData(pkt.Data, &d); err != nil {
			c.Logger.Printf("failed to parse player combat kill data: %s", err)
			return
		}
		if d.PlayerId == c.Self.EntityID { // enable respawn screen
			c.Logger.Printf("died: %s", d.Message.GetText())
			c.Respawn()
		}
	}
}
