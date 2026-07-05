package chat

import "bytes"

const (
	lastSeenCapacity = 20

	// MaxUnacknowledged is the pending-message count past which the client
	// must send a standalone acknowledgment (C2SChatAck).
	MaxUnacknowledged = 64
)

// lastSeenTracker mirrors vanilla's LastSeenMessagesTracker: a ring of the
// last 20 received signatures plus a count of messages not yet acknowledged.
type lastSeenTracker struct {
	tracked     [lastSeenCapacity]*trackedMessage
	tail        int
	offset      int
	lastTracked []byte
}

type trackedMessage struct {
	signature []byte
	pending   bool
}

// Update carries the last-seen state attached to one outbound signed message.
type Update struct {
	LastSeen     [][]byte // signatures, oldest to newest; covered by the message signature
	Offset       int
	Acknowledged [lastSeenCapacity]bool
	Checksum     byte
}

// AddPending records a received message signature and reports whether it was
// newly tracked (a repeat of the previous signature is ignored).
func (cs *ChatSigner) AddPending(signature []byte) bool {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	t := &cs.tracker
	if bytes.Equal(signature, t.lastTracked) {
		return false
	}
	t.lastTracked = signature
	t.tracked[t.tail] = &trackedMessage{signature: signature, pending: true}
	t.tail = (t.tail + 1) % lastSeenCapacity
	t.offset++
	return true
}

// Offset returns the number of received messages not yet acknowledged.
func (cs *ChatSigner) Offset() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.tracker.offset
}

// GetAndClearOffset returns and resets the pending count, for C2SChatAck.
func (cs *ChatSigner) GetAndClearOffset() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.tracker.getAndClearOffset()
}

// GenerateAndApplyUpdate snapshots the last-seen window for an outbound
// message, marking every tracked entry as acknowledged.
func (cs *ChatSigner) GenerateAndApplyUpdate() Update {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	t := &cs.tracker
	update := Update{Offset: t.getAndClearOffset()}
	for i := range lastSeenCapacity {
		idx := (t.tail + i) % lastSeenCapacity
		msg := t.tracked[idx]
		if msg == nil {
			continue
		}
		update.Acknowledged[i] = true
		update.LastSeen = append(update.LastSeen, msg.signature)
		msg.pending = false
	}
	update.Checksum = lastSeenChecksum(update.LastSeen)
	return update
}

func (t *lastSeenTracker) getAndClearOffset() int {
	offset := t.offset
	t.offset = 0
	return offset
}

// lastSeenChecksum mirrors vanilla's LastSeenMessages.computeChecksum:
// a java-style hash over the signature list, with 0 remapped to 1
// (0 on the wire means "ignore checksum").
func lastSeenChecksum(lastSeen [][]byte) byte {
	h := int32(1)
	for _, signature := range lastSeen {
		h = 31*h + javaByteArrayHash(signature)
	}
	if b := byte(h); b != 0 {
		return b
	}
	return 1
}

func javaByteArrayHash(b []byte) int32 {
	h := int32(1)
	for _, v := range b {
		h = 31*h + int32(int8(v))
	}
	return h
}
