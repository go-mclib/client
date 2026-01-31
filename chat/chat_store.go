package chat

import (
	"bytes"
	"crypto"
	"crypto/rsa"
	"sync"
	"time"

	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

const (
	MaxChatHistory         = 1000
	MaxPendingAckPerPlayer = 100
)

type SignedMessage struct {
	PlayerUUID        ns.UUID
	Timestamp         time.Time
	Salt              int64
	MessageHash       []byte
	Signature         []byte
	PreviousSignature []byte
	LastSeenMessages  []MessageRef
	PlainMessage      string
	Acknowledged      bool
}

type MessageRef struct {
	PlayerUUID ns.UUID
	Signature  []byte
}

type PlayerChatState struct {
	UUID          ns.UUID
	PublicKey     *rsa.PublicKey
	LastSignature []byte
	MessageChain  []SignedMessage
	PendingAcks   []SignedMessage
}

type ChatChainStore struct {
	PrivateKey     crypto.PrivateKey
	PublicKey      crypto.PublicKey
	SessionKey     []byte // Mojang signature
	KeyExpiry      time.Time
	PlayerUUID     ns.UUID
	SessionUUID    ns.UUID
	X509PublicKey  []byte
	PKCS1PublicKey []byte

	mu              sync.RWMutex
	playerStates    map[ns.UUID]*PlayerChatState
	inboundHistory  []SignedMessage
	outboundHistory []SignedMessage
	messageIndex    int32
}

func NewChatChainStore() *ChatChainStore {
	return &ChatChainStore{
		playerStates:    make(map[ns.UUID]*PlayerChatState),
		inboundHistory:  make([]SignedMessage, 0, MaxChatHistory),
		outboundHistory: make([]SignedMessage, 0, MaxChatHistory),
	}
}

func (c *ChatChainStore) AddPlayerPublicKey(playerUUID ns.UUID, publicKey *rsa.PublicKey) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.playerStates[playerUUID]; !exists {
		c.playerStates[playerUUID] = &PlayerChatState{
			UUID:         playerUUID,
			PublicKey:    publicKey,
			MessageChain: make([]SignedMessage, 0),
			PendingAcks:  make([]SignedMessage, 0),
		}
	} else {
		c.playerStates[playerUUID].PublicKey = publicKey
	}
}

func (c *ChatChainStore) GetPlayerPublicKey(playerUUID ns.UUID) *rsa.PublicKey {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if state, exists := c.playerStates[playerUUID]; exists {
		return state.PublicKey
	}
	return nil
}

func (c *ChatChainStore) AddInboundMessage(msg SignedMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.inboundHistory = append(c.inboundHistory, msg)
	if len(c.inboundHistory) > MaxChatHistory {
		c.inboundHistory = c.inboundHistory[1:]
	}

	if state, exists := c.playerStates[msg.PlayerUUID]; exists {
		state.MessageChain = append(state.MessageChain, msg)
		state.LastSignature = msg.Signature

		if len(state.MessageChain) > MaxChatHistory {
			state.MessageChain = state.MessageChain[1:]
		}
	}
}

func (c *ChatChainStore) AddOutboundMessage(msg SignedMessage) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.outboundHistory = append(c.outboundHistory, msg)
	if len(c.outboundHistory) > MaxChatHistory {
		c.outboundHistory = c.outboundHistory[1:]
	}

	return msg.Signature
}

func (c *ChatChainStore) GetNextMessageIndex() int32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	index := c.messageIndex
	c.messageIndex++
	return index
}

func (c *ChatChainStore) GetLastSignature(playerUUID ns.UUID) []byte {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if state, exists := c.playerStates[playerUUID]; exists {
		return state.LastSignature
	}
	return nil
}

func (c *ChatChainStore) AddPendingAck(playerUUID ns.UUID, msg SignedMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	state, exists := c.playerStates[playerUUID]
	if !exists {
		state = &PlayerChatState{
			UUID:         playerUUID,
			MessageChain: make([]SignedMessage, 0),
			PendingAcks:  make([]SignedMessage, 0),
		}
		c.playerStates[playerUUID] = state
	}

	state.PendingAcks = append(state.PendingAcks, msg)
	return nil
}

func (c *ChatChainStore) AcknowledgeMessages(playerUUID ns.UUID, signatures [][]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	state, exists := c.playerStates[playerUUID]
	if !exists {
		return
	}

	newPending := make([]SignedMessage, 0)
	for _, msg := range state.PendingAcks {
		acknowledged := false
		for _, sig := range signatures {
			if bytes.Equal(msg.Signature, sig) {
				acknowledged = true
				break
			}
		}
		if !acknowledged {
			newPending = append(newPending, msg)
		}
	}
	state.PendingAcks = newPending
}

func (c *ChatChainStore) GetPendingAckCount(playerUUID ns.UUID) int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if state, exists := c.playerStates[playerUUID]; exists {
		return len(state.PendingAcks)
	}
	return 0
}

func (c *ChatChainStore) GetLastSeenMessages(count int) []MessageRef {
	c.mu.RLock()
	defer c.mu.RUnlock()

	refs := make([]MessageRef, 0, count)
	start := len(c.inboundHistory) - count
	if start < 0 {
		start = 0
	}

	for i := start; i < len(c.inboundHistory); i++ {
		msg := c.inboundHistory[i]
		refs = append(refs, MessageRef{
			PlayerUUID: msg.PlayerUUID,
			Signature:  msg.Signature,
		})
	}

	return refs
}

func (c *ChatChainStore) ClearPlayerState(playerUUID ns.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.playerStates, playerUUID)
}
