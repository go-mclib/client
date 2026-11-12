package self

import (
	"math"

	"github.com/go-mclib/data/packets"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// WorldPosToYawPitch calculates yaw and pitch to look from (x,y,z) at (lookX,lookY,lookZ).
// MC yaw: 0=south(+Z), 90=west(-X), 180=north(-Z), 270=east(+X).
func WorldPosToYawPitch(x, y, z, lookX, lookY, lookZ float64) (yaw, pitch float64) {
	dx := lookX - x
	dy := lookY - y
	dz := lookZ - z
	yaw = -math.Atan2(dx, dz) * 180 / math.Pi
	pitch = -math.Atan2(dy, math.Sqrt(dx*dx+dz*dz)) * 180 / math.Pi
	return
}

func (m *Module) Move(x, y, z float64, onGround, pushingAgainstWall bool) error {
	var flags ns.Int8
	if onGround {
		flags |= 0x01
	}
	if pushingAgainstWall {
		flags |= 0x02
	}

	return m.client.WritePacket(&packets.C2SMovePlayerPos{
		X:     ns.Float64(x),
		FeetY: ns.Float64(y),
		Z:     ns.Float64(z),
		Flags: flags,
	})
}

func (m *Module) MoveRelative(dx, dy, dz float64, onGround, pushingAgainstWall bool) error {
	m.mu.RLock()
	x, y, z := m.x+dx, m.y+dy, m.z+dz
	m.mu.RUnlock()
	return m.Move(x, y, z, onGround, pushingAgainstWall)
}

// LookAt sets yaw/pitch to face the given world position from the current eye position.
func (m *Module) LookAt(x, y, z float64) {
	m.mu.Lock()
	yaw, pitch := WorldPosToYawPitch(m.x, m.y+m.eyeHeightLocked(), m.z, x, y, z)
	m.yaw = float32(yaw)
	m.pitch = float32(pitch)
	m.mu.Unlock()
}

// SetRotation updates yaw and pitch.
func (m *Module) SetRotation(yaw, pitch float64) {
	m.mu.Lock()
	m.yaw = float32(yaw)
	m.pitch = float32(pitch)
	m.mu.Unlock()
}

func (m *Module) Rotate(deltaYaw, deltaPitch float64) {
	m.mu.Lock()
	newYaw := float64(m.yaw) + deltaYaw
	newPitch := float64(m.pitch) + deltaPitch

	newPitch = max(-90, min(90, newPitch))
	newYaw = math.Mod(math.Mod(newYaw, 360)+360, 360)
	m.yaw = float32(newYaw)
	m.pitch = float32(newPitch)
	m.mu.Unlock()
}

func (m *Module) Respawn() error {
	return m.client.WritePacket(&packets.C2SClientCommand{ActionID: 0})
}
