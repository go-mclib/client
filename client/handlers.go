package client

import (
	"time"

	"github.com/go-mclib/data/pkg/data/packet_ids"
	"github.com/go-mclib/data/pkg/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

type Handler func(c *Client, pkt *jp.WirePacket)

// defaultStateHandler drives the client through login -> configuration -> play
func defaultStateHandler(c *Client, pkt *jp.WirePacket) {
	switch c.State() {
	case jp.StateLogin:
		if pkt.PacketID == packet_ids.S2CHelloID && c.SessionClient != nil {
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

func handleEncryptionRequest(c *Client, pkt *jp.WirePacket) {
	c.Logger.Println("received encryption request")

	var encReq packets.S2CHello
	if err := pkt.ReadInto(&encReq); err != nil {
		c.Logger.Println("parse encryption request:", err)
		return
	}

	encryption := c.Conn().Encryption()
	sharedSecret, err := encryption.GenerateSharedSecret()
	if err != nil {
		c.Logger.Println("gen shared secret:", err)
		return
	}

	encryptedSharedSecret, err := encryption.EncryptWithPublicKey(encReq.PublicKey, sharedSecret)
	if err != nil {
		c.Logger.Println("encrypt shared secret:", err)
		return
	}
	encryptedVerifyToken, err := encryption.EncryptWithPublicKey(encReq.PublicKey, encReq.VerifyToken)
	if err != nil {
		c.Logger.Println("encrypt verify token:", err)
		return
	}

	if c.SessionClient != nil {
		if err := c.SessionClient.Join(c.LoginData.AccessToken, c.LoginData.UUID, string(encReq.ServerId), sharedSecret, encReq.PublicKey); err != nil {
			c.Logger.Println("session join warn:", err)
		}
	}

	resp := &packets.C2SKey{SharedSecret: encryptedSharedSecret, VerifyToken: encryptedVerifyToken}
	if err := c.WritePacket(resp); err != nil {
		c.Logger.Println("send enc resp:", err)
	}

	if err := encryption.EnableEncryption(); err != nil {
		c.Logger.Println("enable encryption:", err)
		return
	}
	c.Logger.Println("encryption enabled")
}

func handleLoginPacket(c *Client, pkt *jp.WirePacket) {
	switch pkt.PacketID {
	case packet_ids.S2CLoginDisconnectID:
		var d packets.S2CLoginDisconnectLogin
		if err := pkt.ReadInto(&d); err != nil {
			c.Logger.Println("login disconnect (parse):", err)
		} else {
			c.Logger.Printf("login disconnect: %s", d.Reason.Text)
		}
		c.Disconnect(false)
	case packet_ids.S2CLoginFinishedID:
		c.Logger.Println("login successful")
		_ = c.WritePacket(&packets.C2SLoginAcknowledged{})
		sendBrandPluginMessage(c, c.Brand)
		sendClientInformation(c)

		c.SetState(jp.StateConfiguration)
		c.Logger.Println("switched from login -> configuration state")
	case packet_ids.S2CLoginCompressionID:
		var d packets.S2CLoginCompression
		if err := pkt.ReadInto(&d); err != nil {
			c.Logger.Println("compression threshold:", err)
		} else {
			c.TCPClient.SetCompressionThreshold(int(d.Threshold))
			c.Logger.Printf("compression enabled: %d", d.Threshold)
		}
	}
}

func handleConfigurationPacket(c *Client, pkt *jp.WirePacket) {
	switch pkt.PacketID {
	case packet_ids.S2CDisconnectConfigurationID:
		var d packets.S2CDisconnectConfiguration
		if err := pkt.ReadInto(&d); err != nil {
			c.Logger.Println("failed to parse disconnect configuration data:", err)
		}
		c.Logger.Printf("disconnected during configuration: %s", d.Reason.Text)
		c.Disconnect(false)
	case packet_ids.S2CFinishConfigurationID:
		_ = c.WritePacket(&packets.C2SFinishConfiguration{})
		c.SetState(jp.StatePlay)
		c.Logger.Println("switched from configuration -> play state")
		time.Sleep(100 * time.Millisecond)
		c.sendChatSessionData()
	case packet_ids.S2CKeepAliveConfigurationID:
		var d packets.S2CKeepAliveConfiguration
		if err := pkt.ReadInto(&d); err == nil {
			_ = c.WritePacket(&packets.C2SKeepAliveConfiguration{KeepAliveId: d.KeepAliveId})
		}
	case packet_ids.S2CSelectKnownPacksID:
		_ = c.WritePacket(&packets.C2SSelectKnownPacks{})
	case packet_ids.S2CResourcePackPushConfigurationID:
		var d packets.S2CResourcePackPushConfiguration
		if err := pkt.ReadInto(&d); err == nil {
			_ = c.WritePacket(&packets.C2SResourcePackConfiguration{
				Uuid:   d.Uuid,
				Result: 0, // successfully downloaded
			})
		}
	}
}

func handlePlayPacket(c *Client, pkt *jp.WirePacket) {
	switch pkt.PacketID {
	case packet_ids.S2CDisconnectPlayID:
		var d packets.S2CDisconnectPlay
		if err := pkt.ReadInto(&d); err == nil {
			c.Logger.Printf("disconnect: %s", d.Reason.Text)
		}
		c.Disconnect(false)
	case packet_ids.S2CStartConfigurationID:
		if c.TreatTransferAsDisconnect {
			c.Logger.Println("server transfer detected, treating as disconnect")
			c.Disconnect(false)
			return
		}

		if err := c.WritePacket(&packets.C2SConfigurationAcknowledged{}); err != nil {
			c.Logger.Println("failed to send configuration_acknowledged:", err)
		}
		c.SetState(jp.StateConfiguration)
		c.Logger.Println("switched from play -> configuration phase, client is probably being transfered to another server")
	case packet_ids.S2CLoginID:
		var d packets.S2CLogin
		if err := pkt.ReadInto(&d); err != nil {
			c.Logger.Println("failed to parse login play data:", err)
			return
		}
		c.Self.EntityID = ns.VarInt(d.EntityId)
		c.Self.DeathLocation = d.DeathLocation
		c.Self.Gamemode = d.GameMode
		c.Logger.Println("spawned; ready")

		// Enable TUI input if in interactive mode
		if c.Interactive {
			c.EnableInput()
		}

		if err := c.WritePacket(&packets.C2SPlayerLoaded{}); err != nil {
			c.Logger.Println("failed to send player loaded:", err)
		}

		if c.AutoRespawn {
			c.Respawn() // health not available yet, just send the packet
		}
	case packet_ids.S2CPlayerChatID:
		var d packets.S2CPlayerChat
		if err := pkt.ReadInto(&d); err == nil {
			c.Logger.Printf("%v", d.ChatType.ChatType)
			if d.ChatType.TargetName.Present {
				c.Logger.Printf("[CHAT-WHISPER] %s -> %s: %s", d.ChatType.Name.Text, d.ChatType.TargetName.Value.Text, d.Body.Content)
			} else {
				c.Logger.Printf("[CHAT] %s: %s", d.ChatType.Name.Text, d.Body.Content)
			}
		}
	case packet_ids.S2CKeepAlivePlayID:
		var d packets.S2CKeepAlivePlay
		if err := pkt.ReadInto(&d); err == nil {
			_ = c.WritePacket(&packets.C2SKeepAlivePlay{KeepAliveId: d.KeepAliveId})
		}
	// vanilla server doesn't use this, but some AC plugins like Grim do
	// and they kick bot if it doesn't respond to this packet (timed out):
	case packet_ids.S2CPingPlayID:
		var d packets.S2CPingPlay
		if err := pkt.ReadInto(&d); err == nil {
			_ = c.WritePacket(&packets.C2SPongPlay{Id: d.Id})
		}
	case packet_ids.S2CSystemChatID:
		var d packets.S2CSystemChat
		if err := pkt.ReadInto(&d); err == nil {
			txt := d.Content.Text
			if d.Overlay {
				c.Logger.Printf("[SYSTEM-ACTION] %s", txt)
			} else {
				c.Logger.Printf("[SYSTEM] %s", txt)
			}
		}
	case packet_ids.S2CDisguisedChatID:
		var d packets.S2CDisguisedChat
		if err := pkt.ReadInto(&d); err == nil {
			msg := d.Message.Text
			sender := d.SenderName.Text
			if d.TargetName.Present {
				c.Logger.Printf("[DISGUISED] %s -> %s: %s", sender, d.TargetName.Value.Text, msg)
			} else {
				c.Logger.Printf("[DISGUISED] %s: %s", sender, msg)
			}
		}
	case packet_ids.S2CPlayerCombatKillID:
		var d packets.S2CPlayerCombatKill
		if err := pkt.ReadInto(&d); err != nil {
			c.Logger.Printf("failed to parse player combat kill data: %s", err)
			return
		}
		if d.PlayerId == c.Self.EntityID {
			c.Logger.Printf("died: %s", d.Message.Text)
			if c.AutoRespawn {
				c.Respawn()
			}
		}
	}
}
