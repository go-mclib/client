package signing_test

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/binary"
	"testing"
	"time"

	"github.com/go-mclib/client/modules/chat/internal/signing"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

func sig(fill byte) []byte {
	s := make([]byte, 256)
	for i := range s {
		s[i] = fill
	}
	return s
}

func TestEmptyUpdate(t *testing.T) {
	cs := signing.NewSigner()
	update := cs.GenerateAndApplyUpdate()

	if update.Offset != 0 || len(update.LastSeen) != 0 {
		t.Fatalf("expected empty update, got offset=%d lastSeen=%d", update.Offset, len(update.LastSeen))
	}
	// java hash of an empty list is 1
	if update.Checksum != 1 {
		t.Fatalf("expected checksum 1 for empty last-seen, got %d", update.Checksum)
	}
}

func TestAddPendingDedupe(t *testing.T) {
	cs := signing.NewSigner()
	if !cs.AddPending(sig(1)) {
		t.Fatal("first signature should be tracked")
	}
	if cs.AddPending(sig(1)) {
		t.Fatal("repeat of previous signature should be ignored")
	}
	if !cs.AddPending(sig(2)) {
		t.Fatal("new signature should be tracked")
	}
	if cs.Offset() != 2 {
		t.Fatalf("expected offset 2, got %d", cs.Offset())
	}
}

func TestGenerateAndApplyUpdate(t *testing.T) {
	cs := signing.NewSigner()
	cs.AddPending(sig(1))
	cs.AddPending(sig(2))

	update := cs.GenerateAndApplyUpdate()
	if update.Offset != 2 {
		t.Fatalf("expected offset 2, got %d", update.Offset)
	}
	if len(update.LastSeen) != 2 || !bytes.Equal(update.LastSeen[0], sig(1)) || !bytes.Equal(update.LastSeen[1], sig(2)) {
		t.Fatal("last-seen should hold both signatures, oldest first")
	}
	// newest entries occupy the highest acknowledged bits
	for i, want := range map[int]bool{17: false, 18: true, 19: true} {
		if update.Acknowledged[i] != want {
			t.Fatalf("acknowledged[%d] = %v, want %v", i, update.Acknowledged[i], want)
		}
	}
	if update.Checksum == 0 {
		t.Fatal("checksum must never be 0")
	}

	// a second update keeps the window but resets the offset
	again := cs.GenerateAndApplyUpdate()
	if again.Offset != 0 || len(again.LastSeen) != 2 {
		t.Fatalf("expected offset 0 with same window, got offset=%d lastSeen=%d", again.Offset, len(again.LastSeen))
	}
}

func TestWindowCapsAtTwenty(t *testing.T) {
	cs := signing.NewSigner()
	for i := range 25 {
		cs.AddPending(sig(byte(i + 1)))
	}
	update := cs.GenerateAndApplyUpdate()
	if update.Offset != 25 {
		t.Fatalf("expected offset 25, got %d", update.Offset)
	}
	if len(update.LastSeen) != 20 {
		t.Fatalf("expected window of 20, got %d", len(update.LastSeen))
	}
	if !bytes.Equal(update.LastSeen[0], sig(6)) || !bytes.Equal(update.LastSeen[19], sig(25)) {
		t.Fatal("window should hold the 20 newest signatures, oldest first")
	}
}

func TestGetAndClearOffset(t *testing.T) {
	cs := signing.NewSigner()
	cs.AddPending(sig(1))
	cs.AddPending(sig(2))
	if got := cs.GetAndClearOffset(); got != 2 {
		t.Fatalf("expected 2, got %d", got)
	}
	if cs.Offset() != 0 {
		t.Fatalf("offset should reset, got %d", cs.Offset())
	}
}

func TestSignMessage(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cs := signing.NewSigner()
	cs.SetPrivateKey(key)
	cs.PlayerUUID[0] = 0xAA
	var session ns.UUID
	session[0] = 0xBB
	cs.StartSession(session)

	timestamp := time.Unix(1700000000, 0)
	lastSeen := [][]byte{sig(1), sig(2)}
	signature, err := cs.SignMessage("hello", timestamp, 42, lastSeen)
	if err != nil {
		t.Fatal(err)
	}

	// recompute the signed digest per the wiki layout and verify
	h := sha256.New()
	binary.Write(h, binary.BigEndian, int32(1))
	h.Write(cs.PlayerUUID[:])
	h.Write(session[:])
	binary.Write(h, binary.BigEndian, int32(0)) // first message index
	binary.Write(h, binary.BigEndian, int64(42))
	binary.Write(h, binary.BigEndian, timestamp.Unix())
	binary.Write(h, binary.BigEndian, int32(len("hello")))
	h.Write([]byte("hello"))
	binary.Write(h, binary.BigEndian, int32(len(lastSeen)))
	for _, s := range lastSeen {
		h.Write(s)
	}
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, h.Sum(nil), signature); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}

	// message index must advance per signed message
	second, err := cs.SignMessage("hello", timestamp, 42, lastSeen)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(signature, second) {
		t.Fatal("second signature should differ (message index advanced)")
	}
}

func TestSignMessageWithoutKey(t *testing.T) {
	cs := signing.NewSigner()
	if _, err := cs.SignMessage("hello", time.Now(), 0, nil); err == nil {
		t.Fatal("expected error without private key")
	}
}

func TestStartSessionResetsChainAndLastSeen(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cs := signing.NewSigner()
	cs.SetPrivateKey(key)
	cs.StartSession(ns.UUID{1})
	cs.AddPending(sig(1))
	cs.AddPending(sig(2))
	if _, err := cs.SignMessage("first", time.Now(), 0, nil); err != nil {
		t.Fatal(err)
	}

	session := ns.UUID{2}
	cs.StartSession(session)
	if cs.SessionUUID() != session {
		t.Fatal("session UUID not replaced")
	}
	update := cs.GenerateAndApplyUpdate()
	if update.Offset != 0 || len(update.LastSeen) != 0 || update.Checksum != 1 {
		t.Fatalf("last-seen state survived new session: %+v", update)
	}
	if cs.AddPending(sig(2)) != true {
		t.Fatal("dedupe state survived new session")
	}

	// a fresh session starts the message chain at index 0
	timestamp := time.Unix(1700000000, 0)
	signature, err := cs.SignMessage("hello", timestamp, 7, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	binary.Write(h, binary.BigEndian, int32(1))
	h.Write(cs.PlayerUUID[:])
	h.Write(session[:])
	binary.Write(h, binary.BigEndian, int32(0))
	binary.Write(h, binary.BigEndian, int64(7))
	binary.Write(h, binary.BigEndian, timestamp.Unix())
	binary.Write(h, binary.BigEndian, int32(len("hello")))
	h.Write([]byte("hello"))
	binary.Write(h, binary.BigEndian, int32(0))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, h.Sum(nil), signature); err != nil {
		t.Fatalf("message index not reset: %v", err)
	}
}
