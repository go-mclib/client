package client

import (
	"encoding/binary"
	"math/rand"
	"time"

	packets "github.com/go-mclib/data/go/772/java_packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/net_structures"
)

func (c *Client) SendChatMessage(message string) {
	if c.ChatSigner != nil {
		// signed message
		saltBytes := make([]byte, 8)
		rand.Read(saltBytes)
		salt := int64(binary.BigEndian.Uint64(saltBytes))
		timestamp := time.Now()
		lastSeen := c.ChatSigner.GetLastSeenMessages(20)
		signedMsg, err := c.ChatSigner.SignMessage(message, timestamp, salt, lastSeen)
		if err != nil {
			c.Logger.Println("sign chat message:", err)
			return
		}
		ack := ns.FixedBitSet{Length: 20, Data: make([]byte, 3)}
		pkt, err := packets.C2SChat.WithData(packets.C2SChatData{
			Message:      ns.String(message),
			Timestamp:    ns.Long(timestamp.UnixMilli()),
			Salt:         ns.Long(salt),
			Signature:    ns.PrefixedOptional[ns.ByteArray]{Present: true, Value: ns.ByteArray(signedMsg.Signature)},
			MessageCount: ns.VarInt(len(lastSeen)),
			Acknowledged: ack,
			Checksum:     ns.Byte(0),
		})
		if err != nil {
			c.Logger.Println("build signed chat:", err)
			return
		}
		_ = c.WritePacket(pkt)
		return
	}

	// unsigned
	pkt, err := packets.C2SChat.WithData(packets.C2SChatData{
		Message:      ns.String(message),
		Timestamp:    ns.Long(time.Now().UnixMilli()),
		Salt:         ns.Long(0),
		Signature:    ns.PrefixedOptional[ns.ByteArray]{},
		MessageCount: ns.VarInt(0),
		Acknowledged: ns.FixedBitSet{Length: 20, Data: make([]byte, 3)},
		Checksum:     ns.Byte(0),
	})
	if err != nil {
		c.Logger.Println("build unsigned chat:", err)
		return
	}
	_ = c.WritePacket(pkt)
}

func (c *Client) sendChatSessionData() {
	if c.ChatSigner == nil {
		return
	}

	var sessionID ns.UUID
	rand.Read(sessionID[:])
	c.ChatSigner.SessionUUID = sessionID

	pub := c.ChatSigner.X509PublicKey
	if len(pub) == 0 {
		return
	}
	expiresAt := ns.Long(c.ChatSigner.KeyExpiry.UnixMilli())
	mojangSig := c.ChatSigner.SessionKey

	pubBytes := make([]ns.Byte, len(pub))
	for i, b := range pub {
		pubBytes[i] = ns.Byte(b)
	}
	sigBytes := make([]ns.Byte, len(mojangSig))
	for i, b := range mojangSig {
		sigBytes[i] = ns.Byte(b)
	}

	pkt, err := jp.NewPacket(jp.StatePlay, jp.C2S, 0x09).WithData(packets.C2SChatSessionUpdateData{
		SessionId:    sessionID,
		ExpiresAt:    expiresAt,
		PublicKey:    ns.PrefixedArray[ns.Byte](pubBytes),
		KeySignature: ns.PrefixedArray[ns.Byte](sigBytes),
	})
	if err != nil {
		c.Logger.Println("build chat session packet:", err)
		return
	}
	_ = c.WritePacket(pkt)
}
