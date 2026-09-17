package self

import (
	"net"
	"testing"
	"time"

	"github.com/go-mclib/client"
	"github.com/go-mclib/data/packet_ids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
)

func TestTeleportAcknowledgesResolvedStateWithoutMovementEcho(t *testing.T) {
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
		c.TCPClient.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { remote.Close(); c.TCPClient.Close() })
	remote.SetDeadline(time.Now().Add(2 * time.Second))
	m := New()
	m.Init(c)
	m.x, m.y, m.z, m.yaw, m.pitch = 10, 64, -20, 90, 30
	wire, err := jp.ToWire(&packets.S2CPlayerPosition{TeleportId: 17, X: 2, Y: 80, Z: -3, Yaw: 15, Pitch: -10, Flags: 0x01 | 0x04 | 0x08 | 0x10})
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
	if response.PacketID != packet_ids.C2SAcceptTeleportationID {
		t.Fatalf("first response is not teleport acknowledgement: %d", response.PacketID)
	}
	var ack packets.C2SAcceptTeleportation
	if err := response.ReadInto(&ack); err != nil {
		t.Fatal(err)
	}
	if ack.TeleportId != 17 || ack.X != 12 || ack.Y != 80 || ack.Z != -23 || ack.Yaw != 105 || ack.Pitch != 20 {
		t.Fatalf("acknowledged unresolved teleport: %+v", ack)
	}
	response, err = jp.ReadWirePacketFrom(remote, -1)
	if err != nil {
		t.Fatal(err)
	}
	if response.PacketID != packet_ids.C2SClientTickEndID {
		t.Fatalf("teleport emitted an extra movement packet: %d", response.PacketID)
	}
}
