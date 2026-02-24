package protocol

import (
	"bytes"
	"strconv"
	"time"

	"github.com/go-mclib/client/pkg/client"
	"github.com/go-mclib/data/pkg/data/packet_ids"
	"github.com/go-mclib/data/pkg/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

const (
	ModuleName      = "protocol"
	protocolVersion = 774 // 1.21.11
)

// Module drives the client through login -> configuration -> play.
type Module struct {
	client *client.Client

	// TreatTransferAsDisconnect treats S2CStartConfiguration in play state
	// as a disconnect instead of transitioning back to configuration.
	TreatTransferAsDisconnect bool
}

func New() *Module {
	return &Module{}
}

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c
}

func (m *Module) Reset() {}

// From retrieves the protocol module from a client.
func From(c *client.Client) *Module {
	mod := c.Module(ModuleName)
	if mod == nil {
		return nil
	}
	return mod.(*Module)
}

// OnConnect sends handshake and login start after TCP connection.
func (m *Module) OnConnect() {
	c := m.client

	host, port := c.ResolvedAddr()
	portNum, _ := strconv.Atoi(port)

	_ = c.WritePacket(&packets.C2SIntention{
		ProtocolVersion: protocolVersion,
		ServerAddress:   ns.String(host),
		ServerPort:      ns.Uint16(portNum),
		Intent:          2,
	})

	c.SetState(jp.StateLogin)

	uuid, _ := ns.UUIDFromString(c.LoginData.UUID)
	_ = c.WritePacket(&packets.C2SHello{
		Name:       ns.String(c.LoginData.Username),
		PlayerUuid: uuid,
	})
}

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	switch m.client.State() {
	case jp.StateLogin:
		m.handleLogin(pkt)
	case jp.StateConfiguration:
		m.handleConfiguration(pkt)
	case jp.StatePlay:
		m.handlePlay(pkt)
	}
}

func (m *Module) handleLogin(pkt *jp.WirePacket) {
	c := m.client

	if pkt.PacketID == packet_ids.S2CHelloID {
		m.handleEncryptionRequest(pkt)
		return
	}

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
		m.sendBrandPluginMessage()
		m.sendClientInformation()
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

func (m *Module) handleEncryptionRequest(pkt *jp.WirePacket) {
	c := m.client
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

	_ = c.WritePacket(&packets.C2SKey{
		SharedSecret: encryptedSharedSecret,
		VerifyToken:  encryptedVerifyToken,
	})

	if err := encryption.EnableEncryption(); err != nil {
		c.Logger.Println("enable encryption:", err)
		return
	}
	c.Logger.Println("encryption enabled")
}

func (m *Module) handleConfiguration(pkt *jp.WirePacket) {
	c := m.client

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
		// send chat session data if encryption was negotiated
		if c.Conn().Encryption().IsEnabled() {
			if mod := c.Module("chat"); mod != nil {
				if css, ok := mod.(client.ChatSessionSender); ok {
					_ = css.SendChatSessionData()
				}
			}
		}
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
				Result: 0,
			})
		}
	case packet_ids.S2CPingConfigurationID:
		var d packets.S2CPingConfiguration
		if err := pkt.ReadInto(&d); err == nil {
			_ = c.WritePacket(&packets.C2SPongConfiguration{Id: d.Id})
		}
	case packet_ids.S2CCodeOfConductID:
		_ = c.WritePacket(&packets.C2SAcceptCodeOfConduct{})
	}
}

func (m *Module) handlePlay(pkt *jp.WirePacket) {
	c := m.client

	switch pkt.PacketID {
	case packet_ids.S2CDisconnectPlayID:
		var d packets.S2CDisconnectPlay
		if err := pkt.ReadInto(&d); err == nil {
			c.Logger.Printf("disconnect: %s", d.Reason.Text)
		}
		c.Disconnect(false)
	case packet_ids.S2CStartConfigurationID:
		if m.TreatTransferAsDisconnect {
			c.Logger.Println("server transfer detected, treating as disconnect")
			c.Disconnect(false)
			return
		}
		if err := c.WritePacket(&packets.C2SConfigurationAcknowledged{}); err != nil {
			c.Logger.Println("failed to send configuration_acknowledged:", err)
		}
		c.SetState(jp.StateConfiguration)
		c.Logger.Println("switched from play -> configuration phase, client is probably being transfered to another server")
	case packet_ids.S2CKeepAlivePlayID:
		var d packets.S2CKeepAlivePlay
		if err := pkt.ReadInto(&d); err == nil {
			_ = c.WritePacket(&packets.C2SKeepAlivePlay{KeepAliveId: d.KeepAliveId})
		}
	case packet_ids.S2CPingPlayID:
		var d packets.S2CPingPlay
		if err := pkt.ReadInto(&d); err == nil {
			_ = c.WritePacket(&packets.C2SPongPlay{Id: d.Id})
		}
	}
}

func (m *Module) sendClientInformation() {
	_ = m.client.WritePacket(&packets.C2SClientInformationConfiguration{
		Locale:              "en_us",
		ViewDistance:        32,
		ChatMode:            0,
		ChatColors:          true,
		DisplayedSkinParts:  0x7F,
		MainHand:            1,
		EnableTextFiltering: false,
		AllowServerListings: true,
		ParticleStatus:      2,
	})
}

func (m *Module) sendBrandPluginMessage() {
	brand := m.client.Brand
	if brand == "" {
		brand = "vanilla"
	}
	var buf bytes.Buffer
	if err := ns.String(brand).Encode(&buf); err != nil {
		return
	}
	_ = m.client.WritePacket(&packets.C2SCustomPayloadConfiguration{
		Channel: ns.Identifier("minecraft:brand"),
		Data:    ns.ByteArray(buf.Bytes()),
	})
}
