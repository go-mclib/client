package client

import (
	"math"
	"sync"
	"time"

	packets "github.com/go-mclib/data/go/772/java_packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/net_structures"
)

type PhysicsController struct {
	client *Client
	mu     sync.RWMutex

	velocityX float64
	velocityY float64
	velocityZ float64

	gravity     float64
	airDrag     float64
	groundDrag  float64
	terminalVel float64

	onGround bool
	enabled  bool
	stopChan chan struct{}

	// Knockback configuration
	knockbackHorizontal    float64
	knockbackVertical      float64
	knockbackAirMultiplier float64
	knockbackResistance    float64 // 0.0 = no resistance, 1.0 = full resistance
}

func NewPhysicsController(client *Client) *PhysicsController {
	return &PhysicsController{
		client:      client,
		gravity:     -0.08,
		airDrag:     0.98,
		groundDrag:  0.6,
		terminalVel: -3.92,
		onGround:    false,
		enabled:     false,
		stopChan:    make(chan struct{}),
		// Default knockback values (vanilla-like)
		knockbackHorizontal:    0.4,
		knockbackVertical:      0.4,
		knockbackAirMultiplier: 0.6,
		knockbackResistance:    0.0,
	}
}

func (p *PhysicsController) SetVelocity(x, y, z float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.velocityX = x
	p.velocityY = y
	p.velocityZ = z
}

func (p *PhysicsController) GetVelocity() (x, y, z float64) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.velocityX, p.velocityY, p.velocityZ
}

func (p *PhysicsController) AddVelocity(dx, dy, dz float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.velocityX += dx
	p.velocityY += dy
	p.velocityZ += dz
}

func (p *PhysicsController) SetOnGround(onGround bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onGround = onGround
}

func (p *PhysicsController) IsOnGround() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.onGround
}

// SetKnockbackStrength sets the horizontal and vertical knockback strength
func (p *PhysicsController) SetKnockbackStrength(horizontal, vertical float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.knockbackHorizontal = horizontal
	p.knockbackVertical = vertical
}

// SetKnockbackResistance sets the knockback resistance (0.0 to 1.0)
func (p *PhysicsController) SetKnockbackResistance(resistance float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Clamp between 0 and 1
	if resistance < 0 {
		resistance = 0
	} else if resistance > 1 {
		resistance = 1
	}
	p.knockbackResistance = resistance
}

// SetKnockbackAirMultiplier sets the air knockback multiplier
func (p *PhysicsController) SetKnockbackAirMultiplier(multiplier float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.knockbackAirMultiplier = multiplier
}

// ResetPhysics resets velocity and ground state
func (p *PhysicsController) ResetPhysics() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.velocityX = 0
	p.velocityY = 0
	p.velocityZ = 0
	p.onGround = true
}

func (p *PhysicsController) Start() {
	p.mu.Lock()
	if p.enabled {
		p.mu.Unlock()
		return
	}
	p.enabled = true
	p.stopChan = make(chan struct{})
	p.mu.Unlock()

	go p.physicsLoop()
}

func (p *PhysicsController) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.enabled {
		return
	}
	p.enabled = false
	close(p.stopChan)
}

func (p *PhysicsController) physicsLoop() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopChan:
			return
		case <-ticker.C:
			p.tick()
		}
	}
}

func (p *PhysicsController) tick() {
	if p.client.GetState() != jp.StatePlay {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	currentY := float64(p.client.Self.Y)
	if p.velocityY < 0 && currentY < -128 {
		// hit the void damage threshold, stop falling
		p.onGround = true
		p.velocityY = 0
		return // don't send position updates below void
	}

	if !p.onGround && p.client.HasGravity {
		p.velocityY += p.gravity
		if p.velocityY < p.terminalVel {
			p.velocityY = p.terminalVel
		}
	}

	drag := p.airDrag
	if p.onGround {
		drag = p.groundDrag
	}
	p.velocityX *= drag
	p.velocityY *= drag
	p.velocityZ *= drag

	const threshold = 0.005
	if absF(p.velocityX) < threshold {
		p.velocityX = 0
	}
	if absF(p.velocityY) < threshold {
		p.velocityY = 0
	}
	if absF(p.velocityZ) < threshold {
		p.velocityZ = 0
	}

	if p.velocityX != 0 || p.velocityY != 0 || p.velocityZ != 0 {
		newX := float64(p.client.Self.X) + p.velocityX
		newY := float64(p.client.Self.Y) + p.velocityY
		newZ := float64(p.client.Self.Z) + p.velocityZ

		// bounds checking to prevent protocol errors
		const maxCoord = 30000000.0
		const minY = -1024.0
		const maxY = 512.0

		if newX > maxCoord {
			newX = maxCoord
			p.velocityX = 0
		} else if newX < -maxCoord {
			newX = -maxCoord
			p.velocityX = 0
		}

		if newZ > maxCoord {
			newZ = maxCoord
			p.velocityZ = 0
		} else if newZ < -maxCoord {
			newZ = -maxCoord
			p.velocityZ = 0
		}

		if newY < minY {
			newY = minY
			p.velocityY = 0
			p.onGround = true // hit bottom
		} else if newY > maxY {
			newY = maxY
			p.velocityY = 0
		}

		// check for NaN or Infinity
		if math.IsNaN(newX) || math.IsInf(newX, 0) ||
			math.IsNaN(newY) || math.IsInf(newY, 0) ||
			math.IsNaN(newZ) || math.IsInf(newZ, 0) {

			// reset to last known good position
			newX = float64(p.client.Self.X)
			newY = float64(p.client.Self.Y)
			newZ = float64(p.client.Self.Z)
			p.velocityX = 0
			p.velocityY = 0
			p.velocityZ = 0
			return
		}

		p.client.Self.X = ns.Double(newX)
		p.client.Self.Y = ns.Double(newY)
		p.client.Self.Z = ns.Double(newZ)

		move, err := packets.C2SMovePlayerPos.WithData(packets.C2SMovePlayerPosData{
			X:     ns.Double(newX),
			FeetY: ns.Double(newY),
			Z:     ns.Double(newZ),
			Flags: boolToFlag(p.onGround),
		})
		if err == nil {
			p.client.OutgoingPacketQueue <- move
		}
	}
}

func (p *PhysicsController) HandlePacket(c *Client, pkt *jp.Packet) {
	switch pkt.PacketID {
	case packets.S2CPlayerPosition.PacketID:
		var d packets.S2CPlayerPositionData
		if err := jp.BytesToPacketData(pkt.Data, &d); err != nil {
			return
		}

		isRelative := func(flag ns.TeleportFlags, mask ns.TeleportFlags) bool {
			return (flag & mask) != 0
		}

		newX := float64(d.X)
		newY := float64(d.Y)
		newZ := float64(d.Z)

		if isRelative(d.Flags, 0x01) {
			newX += float64(c.Self.X)
		}
		if isRelative(d.Flags, 0x02) {
			newY += float64(c.Self.Y)
		}
		if isRelative(d.Flags, 0x04) {
			newZ += float64(c.Self.Z)
		}

		c.Self.X = ns.Double(newX)
		c.Self.Y = ns.Double(newY)
		c.Self.Z = ns.Double(newZ)

		p.mu.Lock()
		p.velocityX = float64(d.VelocityX)
		p.velocityY = float64(d.VelocityY)
		p.velocityZ = float64(d.VelocityZ)

		p.mu.Unlock()

		confirm, err := packets.C2SAcceptTeleportation.WithData(packets.C2SAcceptTeleportationData{
			TeleportId: d.TeleportId,
		})
		if err == nil {
			c.OutgoingPacketQueue <- confirm
		}
	// knockback on damage
	case packets.S2CHurtAnimation.PacketID:
		var d packets.S2CHurtAnimationData
		if err := jp.BytesToPacketData(pkt.Data, &d); err != nil {
			return
		}

		angle := float64(d.Yaw) * (math.Pi / 180) // direction the damage is coming FROM
		horizontalStrength := p.knockbackHorizontal
		verticalStrength := p.knockbackVertical

		if !p.IsOnGround() {
			horizontalStrength *= p.knockbackAirMultiplier
			verticalStrength *= 0.95
		}

		resistanceMultiplier := 1.0 - p.knockbackResistance
		horizontalStrength *= resistanceMultiplier
		verticalStrength *= resistanceMultiplier

		dx := -math.Sin(angle) * horizontalStrength
		dz := -math.Cos(angle) * horizontalStrength
		dy := verticalStrength

		p.AddVelocity(dx, dy, dz)
	}
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func boolToFlag(onGround bool) ns.Byte {
	if onGround {
		return 0x01
	}
	return 0x00
}
