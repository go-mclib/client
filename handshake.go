package client

import (
	"cmp"
	"context"
	"strconv"
	"time"

	"github.com/go-mclib/data"
	"github.com/go-mclib/data/packetids"
	"github.com/go-mclib/data/packets"
	"github.com/go-mclib/protocol/crypto"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// intentLogin is the handshake intent that starts a login.
const intentLogin = 2

// chatSessionSender is implemented by the chat module: the handshake starts
// the signed chat session when play begins on an encrypted connection.
type chatSessionSender interface {
	SendChatSessionData() error
}

// startLogin sends the handshake and login start on a new connection.
func (c *Client) startLogin() {
	s := c.Session()
	host, port := s.ResolvedAddr()
	portNum, _ := strconv.Atoi(port)
	transport := s.link.transport

	_ = transport.WritePacket(&packets.C2SIntention{
		ProtocolVersion: data.ProtocolVersion,
		ServerAddress:   ns.String(host),
		ServerPort:      ns.Uint16(portNum),
		Intent:          intentLogin,
	})
	transport.SetState(jp.StateLogin)

	ld := c.LoginData()
	_ = transport.WritePacket(&packets.C2SHello{
		Name:       ns.String(ld.Username),
		PlayerUUID: ld.UUID,
	})
}

// handleHandshake drives login -> configuration -> play and answers the
// connection-level packets (keep-alive, ping, transfer, disconnect) of state.
func (c *Client) handleHandshake(connCtx context.Context, state jp.State, pkt *jp.WirePacket) {
	switch state {
	case jp.StateLogin:
		c.handleLogin(connCtx, pkt)
	case jp.StateConfiguration:
		c.handleConfiguration(pkt)
	case jp.StatePlay:
		c.handlePlay(connCtx, pkt)
	}
}

func (c *Client) setState(state jp.State) {
	if t := c.transport(); t != nil {
		t.SetState(state)
	}
}

func (c *Client) handleLogin(connCtx context.Context, pkt *jp.WirePacket) {
	switch pkt.PacketID {
	case packetids.S2CHelloID:
		c.handleEncryptionRequest(connCtx, pkt)
	case packetids.S2CLoginDisconnectID:
		var d packets.S2CLoginDisconnectLogin
		if err := pkt.ReadInto(&d); err != nil {
			c.Logger.Println("login disconnect (parse):", err)
		} else {
			c.Logger.Printf("login disconnect: %s", d.Reason)
		}
		_ = c.Disconnect(false)
	case packetids.S2CLoginFinishedID:
		c.Logger.Println("login successful")
		_ = c.WritePacket(&packets.C2SLoginAcknowledged{})
		c.sendBrand()
		c.sendClientInformation()
		c.setState(jp.StateConfiguration)
		c.Logger.Println("switched from login -> configuration state")
	case packetids.S2CLoginCompressionID:
		var d packets.S2CLoginCompression
		if err := pkt.ReadInto(&d); err != nil {
			c.Logger.Println("compression threshold:", err)
		} else if t := c.transport(); t != nil {
			t.SetCompressionThreshold(int(d.Threshold))
			c.Logger.Printf("compression enabled: %d", d.Threshold)
		}
	}
}

func (c *Client) handleEncryptionRequest(connCtx context.Context, pkt *jp.WirePacket) {
	c.Logger.Println("received encryption request")

	// Before C2SKey the server waits until its login timeout; after it the
	// stream is unusable. Either way the connection cannot proceed.
	fail := func(what string, err error) {
		c.Logger.Printf("%s: %v", what, err)
		_ = c.Disconnect(false)
	}

	var encReq packets.S2CHello
	if err := pkt.ReadInto(&encReq); err != nil {
		fail("parse encryption request", err)
		return
	}

	sharedSecret := crypto.NewSharedSecret()
	encryptedSharedSecret, err := crypto.EncryptWithPublicKey(encReq.PublicKey, sharedSecret)
	if err != nil {
		fail("encrypt shared secret", err)
		return
	}
	encryptedVerifyToken, err := crypto.EncryptWithPublicKey(encReq.PublicKey, encReq.VerifyToken)
	if err != nil {
		fail("encrypt verify token", err)
		return
	}

	c.sessionMu.RLock()
	sessionClient, ld := c.sessionClient, c.loginData
	c.sessionMu.RUnlock()
	if sessionClient != nil {
		if err := sessionClient.Join(connCtx, ld.AccessToken, ld.UUID, string(encReq.ServerID), sharedSecret, encReq.PublicKey); err != nil {
			c.Logger.Println("session join warn:", err)
		}
	}

	if err := c.WritePacket(&packets.C2SKey{
		SharedSecret: encryptedSharedSecret,
		VerifyToken:  encryptedVerifyToken,
	}); err != nil {
		fail("send encryption response", err)
		return
	}

	conn := c.Conn()
	if conn == nil {
		fail("enable encryption", ErrNotConnected)
		return
	}
	if err := conn.EnableEncryption(sharedSecret); err != nil {
		fail("enable encryption", err)
		return
	}
	c.Logger.Println("encryption enabled")
}

func (c *Client) handleConfiguration(pkt *jp.WirePacket) {
	switch pkt.PacketID {
	case packetids.S2CSelectKnownPacksID:
		_ = c.WritePacket(&packets.C2SSelectKnownPacks{})
	case packetids.S2CDisconnectConfigurationID:
		var d packets.S2CDisconnectConfiguration
		if err := pkt.ReadInto(&d); err != nil {
			c.Logger.Println("failed to parse disconnect configuration data:", err)
		}
		c.Logger.Printf("disconnected during configuration: %s", d.Reason)
		_ = c.Disconnect(false)
	case packetids.S2CFinishConfigurationID:
		_ = c.WritePacket(&packets.C2SFinishConfiguration{})
		c.setState(jp.StatePlay)
		c.Logger.Println("switched from configuration -> play state")
		time.Sleep(100 * time.Millisecond)
		// send chat session data if encryption was negotiated
		if conn := c.Conn(); conn != nil && conn.Encrypted() {
			if css, ok := c.Module("chat").(chatSessionSender); ok {
				_ = css.SendChatSessionData()
			}
		}
		c.firePlay()
	case packetids.S2CKeepAliveConfigurationID:
		var d packets.S2CKeepAliveConfiguration
		if c.Decode(pkt, &d) {
			_ = c.WritePacket(&packets.C2SKeepAliveConfiguration{KeepAliveID: d.KeepAliveID})
		}
	case packetids.S2CResourcePackPushConfigurationID:
		var d packets.S2CResourcePackPushConfiguration
		if c.Decode(pkt, &d) {
			_ = c.WritePacket(&packets.C2SResourcePackConfiguration{UUID: d.UUID, Result: 0})
		}
	case packetids.S2CPingConfigurationID:
		var d packets.S2CPingConfiguration
		if c.Decode(pkt, &d) {
			_ = c.WritePacket(&packets.C2SPongConfiguration{PingID: d.PingID})
		}
	case packetids.S2CCodeOfConductID:
		_ = c.WritePacket(&packets.C2SAcceptCodeOfConduct{})
	}
}

func (c *Client) handlePlay(connCtx context.Context, pkt *jp.WirePacket) {
	switch pkt.PacketID {
	case packetids.S2CDisconnectPlayID:
		var d packets.S2CDisconnectPlay
		if err := pkt.ReadInto(&d); err == nil {
			c.Logger.Printf("disconnect: %s", d.Reason)
		}
		_ = c.Disconnect(false)
	case packetids.S2CStartConfigurationID:
		if c.TreatTransferAsDisconnect {
			c.Logger.Println("server transfer detected, treating as disconnect")
			_ = c.Disconnect(false)
			return
		}

		c.Logger.Println("server transfer: play -> configuration")
		if s := c.Session(); s != nil {
			c.beginSession(connCtx, s.link)
		}
		c.fireTransfer()

		if err := c.WritePacket(&packets.C2SConfigurationAcknowledged{}); err != nil {
			c.Logger.Println("failed to send configuration_acknowledged:", err)
		}
		c.setState(jp.StateConfiguration)
	case packetids.S2CKeepAlivePlayID:
		var d packets.S2CKeepAlivePlay
		if c.Decode(pkt, &d) {
			_ = c.WritePacket(&packets.C2SKeepAlivePlay{KeepAliveID: d.KeepAliveID})
		}
	case packetids.S2CPingPlayID:
		var d packets.S2CPingPlay
		if c.Decode(pkt, &d) {
			_ = c.WritePacket(&packets.C2SPongPlay{PingID: d.PingID})
		}
	}
}

func (c *Client) sendClientInformation() {
	_ = c.WritePacket(&packets.C2SClientInformationConfiguration{
		Locale:              ns.String(c.Locale),
		ViewDistance:        ns.Int8(c.ViewDistance),
		ChatMode:            0,
		ChatColors:          true,
		DisplayedSkinParts:  0x7F,
		MainHand:            ns.VarInt(c.MainHand),
		EnableTextFiltering: false,
		AllowServerListings: true,
		ParticleStatus:      2,
	})
}

func (c *Client) sendBrand() {
	w := ns.NewWriter()
	if err := w.WriteString(ns.String(cmp.Or(c.Brand, "vanilla"))); err != nil {
		return
	}
	_ = c.WritePacket(&packets.C2SCustomPayloadConfiguration{
		Channel: ns.Identifier("minecraft:brand"),
		Data:    ns.ByteArray(w.Bytes()),
	})
}
