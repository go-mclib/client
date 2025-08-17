package chat

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/go-mclib/client/memory"
	ns "github.com/go-mclib/protocol/net_structures"
)

const (
	SignatureVersion = 1
)

type ChatSigner struct {
	store      *memory.ChatChainStore
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
}

func NewChatSigner(store *memory.ChatChainStore) *ChatSigner {
	return &ChatSigner{
		store: store,
	}
}

func (cs *ChatSigner) SetKeys(privateKey *rsa.PrivateKey, publicKey *rsa.PublicKey) {
	cs.privateKey = privateKey
	cs.publicKey = publicKey
	cs.store.SetKeys(privateKey, publicKey)
}

func (cs *ChatSigner) SignMessage(message string, timestamp time.Time, salt int64, lastSeenMessages []memory.MessageRef) (*memory.SignedMessage, error) {
	if cs.privateKey == nil {
		return nil, fmt.Errorf("private key not set")
	}

	messageHash := cs.computeMessageHash(message, timestamp, salt, lastSeenMessages)

	signature, err := rsa.SignPKCS1v15(rand.Reader, cs.privateKey, crypto.SHA256, messageHash)
	if err != nil {
		return nil, fmt.Errorf("failed to sign message: %w", err)
	}

	signedMsg := &memory.SignedMessage{
		Timestamp:        timestamp,
		Salt:             salt,
		MessageHash:      messageHash,
		Signature:        signature,
		LastSeenMessages: lastSeenMessages,
		PlainMessage:     message,
	}

	cs.store.AddOutboundMessage(*signedMsg)

	return signedMsg, nil
}

func (cs *ChatSigner) computeMessageHash(message string, timestamp time.Time, salt int64, lastSeenMessages []memory.MessageRef) []byte {
	h := sha256.New()

	// According to the wiki, the signature is computed over:
	// 1. The number 1 as a 4-byte int (Always 00 00 00 01)
	binary.Write(h, binary.BigEndian, int32(1))

	// 2. The player's 16 byte UUID
	playerUUID := cs.store.GetPlayerUUID()
	h.Write(playerUUID[:])

	// 3. The chat session (16 byte UUID generated randomly by the client)
	sessionUUID := cs.store.GetSessionUUID()
	h.Write(sessionUUID[:])

	// 4. The index of the message within this chat session as a 4-byte int
	messageIndex := cs.store.GetNextMessageIndex()
	binary.Write(h, binary.BigEndian, messageIndex)

	// 5. The salt (from above) as a 8-byte long
	binary.Write(h, binary.BigEndian, salt)

	// 6. The timestamp converted to seconds as a 8-byte long
	binary.Write(h, binary.BigEndian, timestamp.Unix())

	// 7. The length of the message in bytes as a 4-byte int
	msgBytes := []byte(message)
	binary.Write(h, binary.BigEndian, int32(len(msgBytes)))

	// 8. The message bytes
	h.Write(msgBytes)

	// 9. The number of messages in the last seen set, as a 4-byte int (Always in the range [0,20])
	binary.Write(h, binary.BigEndian, int32(len(lastSeenMessages)))

	// 10. For each message in the last seen set, from oldest to newest, the 256 byte signature
	for _, ref := range lastSeenMessages {
		h.Write(ref.Signature)
	}

	return h.Sum(nil)
}

func (cs *ChatSigner) VerifyMessage(msg memory.SignedMessage, publicKey *rsa.PublicKey) error {
	if publicKey == nil {
		storedKey := cs.store.GetPlayerPublicKey(msg.PlayerUUID)
		if storedKey == nil {
			return fmt.Errorf("no public key found for player %s", msg.PlayerUUID)
		}
		publicKey = storedKey
	}

	messageHash := cs.computeMessageHash(msg.PlainMessage, msg.Timestamp, msg.Salt, msg.LastSeenMessages)

	err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, messageHash, msg.Signature)
	if err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}

	return nil
}

func (cs *ChatSigner) VerifyChain(playerUUID ns.UUID, currentSig []byte, previousSig []byte) bool {
	lastSig := cs.store.GetLastSignature(playerUUID)
	if lastSig == nil && previousSig != nil {
		return false
	}
	if lastSig != nil && !bytes.Equal(lastSig, previousSig) {
		return false
	}
	return true
}

type ChatSessionData struct {
	SessionID ns.UUID
	PublicKey *rsa.PublicKey
	KeyExpiry time.Time
	Signature []byte
}

func (cs *ChatSigner) GenerateSessionData() (*ChatSessionData, error) {
	if cs.privateKey == nil || cs.publicKey == nil {
		return nil, fmt.Errorf("keys not set")
	}

	sessionID := ns.UUID{}
	rand.Read(sessionID[:])

	keyExpiry := time.Now().Add(24 * time.Hour)

	publicKeyBytes, err := x509.MarshalPKIXPublicKey(cs.publicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal public key: %w", err)
	}

	h := sha256.New()
	h.Write(sessionID[:])
	binary.Write(h, binary.BigEndian, keyExpiry.UnixMilli())
	h.Write(publicKeyBytes)

	signature, err := rsa.SignPKCS1v15(rand.Reader, cs.privateKey, crypto.SHA256, h.Sum(nil))
	if err != nil {
		return nil, fmt.Errorf("failed to sign session data: %w", err)
	}

	return &ChatSessionData{
		SessionID: sessionID,
		PublicKey: cs.publicKey,
		KeyExpiry: keyExpiry,
		Signature: signature,
	}, nil
}

func (cs *ChatSigner) CreateChatHeader(msg memory.SignedMessage) []byte {
	h := sha256.New()

	h.Write(msg.PreviousSignature)
	h.Write(msg.PlayerUUID[:])

	h.Write(msg.MessageHash)

	return h.Sum(nil)
}

func (cs *ChatSigner) LogSentMessageFromPeer(playerUUID ns.UUID, msg memory.SignedMessage) error {
	cs.store.AddInboundMessage(msg)
	return cs.store.AddPendingAck(playerUUID, msg)
}

func (cs *ChatSigner) ProcessAcknowledgement(playerUUID ns.UUID, signatures [][]byte) {
	cs.store.AcknowledgeMessages(playerUUID, signatures)
}

func (cs *ChatSigner) ShouldKickForPendingAcks(playerUUID ns.UUID) bool {
	return cs.store.GetPendingAckCount(playerUUID) > memory.MaxPendingAckPerPlayer
}
