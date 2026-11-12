package physics

import (
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/data/packets"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// sendTickPackets sends input, position and tick-end packets at the end of a tick.
func (m *Module) sendTickPackets(s *self.Module) {
	// send input state (vanilla: LocalPlayer.tick sends C2SPlayerInput before sendPosition)
	m.sendInput(s)

	// send position (calls sendIsSprintingIfNeeded equivalent first, matching vanilla)
	m.sendPosition(s)

	// tick end (vanilla: Minecraft.tick sends ClientTickEnd after all tick logic)
	m.sendPacket(&packets.C2SClientTickEnd{})
}

// sendInput sends C2SPlayerInput when key states change (vanilla: LocalPlayer.tick).
// flags: forward(1), backward(2), left(4), right(8), jump(16), shift(32), sprint(64)
func (m *Module) sendInput(s *self.Module) {
	var flags uint8
	if m.forwardImpulse > 0 {
		flags |= 1
	}
	if m.forwardImpulse < 0 {
		flags |= 2
	}
	if m.strafeImpulse > 0 {
		flags |= 4
	}
	if m.strafeImpulse < 0 {
		flags |= 8
	}
	if m.jumping {
		flags |= 16
	}
	if s.Sneaking() {
		flags |= 32
	}
	if s.Sprinting() {
		flags |= 64
	}

	if flags != m.lastSentInputFlags {
		m.lastSentInputFlags = flags
		m.sendPacket(&packets.C2SPlayerInput{
			Flags: ns.Uint8(flags),
		})
	}
}

// sendPosition sends position/rotation packets following MC's LocalPlayer.sendPosition logic.
// Sprint commands precede movement; sneaking is carried only by PlayerInput.
func (m *Module) sendPosition(s *self.Module) {
	// sendIsSprintingIfNeeded
	sprinting := s.Sprinting()
	if sprinting != m.lastSentSprinting {
		m.lastSentSprinting = sprinting
		actionID := ns.VarInt(2) // stop sprinting
		if sprinting {
			actionID = 1 // start sprinting
		}
		m.sendPacket(&packets.C2SPlayerCommand{
			EntityID: ns.VarInt(s.EntityID()),
			ActionID: actionID,
		})
	}

	m.positionReminder++

	x, y, z := s.Position()
	yaw, pitch := s.Rotation()

	dx := x - m.lastSentX
	dy := y - m.lastSentY
	dz := z - m.lastSentZ
	moved := (dx*dx+dy*dy+dz*dz) > positionThresholdSq || m.positionReminder >= positionReminderMax
	rotated := yaw != m.lastSentYaw || pitch != m.lastSentPitch

	var flags ns.Int8
	if m.onGround {
		flags = 0x01
	}
	if m.horizontalCollision {
		flags |= 0x02
	}

	if moved && rotated {
		m.sendPacket(&packets.C2SMovePlayerPosRot{
			X: ns.Float64(x), FeetY: ns.Float64(y), Z: ns.Float64(z),
			Yaw: ns.Float32(yaw), Pitch: ns.Float32(pitch),
			Flags: flags,
		})
	} else if moved {
		m.sendPacket(&packets.C2SMovePlayerPos{
			X: ns.Float64(x), FeetY: ns.Float64(y), Z: ns.Float64(z),
			Flags: flags,
		})
	} else if rotated {
		m.sendPacket(&packets.C2SMovePlayerRot{
			Yaw: ns.Float32(yaw), Pitch: ns.Float32(pitch),
			Flags: flags,
		})
	} else if m.onGround != m.lastSentOnGround || m.horizontalCollision != m.lastSentHorizontalCollision {
		m.sendPacket(&packets.C2SMovePlayerStatusOnly{
			Flags: flags,
		})
	}

	if moved {
		m.lastSentX = x
		m.lastSentY = y
		m.lastSentZ = z
		m.positionReminder = 0
	}
	if rotated {
		m.lastSentYaw = yaw
		m.lastSentPitch = pitch
	}
	m.lastSentOnGround = m.onGround
	m.lastSentHorizontalCollision = m.horizontalCollision
}
