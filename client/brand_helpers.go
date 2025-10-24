package client

import (
	packets "github.com/go-mclib/data/go/773/java_packets"
	ns "github.com/go-mclib/protocol/net_structures"
)

func sendClientInformation(c *Client) {
	info := packets.C2SClientInformationConfigurationData{
		Locale:              "en_us",
		ViewDistance:        32,
		ChatMode:            0,
		ChatColors:          true,
		DisplayedSkinParts:  0x7F,
		MainHand:            1,
		EnableTextFiltering: false,
		AllowServerListings: true,
		ParticleStatus:      2,
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
