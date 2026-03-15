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

	// recorded config-phase packets (for proxy replay)
	configPackets []*jp.WirePacket
	configDone    bool

	// recorded play-phase opaque packets
	commandsPacket    *jp.WirePacket
	playerInfoPackets []*jp.WirePacket
	waypointPackets   []*jp.WirePacket
	bossEventPackets  map[ns.UUID]*jp.WirePacket // keyed by boss bar UUID
}

func New() *Module {
	return &Module{
		bossEventPackets: make(map[ns.UUID]*jp.WirePacket),
	}
}

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c
	c.OnConnect(m.onConnect)
	c.OnTransfer(m.Reset)
}

func (m *Module) Reset() {
	m.configPackets = nil
	m.configDone = false
	m.commandsPacket = nil
	m.playerInfoPackets = nil
	m.waypointPackets = nil
	clear(m.bossEventPackets)
}

// From retrieves the protocol module from a client.
func From(c *client.Client) *Module {
	mod := c.Module(ModuleName)
	if mod == nil {
		return nil
	}
	return mod.(*Module)
}

func (m *Module) onConnect() {
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
			c.Logger.Printf("login disconnect: %s", d.Reason)
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

	// record config packets for replay (skip protocol-level ones)
	if !m.configDone {
		switch pkt.PacketID {
		case packet_ids.S2CFinishConfigurationID,
			packet_ids.S2CKeepAliveConfigurationID,
			packet_ids.S2CPingConfigurationID,
			packet_ids.S2CDisconnectConfigurationID:
			// don't record
		default:
			m.configPackets = append(m.configPackets, pkt.Clone())
		}
	}

	switch pkt.PacketID {
	case packet_ids.S2CDisconnectConfigurationID:
		var d packets.S2CDisconnectConfiguration
		if err := pkt.ReadInto(&d); err != nil {
			c.Logger.Println("failed to parse disconnect configuration data:", err)
		}
		c.Logger.Printf("disconnected during configuration: %s", d.Reason)
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
		c.FirePlay()
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

	// mark config as done on first play packet
	if !m.configDone {
		m.configDone = true
	}

	// record opaque play-state packets
	switch pkt.PacketID {
	case packet_ids.S2CCommandsID:
		m.commandsPacket = pkt.Clone()
	case packet_ids.S2CPlayerInfoUpdateID, packet_ids.S2CPlayerInfoRemoveID:
		m.playerInfoPackets = append(m.playerInfoPackets, pkt.Clone())
	case packet_ids.S2CWaypointID:
		m.waypointPackets = append(m.waypointPackets, pkt.Clone())
	case packet_ids.S2CBossEventID:
		m.recordBossEvent(pkt)
	}

	switch pkt.PacketID {
	case packet_ids.S2CDisconnectPlayID:
		var d packets.S2CDisconnectPlay
		if err := pkt.ReadInto(&d); err == nil {
			c.Logger.Printf("disconnect: %s", d.Reason)
		}
		c.Disconnect(false)
	case packet_ids.S2CStartConfigurationID:
		if m.TreatTransferAsDisconnect {
			c.Logger.Println("server transfer detected, treating as disconnect")
			c.Disconnect(false)
			return
		}

		c.Logger.Println("server transfer: play -> configuration")
		c.FireTransfer()

		if err := c.WritePacket(&packets.C2SConfigurationAcknowledged{}); err != nil {
			c.Logger.Println("failed to send configuration_acknowledged:", err)
		}
		c.SetState(jp.StateConfiguration)
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

// ConfigDone returns whether configuration phase has completed.
func (m *Module) ConfigDone() bool { return m.configDone }

// ConfigPackets returns the recorded configuration-phase packets.
func (m *Module) ConfigPackets() []*jp.WirePacket {
	result := make([]*jp.WirePacket, len(m.configPackets))
	copy(result, m.configPackets)
	return result
}

// CommandsPacket returns the last recorded commands packet, or nil.
func (m *Module) CommandsPacket() *jp.WirePacket { return m.commandsPacket }

// PlayerInfoPackets returns all recorded player info update/remove packets.
func (m *Module) PlayerInfoPackets() []*jp.WirePacket {
	result := make([]*jp.WirePacket, len(m.playerInfoPackets))
	copy(result, m.playerInfoPackets)
	return result
}

// WaypointPackets returns all recorded waypoint packets.
func (m *Module) WaypointPackets() []*jp.WirePacket {
	result := make([]*jp.WirePacket, len(m.waypointPackets))
	copy(result, m.waypointPackets)
	return result
}

// recordBossEvent tracks boss bar add/remove for proxy replay.
func (m *Module) recordBossEvent(pkt *jp.WirePacket) {
	var d packets.S2CBossEvent
	if err := pkt.ReadInto(&d); err != nil {
		return
	}
	switch d.Action {
	case packets.BossEventActionAdd:
		m.bossEventPackets[d.Uuid] = pkt.Clone()
	case packets.BossEventActionRemove:
		delete(m.bossEventPackets, d.Uuid)
	}
}

// BossEventPackets returns stored boss bar "add" packets for replay.
func (m *Module) BossEventPackets() []*jp.WirePacket {
	result := make([]*jp.WirePacket, 0, len(m.bossEventPackets))
	for _, pkt := range m.bossEventPackets {
		result = append(result, pkt)
	}
	return result
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
