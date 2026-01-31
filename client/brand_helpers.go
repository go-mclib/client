package client

import (
	"bytes"

	"github.com/go-mclib/data/packets"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func sendClientInformation(c *Client) {
	pkt := &packets.C2SClientInformationConfiguration{
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
	_ = c.WritePacket(pkt)
}

func sendBrandPluginMessage(c *Client, brand string) {
	var buf bytes.Buffer
	if err := ns.String(brand).Encode(&buf); err != nil {
		return
	}
	pkt := &packets.C2SCustomPayloadConfiguration{
		Channel: ns.Identifier("minecraft:brand"),
		Data:    ns.ByteArray(buf.Bytes()),
	}
	_ = c.WritePacket(pkt)
}
