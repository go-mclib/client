package physics

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/entities"
	"github.com/go-mclib/client/modules/inventory"
	"github.com/go-mclib/client/modules/registry"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/client/modules/world"
	"github.com/go-mclib/data/packet_ids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

const ModuleName = "physics"

type Module struct {
	client *client.Client
	mu     sync.RWMutex
	tickMu sync.Mutex // serializes old/new tick loops across lifecycle transitions

	// deps (self/world/collisions required, entities optional)
	self       *self.Module
	world      *world.Module
	collisions *collisions.Module
	entities   *entities.Module
	inventory  *inventory.Module
	registry   *registry.Module

	// velocity (delta movement)
	velX, velY, velZ float64

	// state
	onGround                 bool
	horizontalCollision      bool
	xCollision               bool
	zCollision               bool
	minorHorizontalCollision bool
	fluid                    fluidState
	blocks                   blockEffects
	fallDistance             float64
	noJumpDelay              int
	poseHeight               float64
	swimming                 bool

	// input
	forwardImpulse float64 // -1.0 to 1.0
	strafeImpulse  float64 // -1.0 to 1.0
	jumping        bool

	// position packet tracking (LocalPlayer.sendPosition)
	lastSentX, lastSentY, lastSentZ float64
	lastSentYaw, lastSentPitch      float32
	lastSentOnGround                bool
	lastSentHorizontalCollision     bool
	lastSentSprinting               bool
	lastSentInputFlags              uint8
	positionReminder                int

	cancel        context.CancelFunc
	tickContext   context.Context
	outgoing      chan jp.Packet
	positionKnown bool
	vehicleID     int32

	onTick []func()
}

func New() *Module { return &Module{} }

func (m *Module) Name() string { return ModuleName }

func (m *Module) Init(c *client.Client) {
	m.client = c
	c.OnTransfer(m.Reset)
	c.OnDisconnect(m.Reset)
}

func (m *Module) Wire() error {
	m.self = self.From(m.client)
	m.world = world.From(m.client)
	m.collisions = collisions.From(m.client)
	if m.self == nil || m.world == nil || m.collisions == nil {
		return fmt.Errorf("requires self, world and collisions modules")
	}
	m.entities = entities.From(m.client) // optional: enables entity pushing
	m.inventory = inventory.From(m.client)
	m.registry = registry.From(m.client)

	s := m.self
	// start tick loop when player spawns
	s.OnSpawn(func() {
		m.startTickLoop()
	})
	s.OnRespawn(func() {
		m.Reset()
		m.startTickLoop()
	})
	s.OnDeath(m.Reset)
	s.OnTeleport(m.handleTeleport)

	// sync last-sent tracking after server teleport so sendPosition
	// doesn't re-send a flying packet for the same position
	s.OnPosition(func(x, y, z float64) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.positionKnown = true
		m.lastSentX = x
		m.lastSentY = y
		m.lastSentZ = z
		yaw, pitch := s.Rotation()
		m.lastSentYaw = yaw
		m.lastSentPitch = pitch
		m.lastSentOnGround = m.onGround
		m.positionReminder = 0
	})
	return nil
}

func (m *Module) Reset() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Unlock()
	// The loop never blocks on packet writes, so an old tick can finish without
	// needing the packet/lifecycle loop that is waiting here.
	m.tickMu.Lock()
	defer m.tickMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancel = nil
	m.tickContext = nil
	m.outgoing = nil
	m.velX, m.velY, m.velZ = 0, 0, 0
	m.onGround = false
	m.horizontalCollision = false
	m.xCollision, m.zCollision = false, false
	m.forwardImpulse, m.strafeImpulse, m.jumping = 0, 0, false
	m.positionReminder = 0
	m.positionKnown = false
	m.vehicleID = 0
	m.fluid, m.blocks = fluidState{}, blockEffects{}
	m.fallDistance, m.poseHeight = 0, 0
	m.noJumpDelay = 0
	m.swimming, m.minorHorizontalCollision = false, false
}

// QueuePacket submits an action to this physics session without blocking the
// tick or packet loop. Failure cancels the loop rather than retaining stale input.
func (m *Module) QueuePacket(pkt jp.Packet) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sendPacket(pkt)
}

// QueuePacketContext additionally cancels an action packet that is still
// waiting in the outgoing queue. Bytes already written cannot be recalled.
func (m *Module) QueuePacketContext(ctx context.Context, pkt jp.Packet) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return m.QueuePacket(contextPacket{Packet: pkt, ctx: ctx})
}

type contextPacket struct {
	jp.Packet
	ctx context.Context
}

func (p contextPacket) Write(buf *ns.PacketBuffer) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	return p.Packet.Write(buf)
}

// sendPacket requires mu. The captured queue cannot cross a reconnect.
func (m *Module) sendPacket(pkt jp.Packet) error {
	if m.tickContext == nil || m.tickContext.Err() != nil {
		return fmt.Errorf("physics session is inactive")
	}
	select {
	case m.outgoing <- contextPacket{Packet: pkt, ctx: m.tickContext}:
		return nil
	default:
		m.cancel()
		return fmt.Errorf("outgoing packet queue is full; physics stopped")
	}
}

// Available reports whether loaded-position physics can accept action input.
func (m *Module) Available() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.tickContext != nil && m.tickContext.Err() == nil && m.positionKnown && !m.self.SuppressPositionEcho()
}

func From(c *client.Client) *Module {
	mod := c.Module(ModuleName)
	if mod == nil {
		return nil
	}
	return mod.(*Module)
}

// Velocity returns the current velocity (delta movement).
func (m *Module) Velocity() (x, y, z float64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.velX, m.velY, m.velZ
}

// IsOnGround returns whether the player is on the ground.
func (m *Module) IsOnGround() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.onGround
}

// HasHorizontalCollision returns whether the player has a horizontal collision.
func (m *Module) HasHorizontalCollision() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.horizontalCollision
}

// CollisionAxes returns which axes have horizontal collisions.
func (m *Module) CollisionAxes() (x, z bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.xCollision, m.zCollision
}

// Input returns the current movement input state.
func (m *Module) Input() (forward, strafe float64, jumping bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.forwardImpulse, m.strafeImpulse, m.jumping
}

func (m *Module) OnTick(cb func()) { m.onTick = append(m.onTick, cb) }

func (m *Module) SetInput(forward, strafe float64, jumping bool) {
	m.mu.Lock()
	m.forwardImpulse = forward
	m.strafeImpulse = strafe
	m.jumping = jumping
	m.mu.Unlock()
}

// HandlePacket handles velocity-related packets for the player's own entity.
func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	if m.client.State() != jp.StatePlay {
		return
	}
	switch pkt.PacketID {
	case packet_ids.S2CSetEntityMotionID:
		m.handleEntityMotion(pkt)
	case packet_ids.S2CSetPassengersID:
		var d packets.S2CSetPassengers
		if pkt.ReadInto(&d) != nil {
			return
		}
		m.mu.Lock()
		previousVehicle := m.vehicleID
		if m.vehicleID == int32(d.EntityId) {
			m.vehicleID = 0
		}
		for _, id := range d.Passengers {
			if int32(id) == m.self.EntityID() {
				m.vehicleID = int32(d.EntityId)
			}
		}
		if previousVehicle != 0 && m.vehicleID == 0 {
			// A vehicle can have moved far from our last walking position.
			// Wait for the server's dismount position before echoing movement.
			m.positionKnown = false
		}
		m.mu.Unlock()
	}
}

func (m *Module) handleEntityMotion(pkt *jp.WirePacket) {
	var d packets.S2CSetEntityMotion
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	if int32(d.EntityId) != m.self.EntityID() {
		return
	}

	m.mu.Lock()
	m.velX = d.Velocity.X
	m.velY = d.Velocity.Y
	m.velZ = d.Velocity.Z
	m.mu.Unlock()
}

func (m *Module) handleTeleport(d *packets.S2CPlayerPosition, oldYaw, oldPitch float32) {
	flags := int32(d.Flags)

	m.mu.Lock()
	if m.vehicleID != 0 {
		m.mu.Unlock()
		return
	}
	if flags&0x100 != 0 {
		yaw, pitch := m.self.Rotation()
		sinPitch, cosPitch := sinCos(float64(float32(float64(oldPitch-pitch) * (math.Pi / 180))))
		m.velY, m.velZ = m.velY*cosPitch+m.velZ*sinPitch, m.velZ*cosPitch-m.velY*sinPitch
		sinYaw, cosYaw := sinCos(float64(float32(float64(oldYaw-yaw) * (math.Pi / 180))))
		m.velX, m.velZ = m.velX*cosYaw+m.velZ*sinYaw, m.velZ*cosYaw-m.velX*sinYaw
	}
	if flags&0x20 != 0 {
		m.velX += float64(d.VelocityX)
	} else {
		m.velX = float64(d.VelocityX)
	}
	if flags&0x40 != 0 {
		m.velY += float64(d.VelocityY)
	} else {
		m.velY = float64(d.VelocityY)
	}
	if flags&0x80 != 0 {
		m.velZ += float64(d.VelocityZ)
	} else {
		m.velZ = float64(d.VelocityZ)
	}
	m.mu.Unlock()
}

func (m *Module) startTickLoop() {
	m.Reset()
	m.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel, m.tickContext = cancel, ctx
	m.outgoing = m.client.OutgoingPacketQueue
	m.lastSentInputFlags = 0
	m.lastSentSprinting = false
	m.mu.Unlock()
	go func() {
		ticker := time.NewTicker(tickDuration)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.client.WithPacketState(func() {
					m.tickMu.Lock()
					defer m.tickMu.Unlock()
					if ctx.Err() == nil {
						m.tick()
					}
				})
			}
		}
	}()
}

func (m *Module) tick() {
	m.mu.RLock()
	ctx := m.tickContext
	m.mu.RUnlock()
	if ctx == nil || ctx.Err() != nil {
		return
	}
	s, w, col := m.self, m.world, m.collisions
	if s.SuppressPositionEcho() {
		return
	}
	m.tickEffectsAndCallbacks(s)
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil || !m.positionKnown {
		return
	}
	if m.vehicleID != 0 {
		m.velX, m.velY, m.velZ = 0, 0, 0
		m.noJumpDelay = 0
		if m.entities != nil {
			m.entities.TickPhysics()
		}
		col.TickBorder()
		m.sendInput(s)
		m.sendPacket(&packets.C2SClientTickEnd{})
		return
	}
	x, y, z := s.Position()
	// Unknown chunks are not empty terrain. Do not predict through an unloaded
	// boundary while the server is still streaming the initial world.
	halfWidth := m.bodyWidth() / 2
	for _, bx := range [2]int{int(math.Floor(x - halfWidth - 1)), int(math.Floor(x + halfWidth + 1))} {
		for _, bz := range [2]int{int(math.Floor(z - halfWidth - 1)), int(math.Floor(z + halfWidth + 1))} {
			if !w.IsChunkLoaded(int32(bx>>4), int32(bz>>4)) {
				col.TickBorder()
				m.sendInput(s)
				m.sendPacket(&packets.C2SClientTickEnd{})
				return
			}
		}
	}
	yaw, pitch := s.Rotation()
	flying := s.AbilityFlags()&2 != 0
	if s.Gamemode() == 3 {
		m.onGround = false
	}
	col.SetPlayerContext(s.Sneaking(), m.fallDistance, m.canWalkOnPowderSnow())
	previousEyeInWater := m.fluid.eyeInWater
	m.fluid.eyeHeight, m.fluid.fastLava = m.eyeHeight(), m.fastLava()
	playerHeight := m.bodyHeight()
	m.updateFluidState(x, y, z, playerHeight)
	m.updateSwimming(previousEyeInWater, x, y, z)
	forward, strafe, _ := m.processInput(s, x, y, z, previousEyeInWater)
	if s.Gamemode() != 3 {
		m.nudgeOutOfCorners(col, x, y, z, playerHeight)
	}
	if m.fluid.waterHeight > 0 {
		m.fallDistance = 0
		if s.Sneaking() && !flying {
			m.velY -= float64(float32(0.04))
		}
	}
	if flying {
		verticalInput := float32(0)
		if m.jumping {
			verticalInput++
		}
		if s.Sneaking() {
			verticalInput--
		}
		m.velY += float64(verticalInput * s.FlyingSpeed() * 3)
	}
	m.zeroSmallVelocity()
	if !s.Sleeping() {
		m.applyJumpInput(float64(yaw))
	}
	if s.HasEffect(effectSlowFalling) || s.HasEffect(effectLevitation) {
		m.fallDistance = 0
	}
	inWater, inLava := !flying && m.fluid.waterHeight > 0, !flying && m.fluid.lavaHeight > 0
	m.applySwimSteering(x, y, z, pitch)
	flyingY := m.velY
	gliding := !inWater && !inLava && s.FallFlying() && !m.onClimbableAt(x, y, z)
	blockFriction := 1.0
	if gliding {
		m.applyGliding(yaw, pitch)
	} else {
		blockFriction = m.applyMovementInput(s, w, x, y, z, float64(yaw), forward, strafe, inWater, inLava)
	}
	newX, newY, newZ := m.collideAndMove(s, col, x, y, z, playerHeight, forward, strafe, float64(yaw))
	m.applyPostMovePhysics(s, w, newX, newY, newZ, playerHeight, y, inWater, inLava, gliding, blockFriction)
	if flying {
		m.velY = flyingY * 0.6
	}
	if s.Gamemode() != 3 {
		m.applyInsideBlockEffects(s, x, y, z, newX, newY, newZ, playerHeight)
	}
	m.updatePose(newX, newY, newZ)
	// The local player is inserted first. Remote interpolation/pushing follows
	// local travel, and the resulting impulse belongs to the next movement tick.
	if m.entities != nil {
		m.entities.TickPhysics()
	}
	m.applyEntityPushing(newX, newY, newZ, m.bodyHeight())
	col.TickBorder()
	m.sendTickPackets(s)
}

// tickEffectsAndCallbacks ticks effect durations and fires the registered tick callbacks.
func (m *Module) tickEffectsAndCallbacks(s *self.Module) {
	// tick effect durations (vanilla: LivingEntity.tickEffects before aiStep)
	s.TickEffects()

	// fire tick callbacks FIRST so navigation can set input for this tick
	// (matches vanilla: applyInput runs before travel)
	for _, cb := range m.onTick {
		cb()
	}
}

// processInput applies vanilla input processing and resolves the effective player height.
func (m *Module) processInput(s *self.Module, x, y, z float64, previousEyeInWater bool) (forwardImpulse, strafeImpulse, playerHeight float64) {
	playerHeight = m.bodyHeight()
	if s.Sleeping() {
		return 0, 0, playerHeight
	}
	width, scale := m.bodyWidth(), m.scale()
	crouching := s.AbilityFlags()&2 == 0 && !m.swimming &&
		m.collisions.CanFitAt(x, y, z, width, float64(float32(collisions.PlayerSneakingHeight)*scale)) &&
		(s.Sneaking() || !m.collisions.CanFitAt(x, y, z, width, float64(float32(collisions.PlayerHeight)*scale)))
	m.updateSprinting(crouching, previousEyeInWater)
	slowFactor := 1.0
	if crouching || m.poseHeight == collisions.PlayerWidth && !m.swimming {
		slowFactor = s.AttributeValue("minecraft:sneaking_speed", sneakingSpeedFactor)
	}
	itemFactor, _ := m.itemUseEffects()
	forwardImpulse, strafeImpulse = ProcessInput(m.forwardImpulse, m.strafeImpulse, itemFactor, slowFactor)
	return
}

// zeroSmallVelocity applies movement threshold zeroing (LivingEntity.aiStep lines 2917-2940):
// for players, horizontal velocity is zeroed if magnitude² < 9e-6.
func (m *Module) zeroSmallVelocity() {
	if m.velX*m.velX+m.velZ*m.velZ < 9.0e-6 {
		m.velX = 0
		m.velZ = 0
	}
	if math.Abs(m.velY) < 0.003 {
		m.velY = 0
	}
}

// nudgeOutOfCorners nudges out of any block corner the body is penetrating, matching
// LocalPlayer.moveTowardsClosestSpace (runs for the 4 body corners each tick
// before travel). Without it the player snags on block corners/walls.
func (m *Module) nudgeOutOfCorners(col *collisions.Module, x, y, z, playerHeight float64) {
	cornerInset := m.bodyWidth() * 0.35
	m.moveTowardsClosestSpace(col, x-cornerInset, z+cornerInset, y, y+playerHeight)
	m.moveTowardsClosestSpace(col, x-cornerInset, z-cornerInset, y, y+playerHeight)
	m.moveTowardsClosestSpace(col, x+cornerInset, z-cornerInset, y, y+playerHeight)
	m.moveTowardsClosestSpace(col, x+cornerInset, z+cornerInset, y, y+playerHeight)
}

// applyMovementInput applies movement input to velocity (pre-collision).
// vanilla order: moveRelative → move/collide → gravity + friction.
// Returns the block friction for post-collision air physics.
func (m *Module) applyMovementInput(s *self.Module, w *world.Module, x, y, z, yaw, forwardImpulse, strafeImpulse float64, inWater, inLava bool) (blockFriction float64) {
	if inWater {
		m.applyWaterInputScaled(s, yaw, forwardImpulse, strafeImpulse)
	} else if inLava {
		m.applyLavaInputScaled(s, yaw, forwardImpulse, strafeImpulse)
	} else {
		blockFriction = m.applyAirInputScaled(s, x, y, z, yaw, w, forwardImpulse, strafeImpulse)
	}
	return blockFriction
}

// collideAndMove resolves collisions, updates collision flags, position and onGround.
func (m *Module) collideAndMove(s *self.Module, col *collisions.Module, x, y, z, playerHeight, forward, strafe, yaw float64) (newX, newY, newZ float64) {
	if s.Gamemode() == 3 {
		m.horizontalCollision, m.minorHorizontalCollision = false, false
		m.xCollision, m.zCollision = false, false
		newX, newY, newZ = x+m.velX, y+m.velY, z+m.velZ
		s.SetPosition(newX, newY, newZ)
		return
	}
	dx, dy, dz := m.consumeStuckMovement(m.velX, m.velY, m.velZ)
	maxStep := float64(float32(s.AttributeValue("minecraft:step_height", collisions.StepUpHeight)))
	dx, dz = m.backOffFromEdge(x, y, z, dx, dy, dz, maxStep)
	m.blocks.originalX, m.blocks.originalZ = dx, dz
	adjX, adjY, adjZ, _, vCol := col.CollideMovement(x, y, z, m.bodyWidth(), playerHeight, dx, dy, dz, m.onGround, maxStep)
	m.xCollision, m.zCollision = notEqual(dx, adjX), notEqual(dz, adjZ)
	m.horizontalCollision = m.xCollision || m.zCollision
	m.minorHorizontalCollision = m.horizontalCollision && minorCollision(adjX, adjZ, forward, strafe, yaw)
	m.onGround = vCol && dy < 0
	newX, newY, newZ = x, y, z
	movementSquared := adjX*adjX + adjY*adjY + adjZ*adjZ
	if movementSquared > 1e-7 || dx*dx+dy*dy+dz*dz-movementSquared < 1e-7 {
		newX, newY, newZ = x+adjX, y+adjY, z+adjZ
		s.SetPosition(newX, newY, newZ)
	}
	if m.onGround {
		m.fallDistance = 0
	} else if adjY < 0 {
		m.fallDistance -= adjY
	}
	if dy != 0 && vCol || m.horizontalCollision {
		restitution := entityBounciness(s)
		if m.xCollision {
			m.velX = -m.velX * restitution
		}
		if m.zCollision {
			m.velZ = -m.velZ * restitution
		}
		if vCol {
			m.applyLandingEffects(s, newX, newY, newZ, m.velY, adjY)
		}
	}
	return
}

// applyPostMovePhysics applies block speed factor, then gravity and friction after the move.
func (m *Module) applyPostMovePhysics(s *self.Module, w *world.Module, newX, newY, newZ, height, startY float64, inWater, inLava, gliding bool, blockFriction float64) {
	if s.Gamemode() != 3 {
		factor := float32(getBlockSpeedFactorAt(w, newX, newY, newZ, m.bodyWidth()))
		efficiency := float32(s.AttributeValue("minecraft:movement_efficiency", 0))
		factor += efficiency * (1 - factor)
		m.velX *= float64(factor)
		m.velZ *= float64(factor)
	}
	if inWater {
		m.applyWaterPhysics(s, newX, newY, newZ, height, startY)
	} else if inLava {
		m.applyLavaPhysics(s, newX, newY, newZ, height, startY)
	} else if !gliding {
		m.applyAirPhysics(s, blockFriction)
	}
}
