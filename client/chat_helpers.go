package client

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/go-mclib/data/pkg/packets"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func (c *Client) SendChatMessage(message string) error {
	if len(message) > 256 {
		return fmt.Errorf("chat message too long: %d", len(message))
	}

	if c.ChatSigner != nil {
		// signed message
		saltBytes := make([]byte, 8)
		rand.Read(saltBytes)
		salt := int64(binary.BigEndian.Uint64(saltBytes))
		timestamp := time.Now()
		lastSeen := c.ChatSigner.GetLastSeenMessages(20)
		signedMsg, err := c.ChatSigner.SignMessage(message, timestamp, salt, lastSeen)
		if err != nil {
			return err
		}
		pkt := &packets.C2SChat{
			Message:      ns.String(message),
			Timestamp:    ns.Int64(timestamp.UnixMilli()),
			Salt:         ns.Int64(salt),
			Signature:    ns.PrefixedOptional[ns.ByteArray]{Present: true, Value: ns.ByteArray(signedMsg.Signature)},
			MessageCount: ns.VarInt(len(lastSeen)),
			Acknowledged: ns.NewFixedBitSet(20),
			Checksum:     ns.Int8(0),
		}
		return c.WritePacket(pkt)
	}

	// unsigned
	pkt := &packets.C2SChat{
		Message:      ns.String(message),
		Timestamp:    ns.Int64(time.Now().UnixMilli()),
		Salt:         ns.Int64(0),
		Signature:    ns.PrefixedOptional[ns.ByteArray]{},
		MessageCount: ns.VarInt(0),
		Acknowledged: ns.NewFixedBitSet(20),
		Checksum:     ns.Int8(0),
	}
	return c.WritePacket(pkt)
}

func (c *Client) SendCommand(command string) error {
	// if starts with /, remove it
	command = strings.TrimPrefix(command, "/")
	pkt := &packets.C2SChatCommand{
		Command: ns.String(command),
	}

	return c.WritePacket(pkt)
}

func (c *Client) sendChatSessionData() error {
	if c.ChatSigner == nil {
		return fmt.Errorf("no chat signer")
	}

	var sessionID ns.UUID
	rand.Read(sessionID[:])
	c.ChatSigner.SessionUUID = sessionID

	pub := c.ChatSigner.X509PublicKey
	if len(pub) == 0 {
		return fmt.Errorf("no public key")
	}
	expiresAt := ns.Int64(c.ChatSigner.KeyExpiry.UnixMilli())
	mojangSig := c.ChatSigner.SessionKey

	pkt := &packets.C2SChatSessionUpdate{
		SessionId:    sessionID,
		ExpiresAt:    expiresAt,
		PublicKey:    ns.ByteArray(pub),
		KeySignature: ns.ByteArray(mojangSig),
	}
	return c.WritePacket(pkt)
}
