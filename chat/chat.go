// Package chat implements client-side signed chat: outbound message signing
// and last-seen message tracking, mirroring the vanilla client.
package chat

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

// ChatSigner signs outbound chat messages and tracks received message
// signatures for the last-seen acknowledgment scheme.
type ChatSigner struct {
	PlayerUUID    ns.UUID
	SessionUUID   ns.UUID
	SessionKey    []byte // mojang signature over the session public key
	KeyExpiry     time.Time
	X509PublicKey []byte

	privateKey *rsa.PrivateKey

	mu           sync.Mutex
	messageIndex int32
	tracker      lastSeenTracker
}

func NewChatSigner() *ChatSigner {
	return &ChatSigner{}
}

func (cs *ChatSigner) SetPrivateKey(privateKey *rsa.PrivateKey) {
	cs.privateKey = privateKey
}

// SignMessage signs a chat message over the given last-seen signatures
// (from GenerateAndApplyUpdate) and returns the 256-byte signature.
func (cs *ChatSigner) SignMessage(message string, timestamp time.Time, salt int64, lastSeen [][]byte) ([]byte, error) {
	if cs.privateKey == nil {
		return nil, fmt.Errorf("private key not set")
	}

	cs.mu.Lock()
	index := cs.messageIndex
	cs.messageIndex++
	cs.mu.Unlock()

	hash := cs.messageHash(index, message, timestamp, salt, lastSeen)
	signature, err := rsa.SignPKCS1v15(rand.Reader, cs.privateKey, crypto.SHA256, hash)
	if err != nil {
		return nil, fmt.Errorf("sign message: %w", err)
	}
	return signature, nil
}

// messageHash computes the SHA-256 digest that gets signed, per
// https://minecraft.wiki/w/Java_Edition_protocol/Chat
func (cs *ChatSigner) messageHash(index int32, message string, timestamp time.Time, salt int64, lastSeen [][]byte) []byte {
	h := sha256.New()

	binary.Write(h, binary.BigEndian, int32(1))
	h.Write(cs.PlayerUUID[:])
	h.Write(cs.SessionUUID[:])
	binary.Write(h, binary.BigEndian, index)
	binary.Write(h, binary.BigEndian, salt)
	binary.Write(h, binary.BigEndian, timestamp.Unix())

	msgBytes := []byte(message)
	binary.Write(h, binary.BigEndian, int32(len(msgBytes)))
	h.Write(msgBytes)

	binary.Write(h, binary.BigEndian, int32(len(lastSeen)))
	for _, signature := range lastSeen {
		h.Write(signature)
	}

	return h.Sum(nil)
}
