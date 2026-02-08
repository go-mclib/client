package self

import (
	"math"

	"github.com/go-mclib/data/pkg/packets"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func (m *Module) Move(x, y, z float64) error {
	return m.client.WritePacket(&packets.C2SMovePlayerPos{
		X:     ns.Float64(x),
		FeetY: ns.Float64(y),
		Z:     ns.Float64(z),
		Flags: 0x01, // on ground
	})
}

func (m *Module) MoveRelative(dx, dy, dz float64) error {
	return m.Move(float64(m.X)+dx, float64(m.Y)+dy, float64(m.Z)+dz)
}

func (m *Module) LookAt(x, y, z float64) error {
	yaw, pitch := WorldPosToYawPitch(float64(m.X), float64(m.Y)+EyeHeight, float64(m.Z), x, y, z)
	return m.SetRotation(yaw, pitch)
}

func (m *Module) SetRotation(yaw, pitch float64) error {
	m.Yaw = ns.Float32(yaw)
	m.Pitch = ns.Float32(pitch)
	return m.client.WritePacket(&packets.C2SMovePlayerRot{
		Yaw:   ns.Float32(yaw),
		Pitch: ns.Float32(pitch),
		Flags: 0x01, // on ground
	})
}

func (m *Module) Rotate(deltaYaw, deltaPitch float64) error {
	newYaw := float64(m.Yaw) + deltaYaw
	newPitch := float64(m.Pitch) + deltaPitch

	if newPitch > 90 {
		newPitch = 90
	} else if newPitch < -90 {
		newPitch = -90
	}
	for newYaw < 0 {
		newYaw += 360
	}
	for newYaw >= 360 {
		newYaw -= 360
	}
	return m.SetRotation(newYaw, newPitch)
}

func (m *Module) Respawn() error {
	return m.client.WritePacket(&packets.C2SClientCommand{ActionId: 0})
}

func (m *Module) UseAt(hand int8, yaw, pitch float64) error {
	return m.client.WritePacket(&packets.C2SUseItem{
		Hand:     ns.VarInt(hand),
		Sequence: 0,
		Yaw:      ns.Float32(yaw),
		Pitch:    ns.Float32(pitch),
	})
}

func (m *Module) Use(hand int8) error {
	return m.UseAt(hand, float64(m.Yaw), float64(m.Pitch))
}

// WorldPosToYawPitch calculates yaw and pitch to look from (x,y,z) at (lookX,lookY,lookZ).
// Matches MC convention: yaw 0=south(+Z), 90=west(-X), -90/270=east(+X), 180=north(-Z).
func WorldPosToYawPitch(x, y, z, lookX, lookY, lookZ float64) (yaw, pitch float64) {
	dx := lookX - x
	dy := lookY - y
	dz := lookZ - z
	yaw = math.Atan2(dz, dx)*180/math.Pi - 90
	pitch = -math.Atan2(dy, math.Sqrt(dx*dx+dz*dz)) * 180 / math.Pi
	return
}
