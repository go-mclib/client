package self

import (
	"net"
	"testing"
	"time"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/packetids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

func TestTeleportAcknowledgesResolvedStateWithoutMovementEcho(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c := client.New(listener.Addr().String(), "test", false)
	local, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	remote, err := listener.Accept()
	if err != nil {
		local.Close()
		t.Fatal(err)
	}
	client.NewSimulator(c).Connect(local, jp.StatePlay)
	t.Cleanup(func() { remote.Close(); local.Close() })
	remote.SetDeadline(time.Now().Add(2 * time.Second))
	m := New()
	m.Init(c)
	m.x, m.y, m.z, m.yaw, m.pitch = 10, 64, -20, 90, 30
	wire, err := jp.ToWire(&packets.S2CPlayerPosition{TeleportID: 17, X: 2, Y: 80, Z: -3, Yaw: 15, Pitch: -10, Flags: 0x01 | 0x04 | 0x08 | 0x10})
	if err != nil {
		t.Fatal(err)
	}
	m.handlePlayerPosition(wire)
	// An ordered sentinel proves the acknowledgement is not followed by the old
	// movement echo, without relying on a short socket timeout.
	if err := c.WritePacket(&packets.C2SClientTickEnd{}); err != nil {
		t.Fatal(err)
	}
	response, err := jp.ReadWirePacketFrom(remote, -1)
	if err != nil {
		t.Fatal(err)
	}
	if response.PacketID != packetids.C2SAcceptTeleportationID {
		t.Fatalf("first response is not teleport acknowledgement: %d", response.PacketID)
	}
	var ack packets.C2SAcceptTeleportation
	if err := response.ReadInto(&ack); err != nil {
		t.Fatal(err)
	}
	if ack.TeleportID != 17 || ack.X != 12 || ack.Y != 80 || ack.Z != -23 || ack.Yaw != 105 || ack.Pitch != 20 {
		t.Fatalf("acknowledged unresolved teleport: %+v", ack)
	}
	response, err = jp.ReadWirePacketFrom(remote, -1)
	if err != nil {
		t.Fatal(err)
	}
	if response.PacketID != packetids.C2SClientTickEndID {
		t.Fatalf("teleport emitted an extra movement packet: %d", response.PacketID)
	}
}

func TestCombatKillThenZeroHealthFiresDeathOnce(t *testing.T) {
	c := client.New("unused", "test", false)
	m := New()
	m.Init(c)
	m.SetAutoRespawn(false)
	m.login.EntityID = 7
	deaths := 0
	m.OnDeath(func() { deaths++ })

	m.handleCombatKill(mustWire(t, &packets.S2CPlayerCombatKill{PlayerID: 7}))
	m.handleSetHealth(mustWire(t, &packets.S2CSetHealth{Health: 0, Food: 20, FoodSaturation: 5}))
	if deaths != 1 {
		t.Fatalf("one death fired onDeath %d times", deaths)
	}
	if !m.IsDead() {
		t.Fatal("combat kill did not record death")
	}

	m.handleSetHealth(mustWire(t, &packets.S2CSetHealth{Health: 20, Food: 20, FoodSaturation: 5}))
	m.handleCombatKill(mustWire(t, &packets.S2CPlayerCombatKill{PlayerID: 8}))
	if deaths != 1 || m.IsDead() {
		t.Fatalf("another player's death affected us: deaths=%d dead=%v", deaths, m.IsDead())
	}
}

func mustWire(t *testing.T, p jp.Packet) *jp.WirePacket {
	t.Helper()
	wire, err := jp.ToWire(p)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestLoginPacketTracksRespawnAndGameModeChanges(t *testing.T) {
	c := client.New("unused", "test", false)
	m := New()
	m.Init(c)
	m.login = packets.S2CLogin{
		EntityID: 9, MaxPlayers: 20, DimensionNames: ns.PrefixedArray[ns.Identifier]{"minecraft:overworld", "minecraft:the_nether"},
		DimensionName: "minecraft:overworld", GameMode: 0, PreviousGameMode: -1, SeaLevel: 63, EnforcesSecureChat: true,
	}
	m.vitals.Health = 3

	death := ns.PrefixedOptional[ns.GlobalPos]{Present: true, Value: ns.GlobalPos{Dimension: "minecraft:overworld", Pos: ns.Position{X: 1, Y: 2, Z: 3}}}
	m.handleRespawn(mustWire(t, &packets.S2CRespawn{DimensionType: 1, DimensionName: "minecraft:the_nether", HashedSeed: 42, GameMode: 0, PreviousGameMode: -1, DeathLocation: death, PortalCooldown: 5, SeaLevel: 32}))
	m.handleGameEvent(mustWire(t, &packets.S2CGameEvent{Event: packets.GameEventChangeGameMode, Value: 1}))

	login := m.LoginPacket()
	if login.EntityID != 9 || login.MaxPlayers != 20 || !login.EnforcesSecureChat {
		t.Fatalf("respawn lost login-only fields: %+v", login)
	}
	if login.DimensionType != 1 || login.DimensionName != "minecraft:the_nether" || login.HashedSeed != 42 || login.PortalCooldown != 5 || login.SeaLevel != 32 || login.DeathLocation != death {
		t.Fatalf("respawn fields not applied: %+v", login)
	}
	if login.GameMode != 1 || login.PreviousGameMode != 0 || m.Gamemode() != 1 {
		t.Fatalf("game mode change not applied: %+v", login)
	}
	if v := m.Vitals(); v != defaultVitals {
		t.Fatalf("respawn without kept data kept vitals: %+v", v)
	}
	login.DimensionNames[0] = "mutated"
	if m.LoginPacket().DimensionNames[0] != "minecraft:overworld" {
		t.Fatal("LoginPacket aliases DimensionNames")
	}
}
