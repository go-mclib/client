package chat

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-mclib/client"
	chatsign "github.com/go-mclib/client/chat"
	"github.com/go-mclib/data/packet_ids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func testCommandGraph() *commandTree {
	return &commandTree{root: 0, nodes: []commandNode{
		{children: []int{1, 4, 5, 6, 8}, redirect: -1, parser: -1},
		{flags: 1, name: "msg", children: []int{2}, redirect: -1, parser: -1},
		{flags: 2, name: "targets", parser: 6, entityFlags: 2, children: []int{3}, redirect: -1},
		{flags: 6, name: "message", parser: 20, redirect: -1},
		{flags: 1 | 8, name: "tell", redirect: 1, parser: -1},
		{flags: 5, name: "help", redirect: -1, parser: -1},
		{flags: 1, name: "execute", children: []int{7}, redirect: -1, parser: -1},
		{flags: 1 | 8, name: "run", redirect: 0, parser: -1},
		{flags: 1, name: "say", children: []int{3}, redirect: -1, parser: -1},
	}}
}
func TestCommandArgumentSpansAndRedirect(t *testing.T) {
	tree := testCommandGraph()
	for _, command := range []string{`msg @a[name="two words"] hello  @p`, `tell @a[name="two words"] hello  @p`} {
		args, err := tree.arguments(command, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(args) != 1 || args[0].name != "message" || args[0].text != "hello  @p" {
			t.Fatalf("signed the wrong substring: %+v", args)
		}
	}
	if _, err := tree.arguments("msg @a[name=\"unterminated] hello", true); err == nil {
		t.Fatal("malformed signable command accepted")
	}
	if args, err := tree.arguments("help", true); err != nil || len(args) != 0 {
		t.Fatal("discovery became signable")
	}
}
func TestCommandTreeRawDecodingAndInvalidBounds(t *testing.T) {
	// root -> msg -> <message>, with a protocol 776 restricted literal flag.
	b := ns.NewWriter()
	b.WriteVarInt(3)
	b.WriteByte(0)
	b.WriteVarInt(1)
	b.WriteVarInt(1)
	b.WriteByte(0x21)
	b.WriteVarInt(1)
	b.WriteVarInt(2)
	b.WriteString("msg")
	b.WriteByte(6)
	b.WriteVarInt(0)
	b.WriteString("message")
	b.WriteVarInt(20)
	b.WriteVarInt(0)
	tree, err := parseCommandTree(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	args, err := tree.arguments("msg exact  whitespace", true)
	if err != nil || len(args) != 1 || args[0].text != "exact  whitespace" {
		t.Fatalf("decoded graph unusable: %+v %v", args, err)
	}
	for _, bad := range [][]byte{{0}, {1, 0, 1, 2, 0}, append(append([]byte{}, b.Bytes()...), 1), b.Bytes()[:len(b.Bytes())-1]} {
		if _, err := parseCommandTree(bad); err == nil {
			t.Fatalf("malformed graph accepted: %x", bad)
		}
	}
}
func commandSocket(t *testing.T) (*Module, net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c := client.New(listener.Addr().String(), "test", false)
	if _, _, err := c.TCPClient.Connect(listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	remote, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	remote.SetDeadline(time.Now().Add(2 * time.Second))
	t.Cleanup(func() { remote.Close(); c.TCPClient.Close() })
	m := New()
	m.Init(c)
	m.commandTree = testCommandGraph()
	return m, remote
}
func TestSignedCommandUsesNamedArgumentAndVerifiableSignature(t *testing.T) {
	m, remote := commandSocket(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer := chatsign.NewChatSigner()
	signer.SetPrivateKey(key)
	signer.PlayerUUID[0] = 1
	signer.SessionUUID[0] = 2
	m.client.ChatSigner = signer
	if err := m.SendCommand(`/tell "two words" exact  signed text`); err != nil {
		t.Fatal(err)
	}
	wire, err := jp.ReadWirePacketFrom(remote, -1)
	if err != nil {
		t.Fatal(err)
	}
	if wire.PacketID != ns.VarInt(packet_ids.C2SChatCommandSignedID) {
		t.Fatalf("wrong command packet: %d", wire.PacketID)
	}
	// Decode independently of the packet codec: signatures are a named array,
	// and each signature is exactly 256 bytes without a byte-array prefix.
	r := ns.NewReader(wire.Data)
	r.ReadString(32767)
	stamp, _ := r.ReadInt64()
	salt, _ := r.ReadInt64()
	count, _ := r.ReadVarInt()
	name, _ := r.ReadString(16)
	signature, err := r.ReadFixedByteArray(256)
	if err != nil || count != 1 || name != "message" {
		t.Fatalf("invalid named signatures: %d %s %v", count, name, err)
	}
	h := sha256.New()
	binary.Write(h, binary.BigEndian, int32(1))
	h.Write(signer.PlayerUUID[:])
	h.Write(signer.SessionUUID[:])
	binary.Write(h, binary.BigEndian, int32(0))
	binary.Write(h, binary.BigEndian, int64(salt))
	binary.Write(h, binary.BigEndian, int64(stamp)/1000)
	text := "exact  signed text"
	binary.Write(h, binary.BigEndian, int32(len(text)))
	h.Write([]byte(text))
	binary.Write(h, binary.BigEndian, int32(0))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, h.Sum(nil), signature); err != nil {
		t.Fatalf("command signature invalid: %v", err)
	}
	if err := m.SendCommand("help"); err != nil {
		t.Fatal(err)
	}
	wire, err = jp.ReadWirePacketFrom(remote, -1)
	if err != nil {
		t.Fatal(err)
	}
	if wire.PacketID != ns.VarInt(packet_ids.C2SChatCommandID) {
		t.Fatal("ordinary discovery no longer uses unsigned packet")
	}
}
func TestOfflineCommandEnvelopeAndSigningErrors(t *testing.T) {
	m, remote := commandSocket(t)
	if err := m.SendCommand("msg alice hello"); err != nil {
		t.Fatal(err)
	}
	wire, err := jp.ReadWirePacketFrom(remote, -1)
	if err != nil {
		t.Fatal(err)
	}
	var packet packets.C2SChatCommandSigned
	if err := wire.ReadInto(&packet); err != nil {
		t.Fatal(err)
	}
	if len(packet.ArgumentSignatures) != 0 {
		t.Fatal("offline session fabricated a signature")
	}
	m.client.ChatSigner = chatsign.NewChatSigner()
	if err := m.SendCommand("msg alice hello"); err == nil || !strings.Contains(err.Error(), "private key") {
		t.Fatalf("missing signing key error was suppressed: %v", err)
	}
	for _, command := range []string{"help\nstop", "//help", "help\x00"} {
		if err := m.SendCommand(command); err == nil {
			t.Fatalf("unsafe command accepted: %q", command)
		}
	}
	m.client.FireTransfer()
	if _, err := m.Commands(); err == nil {
		t.Fatal("transfer retained old server commands")
	}
}

func TestCommandMessageAndSelectorBoundaries(t *testing.T) {
	tree := testCommandGraph()
	for _, tc := range []struct {
		command string
		text    string
	}{
		{"msg alice  hello", " hello"},
		{"tell alice   hello", "  hello"},
		{"say  ", " "},
		{"say " + strings.Repeat("a", 256), strings.Repeat("a", 256)},
		{"say " + strings.Repeat("\U0001f600", 128), strings.Repeat("\U0001f600", 128)},
		{"say " + strings.Repeat("a", 254) + "\U0001f600", strings.Repeat("a", 254) + "\U0001f600"},
		{"say mail@example.test @unknown @", "mail@example.test @unknown @"},
	} {
		args, err := tree.arguments(tc.command, true)
		if err != nil || len(args) != 1 || args[0].text != tc.text {
			t.Fatalf("%q: got %+v, %v", tc.command, args, err)
		}
	}
	for _, target := range []string{
		"alice", `"two words"`, "@a", "@p", "@r", "@s", "@a[]", "@a[ ]",
		`@a[name="two words",tag=!busy,team=blue]`,
		`@a[name=!alice,name=!bob,tag=,tag=available]`,
		`@a[limit=1,sort=nearest,distance=..10,x=1.5,y=-2,z=3,dx=4,dy=5,dz=6]`,
		`@a[level=1..10,gamemode=!spectator]`, `@a[name=alice,]`,
		`@e[type=minecraft:player]`, `@n[type=player]`, `@e[level=1]`,
	} {
		args, err := tree.arguments("msg "+target+" hello", true)
		if err != nil || len(args) != 1 || args[0].text != "hello" {
			t.Fatalf("valid target %q: got %+v, %v", target, args, err)
		}
	}
	for _, command := range []string{"execute run say hello", "execute run tell alice hello"} {
		args, err := tree.arguments(command, true)
		if err != nil || len(args) != 0 {
			t.Fatalf("root redirect collected signatures: %+v %v", args, err)
		}
	}
}

func TestEntityArgumentFlagsAndUUIDs(t *testing.T) {
	for _, tc := range []struct {
		target string
		flags  byte
		valid  bool
	}{
		{"@a", 1, false}, {"@a[limit=1]", 1, true},
		{"@p[limit=2]", 1, false}, {"@e", 2, false},
		{"@e[type=player]", 2, true}, {"@e[type=!player]", 2, false},
		{"@s", 3, true}, {"@n", 1, true}, {"@n", 3, false},
		{"@e[type=player,limit=1]", 3, true},
		{"dd12be42-52a9-4a91-a8a1-11c01849e498", 1, true},
		{"dd12be42-52a9-4a91-a8a1-11c01849e498", 2, false},
		{"dd12be42-52a9-4a91-a8a1-11c01849e49z", 0, false},
		{"1-1-1-1-1", 0, false},
	} {
		tree := testCommandGraph()
		tree.nodes[2].entityFlags = tc.flags
		_, err := tree.arguments("msg "+tc.target+" hello", true)
		if (err == nil) != tc.valid {
			t.Errorf("target %q flags %d: %v", tc.target, tc.flags, err)
		}
	}
}

func TestCommandTreeRetainsEntityRestrictions(t *testing.T) {
	b := ns.NewWriter()
	b.WriteVarInt(4)
	b.WriteByte(0)
	b.WriteVarInt(1)
	b.WriteVarInt(1)
	b.WriteByte(1)
	b.WriteVarInt(1)
	b.WriteVarInt(2)
	b.WriteString("msg")
	b.WriteByte(2)
	b.WriteVarInt(1)
	b.WriteVarInt(3)
	b.WriteString("target")
	b.WriteVarInt(6)
	b.WriteByte(3)
	b.WriteByte(6)
	b.WriteVarInt(0)
	b.WriteString("message")
	b.WriteVarInt(20)
	b.WriteVarInt(0)
	tree, err := parseCommandTree(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"@a", "@e[limit=1]"} {
		if _, err := tree.arguments("msg "+target+" hello", true); err == nil {
			t.Fatalf("wire entity restrictions lost for %s", target)
		}
	}
	if _, err := tree.arguments("msg @a[limit=1] hello", true); err != nil {
		t.Fatal(err)
	}
}

// This fixture supplies the same synchronized-accessor contract as self.Module;
// tests change it only between SendCommand calls, never concurrently.
type commandPermissions struct{ level int8 }

func (p *commandPermissions) Name() string                { return "self" }
func (p *commandPermissions) Init(*client.Client)         {}
func (p *commandPermissions) HandlePacket(*jp.WirePacket) {}
func (p *commandPermissions) Reset()                      { p.level = 0 }
func (p *commandPermissions) OpLevel() int8               { return p.level }

func TestRejectedCommandsDoNotAdvanceSigningChain(t *testing.T) {
	m, remote := commandSocket(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer := chatsign.NewChatSigner()
	signer.SetPrivateKey(key)
	m.client.ChatSigner = signer
	pending := make([]byte, 256)
	pending[0] = 7
	signer.AddPending(pending)
	// Missing permission state and level 1 are not selector grants in 26.2.
	if err := m.SendCommand("msg @a hello"); err == nil {
		t.Fatal("selector allowed without permission state")
	}
	permissions := &commandPermissions{level: 1}
	m.client.Register(permissions)
	if err := m.SendCommand("msg @a hello"); err == nil {
		t.Fatal("level 1 incorrectly granted selector permission")
	}
	permissions.level = 2
	m.commandTree.nodes[2].parser = 16
	if err := m.SendCommand("msg {} hello"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported predecessor guessed a successful parse: %v", err)
	}
	m.commandTree.nodes[2].parser = 6
	m.commandTree.nodes[1].children = []int{2, 3}
	if err := m.SendCommand("msg alice hello"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("competing argument parsers guessed a successful branch: %v", err)
	}
	m.commandTree.nodes[1].children = []int{2}
	m.commandTree.nodes[2].entityFlags = 3
	if err := m.SendCommand("msg @a hello"); err == nil {
		t.Fatal("single-player parser accepted multiple targets")
	}
	m.commandTree.nodes[2].entityFlags = 2
	for _, command := range []string{
		"msg alice " + strings.Repeat("a", 257),
		"msg alice " + strings.Repeat("\U0001f600", 129),
		"msg alice " + strings.Repeat("a", 255) + "\U0001f600",
		"msg abcdefghijklmnopq hello",
		"msg dd12be42-52a9-4a91-a8a1-11c01849e49z hello",
		"msg @unknown hello", "msg @ hello", "msg @a[ hello",
		"msg @a[unknown=x] hello", "msg @a[limit=0] hello",
		"msg @a[limit=2147483648] hello", "msg @a[sort=backwards] hello",
		"msg @s[limit=1] hello", "msg @s[sort=nearest] hello",
		"msg @a[distance=-1] hello", "msg @a[distance=2..1] hello",
		"msg @a[level=1.5] hello", "msg @a[x=NaN] hello",
		"msg @a[gamemode=invalid] hello", "msg @a[type=unknown] hello",
		"msg @a[name=alice,name=bob] hello", "msg @a[name=!alice,name=bob] hello",
		"msg @a[name=alice, ] hello", `msg @a[name="bad\q"] hello`,
		"msg @a[nbt={}] hello", "msg @a[scores={foo=1}] hello",
		"msg @a[type=#minecraft:skeletons] hello",
		"msg @e hello", "msg  alice hello",
		"msg alice hello @a[limit=0]", "msg alice hello @a[nbt={}]",
	} {
		if err := m.SendCommand(command); err == nil {
			t.Fatalf("unsafe command accepted: %q", command)
		}
		if signer.Offset() != 1 {
			t.Fatalf("rejection consumed pending acknowledgment: %q", command)
		}
	}
	// Root redirects must not consume a signature index or acknowledgment.
	if err := m.SendCommand("execute run say hello"); err != nil {
		t.Fatal(err)
	}
	wire, err := jp.ReadWirePacketFrom(remote, -1)
	if err != nil || wire.PacketID != ns.VarInt(packet_ids.C2SChatCommandID) {
		t.Fatalf("root redirect was not unsigned: %v %v", wire, err)
	}
	if err := m.SendCommand("tell @a[limit=1]  hello"); err != nil {
		t.Fatal(err)
	}
	wire, err = jp.ReadWirePacketFrom(remote, -1)
	if err != nil {
		t.Fatal(err)
	}
	var packet packets.C2SChatCommandSigned
	if err := wire.ReadInto(&packet); err != nil {
		t.Fatal(err)
	}
	if packet.MessageCount != 1 || len(packet.ArgumentSignatures) != 1 || packet.ArgumentSignatures[0].Name != "message" {
		t.Fatalf("rejections changed next signed envelope: %+v", packet)
	}
	// Verify as the server would: first chain index, original pending message,
	// and the leading space Brigadier leaves inside MessageArgument.
	h := sha256.New()
	binary.Write(h, binary.BigEndian, int32(1))
	h.Write(signer.PlayerUUID[:])
	h.Write(signer.SessionUUID[:])
	binary.Write(h, binary.BigEndian, int32(0))
	binary.Write(h, binary.BigEndian, int64(packet.Salt))
	binary.Write(h, binary.BigEndian, int64(packet.Timestamp)/1000)
	text := " hello"
	binary.Write(h, binary.BigEndian, int32(len(text)))
	h.Write([]byte(text))
	binary.Write(h, binary.BigEndian, int32(1))
	h.Write(pending)
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, h.Sum(nil), packet.ArgumentSignatures[0].Signature[:]); err != nil {
		t.Fatalf("rejected command or root redirect desynchronized next signature: %v", err)
	}
}
