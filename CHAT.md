# Minecraft 1.21.8 Secure Chat Implementation Guide

This document provides a complete, detailed guide for implementing Minecraft's secure chat signing system in Go. After extensive debugging and testing, this represents the exact requirements needed to successfully send signed chat messages that appear in the server chat log.

## Overview

Minecraft 1.19+ introduced a secure chat signing system that requires players to cryptographically sign their chat messages using RSA-2048 keys provided by Mojang. The server validates these signatures to ensure message authenticity and prevent tampering.

## Phase 1: Authentication & Certificate Acquisition

### 1.1 Microsoft Authentication

First, obtain a valid Microsoft/Mojang access token through the OAuth2 flow:

```go
// This requires implementing the full Microsoft OAuth2 flow
// Redirect user to Microsoft login and capture authorization code
accessToken := "your_microsoft_access_token"
```

### 1.2 Fetch Player Certificates

Request player certificates from Mojang's API:

```go
url := "https://api.minecraftservices.com/player/certificates"
req, _ := http.NewRequest("POST", url, nil)
req.Header.Set("Authorization", "Bearer " + accessToken)
req.Header.Set("Content-Type", "application/json")

// Critical: Response contains TWO signatures - always use V2!
type MojangCertificate struct {
    ExpiresAt            string        `json:"expiresAt"`
    KeyPair              MojangKeyPair `json:"keyPair"`
    PublicKeySignature   string        `json:"publicKeySignature"`   // 256 bytes - DON'T USE!
    PublicKeySignatureV2 string        `json:"publicKeySignatureV2"` // 512 bytes - USE THIS!
    RefreshedAfter       string        `json:"refreshedAfter"`
}

type MojangKeyPair struct {
    PrivateKey string `json:"privateKey"` // PEM format RSA-2048 private key
    PublicKey  string `json:"publicKey"`  // PEM format RSA-2048 public key
}
```

### 1.3 Process Certificate Data

```go
// Parse RSA private key from PEM
block, _ := pem.Decode([]byte(cert.KeyPair.PrivateKey))
privateKey, _ := x509.ParsePKCS1PrivateKey(block.Bytes)

// Parse RSA public key from PEM
block, _ = pem.Decode([]byte(cert.KeyPair.PublicKey))
publicKeyInterface, _ := x509.ParsePKIXPublicKey(block.Bytes)
publicKey := publicKeyInterface.(*rsa.PublicKey)

// CRITICAL: Convert public key to SPKI DER format
spkiPublicKeyBytes, _ := x509.MarshalPKIXPublicKey(publicKey)

// CRITICAL: Use PublicKeySignatureV2 (512 bytes), not PublicKeySignature (256 bytes)
mojangSignatureV2, _ := base64.StdEncoding.DecodeString(cert.PublicKeySignatureV2)

// Parse expiry time
expiryTime, _ := time.Parse(time.RFC3339, cert.ExpiresAt)
```

## Phase 2: Connection & Protocol Handshake

### 2.1 Standard Minecraft Connection

```go
// TCP connection
conn, _ := net.Dial("tcp", "server:25565")

// Handshake packet (0x00) in Handshake state
handshakePacket := {
    ProtocolVersion: 769,        // VarInt - Minecraft 1.21.8
    ServerAddress:   "localhost", // String
    ServerPort:      25565,      // Unsigned Short
    Intent:          2,          // VarInt - 2 for login
}

// Login Start packet (0x00) in Login state
loginStartPacket := {
    Name:       "PlayerName",               // String
    PlayerUuid: uuid.Parse("player-uuid"), // UUID (16 bytes)
}

// Handle encryption request/response (standard Minecraft protocol)
// Handle login success and transition to Configuration state
```

### 2.2 Configuration Phase

```go
// Login Acknowledged (0x03) - enter Configuration state
// Client Information (0x00) - send client settings
// Brand Plugin Message (0x01) - send client brand
// Configuration Acknowledged (0x02) - enter Play state
```

## Phase 3: The Critical Player Session Packet

This is where most implementations fail. The Player Session packet (0x09) must be sent immediately upon entering the Play state.

### 3.1 Player Session Packet Structure

```go
// Generate random session UUID (NOT the player UUID!)
var sessionUUID [16]byte
rand.Read(sessionUUID[:])

// Prepare packet data with exact field ordering
sessionPacket := {
    SessionId:    sessionUUID,              // UUID (16 bytes) - MUST be random!
    ExpiresAt:    expiryTime.UnixMilli(),   // Long (8 bytes) - MUST be milliseconds!
    PublicKey:    spkiPublicKeyBytes,       // Prefixed Byte Array - MUST be SPKI DER!
    KeySignature: mojangSignatureV2,       // Prefixed Byte Array - MUST be 512 bytes!
}
```

### 3.2 Wire Format (Manual Serialization)

```go
func serializePlayerSessionPacket(sessionUUID [16]byte, expiresAt int64, publicKey []byte, signature []byte) []byte {
    var buf bytes.Buffer
    
    // Session UUID (16 bytes)
    buf.Write(sessionUUID[:])
    
    // Expires At (8 bytes, big-endian milliseconds)
    binary.Write(&buf, binary.BigEndian, uint64(expiresAt))
    
    // Public Key (VarInt length + data)
    writeVarInt(&buf, len(publicKey))
    buf.Write(publicKey)
    
    // Signature (VarInt length + data)
    writeVarInt(&buf, len(signature))
    buf.Write(signature)
    
    return buf.Bytes()
}

func writeVarInt(buf *bytes.Buffer, value int) {
    for value >= 0x80 {
        buf.WriteByte(byte(value&0x7F | 0x80))
        value >>= 7
    }
    buf.WriteByte(byte(value))
}
```

### 3.3 Critical Details

**Session UUID**:

- Must be randomly generated, NOT the player's UUID
- Equivalent to `uuid.v4fast()` in `node-minecraft-protocol`
- Each connection should use a different session UUID

**Expires At**:

- Timestamp in milliseconds (not seconds!)
- Use `expiryTime.UnixMilli()`
- Matches certificate expiry from Mojang

**Public Key**:

- Must be in SPKI DER format (not X.509 or PKCS#1)
- Use `x509.MarshalPKIXPublicKey(publicKey)`
- This is the format that `crypto.createPublicKey({ type: 'spki', format: 'der' })` produces in Node.js (`node-minecraft-protocol`)

**Key Signature**:

- Must use `PublicKeySignatureV2` (512 bytes)
- Never use `PublicKeySignature` (256 bytes) - servers will reject it
- This is Mojang's signature that authenticates your public key

## Phase 4: Sending Chat Messages

### 4.1 Chat Message Packet Structure

```go
message := "hello, world!"
timestamp := time.Now().UnixMilli()
salt := rand.Int63()

// Create signature for the message
signableData := createSignableMessage(message, timestamp, salt, sessionUUID, messageIndex, acknowledgments)
hasher := sha256.New()
hasher.Write(signableData)
messageHash := hasher.Sum(nil)

signature, _ := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, messageHash)

chatPacket := {
    Message:      message,                  // String
    Timestamp:    timestamp,                // Long (milliseconds)
    Salt:         salt,                     // Long
    Signature:    signature,                // Optional Byte Array (nil if unsigned)
    Offset:       0,                        // VarInt - acknowledgment offset
    Acknowledged: []byte{0, 0, 0},         // Fixed Bit Set (3 bytes)
}
```

### 4.2 Message Signing Process

```go
func createSignableMessage(message string, timestamp int64, salt int64, sessionUUID [16]byte, messageIndex int32, acknowledgments [][]byte) []byte {
    var buf bytes.Buffer
    
    // Protocol format for signable data:
    binary.Write(&buf, binary.BigEndian, int32(1))           // Signature version
    buf.Write(playerUUID[:])                                 // Player UUID (16 bytes)
    buf.Write(sessionUUID[:])                                // Session UUID (16 bytes)
    binary.Write(&buf, binary.BigEndian, messageIndex)       // Message index
    binary.Write(&buf, binary.BigEndian, salt)               // Salt
    binary.Write(&buf, binary.BigEndian, timestamp/1000)     // Timestamp in seconds
    binary.Write(&buf, binary.BigEndian, int32(len(message))) // Message length
    buf.WriteString(message)                                 // Message content
    
    // Append acknowledgments
    binary.Write(&buf, binary.BigEndian, int32(len(acknowledgments)))
    for _, ack := range acknowledgments {
        buf.Write(ack)
    }
    
    return buf.Bytes()
}
```

## Phase 5: Implementation Notes

### 5.1 Encryption Details

- **Connection Encryption**: AES-128-CFB8 after encryption request
- **Message Signing**: RSA-2048 with SHA-256, PKCS#1 v1.5 padding
- **Certificate Validation**: Server uses RSA with SHA-1 to verify Mojang's signature

### 5.2 Common Failure Points

1. **Wrong Signature Version**: Using `PublicKeySignature` instead of `PublicKeySignatureV2`
   - Error: "Bad signature length: got 256 but was expecting 512"

2. **Wrong Session UUID**: Using player UUID instead of random session UUID
   - Error: Various validation failures

3. **Wrong Timestamp Format**: Using seconds instead of milliseconds
   - Error: "Invalid signature for profile public key"

4. **Wrong Public Key Format**: Using X.509 or PKCS#1 instead of SPKI DER
   - Error: Key format validation failures

5. **Packet Timing**: Not sending Player Session packet immediately after entering Play state
   - Error: "Chat disabled due to missing profile public key"

### 5.3 Server Validation Process

The Minecraft server validates:

1. **Certificate Signature**: Verifies Mojang's signature against known public keys
2. **Certificate Expiry**: Ensures certificate hasn't expired
3. **Public Key Format**: Must be valid SPKI DER encoding
4. **Signature Length**: Must be exactly 512 bytes for V2 signatures
5. **Message Signatures**: Validates each chat message signature against player's public key

### 5.4 Success Indicators

- [x] No "Invalid signature for profile public key" errors in server logs
- [x] No "Bad signature length" errors in server logs  
- [x] Chat messages appear in server logs as `<PlayerName> message`
- [x] Server shows successful login without certificate warnings

## Phase 6: Protocol Flow Summary

```plaintext
Handshake (0x00) → Login Start (0x00) → [Encryption Exchange] → Login Success (0x02) 
→ Login Acknowledged (0x03) → Client Information (0x00) → Brand Plugin (0x01) 
→ Configuration Acknowledged (0x02) → **Player Session (0x09)** → Chat Message (0x07)
```

The **Player Session packet (0x09)** is the critical moment where most implementations fail. Get this packet right with:

- [x] `PublicKeySignatureV2` (512 bytes)
- [x] SPKI DER public key format
- [x] Millisecond timestamps
- [x] Random session UUID

And your chat messages will successfully appear in the server chat log.

## Implementation Reference

This guide is based on successful implementation in Go that achieves working secure chat with Minecraft 1.21.8 servers. The key breakthrough was discovering the need to use `PublicKeySignatureV2` instead of the older `PublicKeySignature` format.

For a working reference implementation, see the `example_client.go` file in this repository, specifically the `sendChatSessionData()` function and certificate processing logic.
