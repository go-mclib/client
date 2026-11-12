// Package signing implements client-side signed chat: outbound message signing
// and last-seen message tracking, mirroring the vanilla client.
package signing

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// Signer signs outbound chat messages and tracks received message
// signatures for the last-seen acknowledgment scheme.
//
// The exported key fields are set once before the signer is shared; the chat
// session, message index and last-seen state are guarded by an internal lock.
type Signer struct {
	PlayerUUID    ns.UUID
	SessionKey    []byte // mojang signature over the session public key
	KeyExpiry     time.Time
	X509PublicKey []byte

	privateKey *rsa.PrivateKey

	mu           sync.Mutex
	sessionUUID  ns.UUID
	messageIndex int32
	tracker      lastSeenTracker
}

func NewSigner() *Signer {
	return &Signer{}
}

func (cs *Signer) SetPrivateKey(privateKey *rsa.PrivateKey) {
	cs.privateKey = privateKey
}

// StartSession begins a new chat session: like vanilla's server, which
// creates a fresh SignedMessageChain and LastSeenMessagesValidator for every
// chat session update, the message index and last-seen state restart.
func (cs *Signer) StartSession(id ns.UUID) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.sessionUUID = id
	cs.messageIndex = 0
	cs.tracker = lastSeenTracker{}
}

// SessionUUID returns the current chat session ID.
func (cs *Signer) SessionUUID() ns.UUID {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.sessionUUID
}

// SignMessage signs a chat message over the given last-seen signatures
// (from GenerateAndApplyUpdate) and returns the 256-byte signature.
func (cs *Signer) SignMessage(message string, timestamp time.Time, salt int64, lastSeen [][]byte) ([]byte, error) {
	if cs.privateKey == nil {
		return nil, errors.New("private key not set")
	}

	cs.mu.Lock()
	index := cs.messageIndex
	cs.messageIndex++
	session := cs.sessionUUID
	cs.mu.Unlock()

	hash := cs.messageHash(session, index, message, timestamp, salt, lastSeen)
	signature, err := rsa.SignPKCS1v15(rand.Reader, cs.privateKey, crypto.SHA256, hash)
	if err != nil {
		return nil, fmt.Errorf("sign message: %w", err)
	}
	return signature, nil
}

// messageHash computes the SHA-256 digest that gets signed, per
// https://minecraft.wiki/w/Java_Edition_protocol/Chat
func (cs *Signer) messageHash(session ns.UUID, index int32, message string, timestamp time.Time, salt int64, lastSeen [][]byte) []byte {
	buf := make([]byte, 0, 4+16+16+4+8+8+4+len(message)+4+len(lastSeen)*256)
	buf = binary.BigEndian.AppendUint32(buf, 1)
	buf = append(buf, cs.PlayerUUID[:]...)
	buf = append(buf, session[:]...)
	buf = binary.BigEndian.AppendUint32(buf, uint32(index))
	buf = binary.BigEndian.AppendUint64(buf, uint64(salt))
	buf = binary.BigEndian.AppendUint64(buf, uint64(timestamp.Unix()))
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(message)))
	buf = append(buf, message...)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(lastSeen)))
	for _, signature := range lastSeen {
		buf = append(buf, signature...)
	}
	sum := sha256.Sum256(buf)
	return sum[:]
}
