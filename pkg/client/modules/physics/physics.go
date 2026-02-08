package physics

import (
	"context"
	"math"
	"time"

	"github.com/go-mclib/client/pkg/client"
	"github.com/go-mclib/client/pkg/client/modules/collisions"
	"github.com/go-mclib/client/pkg/client/modules/entities"
	"github.com/go-mclib/client/pkg/client/modules/self"
	"github.com/go-mclib/client/pkg/client/modules/world"
	"github.com/go-mclib/data/pkg/data/packet_ids"
	"github.com/go-mclib/data/pkg/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

const ModuleName = "physics"

type Module struct {
	client *client.Client

	// velocity (delta movement)
	VelX, VelY, VelZ float64

	// state
	OnGround            bool
	HorizontalCollision bool
	Sprinting           bool

	// input
	ForwardImpulse float64 // -1.0 to 1.0
	StrafeImpulse  float64 // -1.0 to 1.0
	Jumping        bool
	Sneaking       bool

	// position packet tracking (LocalPlayer.sendPosition)
	lastSentX, lastSentY, lastSentZ float64
	lastSentYaw, lastSentPitch      float32
	lastSentOnGround                bool
	positionReminder                int

	cancel context.CancelFunc

	// damage tracking for knockback filtering
	hasPendingDamage      bool
	lastDamageEntityCause bool // true if the last damage had an entity source

	onTick []func()
}

func New() *Module { return &Module{} }

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c

	// start tick loop when player spawns
	s := self.From(c)
	if s != nil {
		s.OnSpawn(func() {
			m.startTickLoop()
		})
	}
}

func (m *Module) Reset() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.VelX = 0
	m.VelY = 0
	m.VelZ = 0
	m.OnGround = false
	m.HorizontalCollision = false
	m.Sprinting = false
	m.ForwardImpulse = 0
	m.StrafeImpulse = 0
	m.Jumping = false
	m.Sneaking = false
	m.positionReminder = 0
}

func From(c *client.Client) *Module {
	mod := c.Module(ModuleName)
	if mod == nil {
		return nil
	}
	return mod.(*Module)
}

// events

func (m *Module) OnTick(cb func()) { m.onTick = append(m.onTick, cb) }

// actions

func (m *Module) SetSprinting(sprinting bool) { m.Sprinting = sprinting }

func (m *Module) SetInput(forward, strafe float64, jumping, sneaking bool) {
	m.ForwardImpulse = forward
	m.StrafeImpulse = strafe
	m.Jumping = jumping
	m.Sneaking = sneaking
}

// HandlePacket handles velocity-related packets for the player's own entity.
func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	switch pkt.PacketID {
	case packet_ids.S2CDamageEventID:
		m.handleDamageEvent(pkt)
	case packet_ids.S2CSetEntityMotionID:
		m.handleEntityMotion(pkt)
	case packet_ids.S2CPlayerPositionID:
		m.handleTeleport(pkt)
	}
}

func (m *Module) handleDamageEvent(pkt *jp.WirePacket) {
	var d packets.S2CDamageEvent
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	s := self.From(m.client)
	if s == nil || int32(d.EntityId) != int32(s.EntityID) {
		return
	}

	// SourceCauseId and SourceDirectId are entity_id + 1, or 0 if no entity
	m.hasPendingDamage = true
	m.lastDamageEntityCause = int32(d.SourceCauseId) > 0 || int32(d.SourceDirectId) > 0
}

func (m *Module) handleEntityMotion(pkt *jp.WirePacket) {
	var d packets.S2CSetEntityMotion
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	s := self.From(m.client)
	if s == nil || int32(d.EntityId) != int32(s.EntityID) {
		return
	}

	// if preceded by a damage event, only apply velocity for entity-caused damage
	if m.hasPendingDamage {
		m.hasPendingDamage = false
		if !m.lastDamageEntityCause {
			return // environmental damage — ignore knockback
		}
	}

	m.VelX = d.Velocity.X
	m.VelY = d.Velocity.Y
	m.VelZ = d.Velocity.Z
}

func (m *Module) handleTeleport(pkt *jp.WirePacket) {
	var d packets.S2CPlayerPosition
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	flags := int32(d.Flags)

	if flags&0x20 != 0 {
		m.VelX += float64(d.VelocityX)
	} else {
		m.VelX = float64(d.VelocityX)
	}
	if flags&0x40 != 0 {
		m.VelY += float64(d.VelocityY)
	} else {
		m.VelY = float64(d.VelocityY)
	}
	if flags&0x80 != 0 {
		m.VelZ += float64(d.VelocityZ)
	} else {
		m.VelZ = float64(d.VelocityZ)
	}
}

func (m *Module) startTickLoop() {
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	s := self.From(m.client)
	if s == nil {
		return
	}

	// initialize last sent position
	m.lastSentX = float64(s.X)
	m.lastSentY = float64(s.Y)
	m.lastSentZ = float64(s.Z)
	m.lastSentYaw = float32(s.Yaw)
	m.lastSentPitch = float32(s.Pitch)
	m.lastSentOnGround = m.OnGround
	m.positionReminder = 0

	go func() {
		ticker := time.NewTicker(TickDuration)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.tick()
			}
		}
	}()
}

func (m *Module) tick() {
	s := self.From(m.client)
	w := world.From(m.client)
	col := collisions.From(m.client)
	if s == nil || w == nil || col == nil {
		return
	}

	x := float64(s.X)
	y := float64(s.Y)
	z := float64(s.Z)
	yaw := float64(s.Yaw)

	// jump
	if m.Jumping && m.OnGround {
		m.jump(yaw)
	}

	// apply fluid flow pushing (Entity.baseTick in vanilla, before travel)
	m.applyFluidPushing(x, y, z, w)

	// determine environment
	feetBlock := w.GetBlock(int(math.Floor(x)), int(math.Floor(y)), int(math.Floor(z)))
	inWater := IsWater(feetBlock)
	inLava := IsLava(feetBlock)

	// pre-collision: apply movement input to velocity
	// vanilla order: moveRelative → move/collide → gravity + friction
	var blockFriction float64
	if inWater {
		m.applyWaterInput(yaw)
	} else if inLava {
		m.applyLavaInput(yaw)
	} else {
		blockFriction = m.applyAirInput(x, y, z, yaw, w)
	}

	// resolve collisions (this.move in vanilla)
	origVelY := m.VelY
	adjX, adjY, adjZ, hCol, vCol := col.CollideMovement(x, y, z, PlayerWidth, PlayerHeight, m.VelX, m.VelY, m.VelZ)

	m.HorizontalCollision = hCol
	if vCol {
		m.VelY = 0
	}
	if hCol {
		if m.VelX != adjX {
			m.VelX = 0
		}
		if m.VelZ != adjZ {
			m.VelZ = 0
		}
	}

	// update position
	newX := x + adjX
	newY := y + adjY
	newZ := z + adjZ
	s.X = ns.Float64(newX)
	s.Y = ns.Float64(newY)
	s.Z = ns.Float64(newZ)

	m.OnGround = vCol && origVelY < 0

	// post-collision: apply gravity and friction (after move, matching vanilla)
	if inWater {
		m.applyWaterPhysics()
	} else if inLava {
		m.applyLavaPhysics()
	} else {
		m.applyAirPhysics(blockFriction)
	}

	// entity pushing
	m.applyEntityPushing(newX, newY, newZ)

	// send position
	m.sendPosition(s)

	// fire tick callbacks
	for _, cb := range m.onTick {
		cb()
	}
}

// applyAirInput adds movement input to velocity (pre-collision).
// Returns the block friction for use in post-collision physics.
func (m *Module) applyAirInput(x, y, z, yaw float64, w *world.Module) float64 {
	belowBlock := w.GetBlock(int(math.Floor(x)), int(math.Floor(y-0.5)), int(math.Floor(z)))
	var blockFriction float64
	if m.OnGround {
		blockFriction = GetBlockFriction(belowBlock)
	} else {
		blockFriction = 1.0
	}
	friction := blockFriction * AirFrictionMul

	var speed float64
	if m.OnGround {
		baseSpeed := PlayerSpeed
		if m.Sprinting {
			baseSpeed *= (1.0 + SprintModifier)
		}
		speed = baseSpeed * (FrictionSpeedFactor / (friction * friction * friction))
		speed *= GetBlockSpeedFactor(belowBlock)
	} else {
		speed = FlyingSpeed
	}

	dx, _, dz := moveRelative(speed, m.ForwardImpulse, m.StrafeImpulse, yaw)
	m.VelX += dx
	m.VelZ += dz

	return blockFriction
}

// applyAirPhysics applies gravity and friction after collision (post-move).
func (m *Module) applyAirPhysics(blockFriction float64) {
	friction := blockFriction * AirFrictionMul
	m.VelY -= Gravity
	m.VelX *= friction
	m.VelZ *= friction
	m.VelY *= VerticalAirFriction
}

// applyWaterInput adds movement input to velocity in water (pre-collision).
func (m *Module) applyWaterInput(yaw float64) {
	dx, _, dz := moveRelative(WaterAcceleration, m.ForwardImpulse, m.StrafeImpulse, yaw)
	m.VelX += dx
	m.VelZ += dz
}

// applyWaterPhysics applies water drag and gravity after collision (post-move).
func (m *Module) applyWaterPhysics() {
	slowDown := WaterSlowdown
	if m.Sprinting {
		slowDown = WaterSprintSlowdown
	}
	m.VelX *= slowDown
	m.VelY *= WaterVerticalDrag
	m.VelZ *= slowDown
	m.VelY -= Gravity
}

// applyLavaInput adds movement input to velocity in lava (pre-collision).
func (m *Module) applyLavaInput(yaw float64) {
	dx, _, dz := moveRelative(WaterAcceleration, m.ForwardImpulse, m.StrafeImpulse, yaw)
	m.VelX += dx
	m.VelZ += dz
}

// applyLavaPhysics applies lava drag and gravity after collision (post-move).
func (m *Module) applyLavaPhysics() {
	m.VelX *= LavaSlowdown
	m.VelY *= LavaVerticalDrag
	m.VelZ *= LavaSlowdown
	m.VelY -= Gravity * LavaGravityFactor
}

// jump applies jump velocity (LivingEntity.jumpFromGround)
func (m *Module) jump(yaw float64) {
	m.VelY = JumpPower
	if m.Sprinting {
		angle := yaw * math.Pi / 180.0
		m.VelX += -math.Sin(angle) * SprintJumpBoost
		m.VelZ += math.Cos(angle) * SprintJumpBoost
	}
	m.OnGround = false
}

// moveRelative computes input vector rotated by yaw (Entity.getInputVector)
func moveRelative(speed, forward, strafe, yaw float64) (dx, dy, dz float64) {
	lengthSq := forward*forward + strafe*strafe
	if lengthSq < 1e-7 {
		return 0, 0, 0
	}
	if lengthSq > 1 {
		invLen := 1.0 / math.Sqrt(lengthSq)
		forward *= invLen
		strafe *= invLen
	}
	forward *= speed
	strafe *= speed
	sinYaw := math.Sin(yaw * math.Pi / 180.0)
	cosYaw := math.Cos(yaw * math.Pi / 180.0)
	dx = strafe*cosYaw - forward*sinYaw
	dz = forward*cosYaw + strafe*sinYaw
	return dx, 0, dz
}

// applyEntityPushing applies pushing forces from nearby entities (Entity.push).
// Vanilla: LivingEntity.pushEntities → getPushableEntities (AABB intersection) → Entity.push
func (m *Module) applyEntityPushing(x, y, z float64) {
	ents := entities.From(m.client)
	if ents == nil {
		return
	}

	// find entities whose AABB intersects with the player's AABB
	hw := PlayerWidth / 2
	overlapping := ents.GetEntitiesInAABB(
		x-hw, y, z-hw,
		x+hw, y+PlayerHeight, z+hw,
	)
	for _, e := range overlapping {
		dx := e.X - x
		dz := e.Z - z
		dist := math.Max(math.Abs(dx), math.Abs(dz))
		if dist < EntityPushMinDist {
			continue
		}
		dist = math.Sqrt(dist)
		dx /= dist
		dz /= dist
		pow := math.Min(1.0, 1.0/dist)
		push := pow * EntityPushStrength
		m.VelX -= dx * push
		m.VelZ -= dz * push
	}
}

// sendPosition sends position/rotation packets following MC's LocalPlayer.sendPosition logic
func (m *Module) sendPosition(s *self.Module) {
	m.positionReminder++

	x := float64(s.X)
	y := float64(s.Y)
	z := float64(s.Z)
	yaw := float32(s.Yaw)
	pitch := float32(s.Pitch)

	dx := x - m.lastSentX
	dy := y - m.lastSentY
	dz := z - m.lastSentZ
	moved := (dx*dx+dy*dy+dz*dz) > PositionThresholdSq || m.positionReminder >= PositionReminderMax
	rotated := yaw != m.lastSentYaw || pitch != m.lastSentPitch

	var flags ns.Int8
	if m.OnGround {
		flags = 0x01
	}
	if m.HorizontalCollision {
		flags |= 0x02
	}

	if moved && rotated {
		m.client.SendPacket(&packets.C2SMovePlayerPosRot{
			X: ns.Float64(x), FeetY: ns.Float64(y), Z: ns.Float64(z),
			Yaw: ns.Float32(yaw), Pitch: ns.Float32(pitch),
			Flags: flags,
		})
	} else if moved {
		m.client.SendPacket(&packets.C2SMovePlayerPos{
			X: ns.Float64(x), FeetY: ns.Float64(y), Z: ns.Float64(z),
			Flags: flags,
		})
	} else if rotated {
		m.client.SendPacket(&packets.C2SMovePlayerRot{
			Yaw: ns.Float32(yaw), Pitch: ns.Float32(pitch),
			Flags: flags,
		})
	} else if m.OnGround != m.lastSentOnGround {
		m.client.SendPacket(&packets.C2SMovePlayerStatusOnly{
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
	m.lastSentOnGround = m.OnGround
}
