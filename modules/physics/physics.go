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
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/client/modules/world"
	"github.com/go-mclib/data/packet_ids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
)

const ModuleName = "physics"

type Module struct {
	client *client.Client
	mu     sync.RWMutex

	// deps (self/world/collisions required, entities optional)
	self       *self.Module
	world      *world.Module
	collisions *collisions.Module
	entities   *entities.Module

	// velocity (delta movement)
	velX, velY, velZ float64

	// state
	onGround            bool
	horizontalCollision bool
	xCollision          bool
	zCollision          bool

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
	lastSentSneaking                bool
	lastSentInputFlags              uint8
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
	c.OnTransfer(m.Reset)
}

func (m *Module) Wire() error {
	m.self = self.From(m.client)
	m.world = world.From(m.client)
	m.collisions = collisions.From(m.client)
	if m.self == nil || m.world == nil || m.collisions == nil {
		return fmt.Errorf("requires self, world and collisions modules")
	}
	m.entities = entities.From(m.client) // optional: enables entity pushing

	s := m.self
	// start tick loop when player spawns
	s.OnSpawn(func() {
		m.startTickLoop()
	})

	// sync last-sent tracking after server teleport so sendPosition
	// doesn't re-send a flying packet for the same position
	s.OnPosition(func(x, y, z float64) {
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
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.mu.Lock()
	m.velX = 0
	m.velY = 0
	m.velZ = 0
	m.onGround = false
	m.horizontalCollision = false
	m.xCollision = false
	m.zCollision = false
	m.forwardImpulse = 0
	m.strafeImpulse = 0
	m.jumping = false
	m.mu.Unlock()
	m.positionReminder = 0
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

	if int32(d.EntityId) != m.self.EntityID() {
		return
	}

	// SourceCauseId and SourceDirectId are >1, or 0 if no entity (e.g. environmental damage)
	m.mu.Lock()
	m.hasPendingDamage = true
	m.lastDamageEntityCause = int32(d.SourceCauseId) > 0 || int32(d.SourceDirectId) > 0
	m.mu.Unlock()
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
	// if preceded by a damage event, only apply velocity for entity-caused damage
	if m.hasPendingDamage {
		m.hasPendingDamage = false
		if !m.lastDamageEntityCause {
			m.mu.Unlock()
			return // environmental damage — ignore knockback
		}
	}

	m.velX = d.Velocity.X
	m.velY = d.Velocity.Y
	m.velZ = d.Velocity.Z
	m.mu.Unlock()
}

func (m *Module) handleTeleport(pkt *jp.WirePacket) {
	var d packets.S2CPlayerPosition
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	flags := int32(d.Flags)

	m.mu.Lock()
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
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	s := m.self

	// initialize last sent position
	x, y, z := s.Position()
	yaw, pitch := s.Rotation()
	m.lastSentX = x
	m.lastSentY = y
	m.lastSentZ = z
	m.lastSentYaw = yaw
	m.lastSentPitch = pitch
	m.lastSentOnGround = m.onGround
	m.positionReminder = 0

	go func() {
		ticker := time.NewTicker(tickDuration)
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
	s := m.self
	w := m.world
	col := m.collisions

	// an external controller handles movement and position — skip physics
	if s.SuppressPositionEcho() {
		return
	}

	m.tickEffectsAndCallbacks(s)

	x, y, z := s.Position()
	yaw, _ := s.Rotation()

	// apply fluid flow pushing (Entity.baseTick in vanilla, before aiStep)
	m.applyFluidPushing(x, y, z, w)

	forwardImpulse, strafeImpulse, playerHeight := m.processInput(s)

	m.zeroSmallVelocity()
	m.nudgeOutOfCorners(col, x, y, z, playerHeight)

	// jump (after threshold zeroing, before travel)
	if m.jumping && m.onGround {
		m.jump(s, float64(yaw))
	}

	inWater, inLava := feetFluid(w, x, y, z)

	blockFriction := m.applyMovementInput(s, w, x, y, z, float64(yaw), forwardImpulse, strafeImpulse, inWater, inLava)

	newX, newY, newZ := m.collideAndMove(s, col, x, y, z, playerHeight)

	m.applyPostMovePhysics(s, w, newX, newY, newZ, inWater, inLava, blockFriction)

	// entity pushing
	m.applyEntityPushing(newX, newY, newZ, playerHeight)

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
func (m *Module) processInput(s *self.Module) (forwardImpulse, strafeImpulse, playerHeight float64) {
	// process inputs (LocalPlayer.modifyInput: 0.98 friction + sneaking + square normalization)
	forwardImpulse, strafeImpulse = modifyInput(m.forwardImpulse, m.strafeImpulse, s.Sneaking())

	// effective player height (1.5 when sneaking, 1.8 otherwise)
	playerHeight = collisions.PlayerHeight
	if s.Sneaking() {
		playerHeight = collisions.PlayerSneakingHeight
	}
	return forwardImpulse, strafeImpulse, playerHeight
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
	const cornerInset = collisions.PlayerWidth * 0.35
	m.moveTowardsClosestSpace(col, x-cornerInset, z+cornerInset, y, y+playerHeight)
	m.moveTowardsClosestSpace(col, x-cornerInset, z-cornerInset, y, y+playerHeight)
	m.moveTowardsClosestSpace(col, x+cornerInset, z-cornerInset, y, y+playerHeight)
	m.moveTowardsClosestSpace(col, x+cornerInset, z+cornerInset, y, y+playerHeight)
}

// feetFluid determines the fluid environment at the player's feet.
func feetFluid(w *world.Module, x, y, z float64) (inWater, inLava bool) {
	feetBlock := w.GetBlock(int(math.Floor(x)), int(math.Floor(y)), int(math.Floor(z)))
	return isWater(feetBlock), isLava(feetBlock)
}

// applyMovementInput applies movement input to velocity (pre-collision).
// vanilla order: moveRelative → move/collide → gravity + friction.
// Returns the block friction for post-collision air physics.
func (m *Module) applyMovementInput(s *self.Module, w *world.Module, x, y, z, yaw, forwardImpulse, strafeImpulse float64, inWater, inLava bool) (blockFriction float64) {
	if inWater {
		m.applyWaterInputScaled(yaw, forwardImpulse, strafeImpulse)
	} else if inLava {
		m.applyLavaInputScaled(yaw, forwardImpulse, strafeImpulse)
	} else {
		blockFriction = m.applyAirInputScaled(s, x, y, z, yaw, w, forwardImpulse, strafeImpulse)
	}
	return blockFriction
}

// collideAndMove resolves collisions, updates collision flags, position and onGround.
func (m *Module) collideAndMove(s *self.Module, col *collisions.Module, x, y, z, playerHeight float64) (newX, newY, newZ float64) {
	// resolve collisions (this.move in vanilla)
	origVelY := m.velY
	adjX, adjY, adjZ, _, vCol := col.CollideMovement(x, y, z, collisions.PlayerWidth, playerHeight, m.velX, m.velY, m.velZ)

	// horizontal collision detection with tolerance (vanilla Mth.equal: 1e-5)
	xCollided := notEqual(m.velX, adjX)
	zCollided := notEqual(m.velZ, adjZ)
	m.horizontalCollision = xCollided || zCollided
	m.xCollision = xCollided
	m.zCollision = zCollided
	if vCol {
		m.velY = 0
	}
	if xCollided {
		m.velX = 0
	}
	if zCollided {
		m.velZ = 0
	}

	// update position
	newX = x + adjX
	newY = y + adjY
	newZ = z + adjZ
	s.SetPosition(newX, newY, newZ)

	m.onGround = vCol && origVelY < 0
	return newX, newY, newZ
}

// applyPostMovePhysics applies block speed factor, then gravity and friction after the move.
func (m *Module) applyPostMovePhysics(s *self.Module, w *world.Module, newX, newY, newZ float64, inWater, inLava bool, blockFriction float64) {
	// block speed factor (Entity.move: applied after collision, before friction)
	if !inWater && !inLava {
		speedFactor := getBlockSpeedFactorAt(w, newX, newY, newZ)
		if speedFactor != 1.0 {
			m.velX *= speedFactor
			m.velZ *= speedFactor
		}
	}

	// post-collision: apply gravity and friction (after move, matching vanilla)
	if inWater {
		m.applyWaterPhysics(s)
	} else if inLava {
		m.applyLavaPhysics()
	} else {
		m.applyAirPhysics(s, blockFriction)
	}
}
