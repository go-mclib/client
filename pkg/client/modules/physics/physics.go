package physics

import (
	"context"
	"math"
	"sync"
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
	mu     sync.RWMutex

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

	s := self.From(c)
	if s != nil {
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
	}
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

// getters

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

// events

func (m *Module) OnTick(cb func()) { m.onTick = append(m.onTick, cb) }

// actions

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

	s := self.From(m.client)
	if s == nil || int32(d.EntityId) != s.EntityID() {
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

	s := self.From(m.client)
	if s == nil || int32(d.EntityId) != s.EntityID() {
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

	s := self.From(m.client)
	if s == nil {
		return
	}

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

	// an external controller handles movement and position — skip physics
	if s.SuppressPositionEcho() {
		return
	}

	// tick effect durations (vanilla: LivingEntity.tickEffects before aiStep)
	s.TickEffects()

	// fire tick callbacks FIRST so navigation can set input for this tick
	// (matches vanilla: applyInput runs before travel)
	for _, cb := range m.onTick {
		cb()
	}

	x, y, z := s.Position()
	yaw, _ := s.Rotation()

	// apply fluid flow pushing (Entity.baseTick in vanilla, before aiStep)
	m.applyFluidPushing(x, y, z, w)

	// process inputs (LocalPlayer.modifyInput: 0.98 friction + sneaking + square normalization)
	forwardImpulse, strafeImpulse := modifyInput(m.forwardImpulse, m.strafeImpulse, s.Sneaking())

	// effective player height (1.5 when sneaking, 1.8 otherwise)
	playerHeight := PlayerHeight
	if s.Sneaking() {
		playerHeight = PlayerSneakingHeight
	}

	// movement threshold zeroing (LivingEntity.aiStep lines 2917-2940)
	// for players: zero horizontal velocity if magnitude² < 9e-6
	if m.velX*m.velX+m.velZ*m.velZ < 9.0e-6 {
		m.velX = 0
		m.velZ = 0
	}
	if math.Abs(m.velY) < 0.003 {
		m.velY = 0
	}

	// jump (after threshold zeroing, before travel)
	if m.jumping && m.onGround {
		m.jump(s, float64(yaw))
	}

	// determine environment
	feetBlock := w.GetBlock(int(math.Floor(x)), int(math.Floor(y)), int(math.Floor(z)))
	inWater := IsWater(feetBlock)
	inLava := IsLava(feetBlock)

	// pre-collision: apply movement input to velocity
	// vanilla order: moveRelative → move/collide → gravity + friction
	var blockFriction float64
	if inWater {
		m.applyWaterInputScaled(float64(yaw), forwardImpulse, strafeImpulse)
	} else if inLava {
		m.applyLavaInputScaled(float64(yaw), forwardImpulse, strafeImpulse)
	} else {
		blockFriction = m.applyAirInputScaled(s, x, y, z, float64(yaw), w, forwardImpulse, strafeImpulse)
	}

	// resolve collisions (this.move in vanilla)
	origVelY := m.velY
	adjX, adjY, adjZ, _, vCol := col.CollideMovement(x, y, z, PlayerWidth, playerHeight, m.velX, m.velY, m.velZ)

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
	newX := x + adjX
	newY := y + adjY
	newZ := z + adjZ
	s.SetPosition(newX, newY, newZ)

	m.onGround = vCol && origVelY < 0

	// block speed factor (Entity.move: applied after collision, before friction)
	if !inWater && !inLava {
		speedFactor := GetBlockSpeedFactorAt(w, newX, newY, newZ)
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

	// entity pushing
	m.applyEntityPushing(newX, newY, newZ, playerHeight)

	// send input state (vanilla: LocalPlayer.tick sends C2SPlayerInput before sendPosition)
	m.sendInput(s)

	// send position (calls sendIsSprintingIfNeeded equivalent first, matching vanilla)
	m.sendPosition(s)

	// tick end (vanilla: Minecraft.tick sends ClientTickEnd after all tick logic)
	m.client.SendPacket(&packets.C2SClientTickEnd{})
}

// applyAirInputScaled adds movement input to velocity (pre-collision) with pre-scaled impulses.
// Returns the block friction for use in post-collision physics.
//
// Vanilla travelInAir passes raw blockFriction to getFrictionInfluencedSpeed (NOT blockFriction * 0.91).
// The 0.91 multiplier only applies to post-move velocity friction (applyAirPhysics).
func (m *Module) applyAirInputScaled(s *self.Module, x, y, z, yaw float64, w *world.Module, forward, strafe float64) float64 {
	belowBlock := w.GetBlock(int(math.Floor(x)), int(math.Floor(y-0.5)), int(math.Floor(z)))
	var blockFriction float64
	if m.onGround {
		blockFriction = GetBlockFriction(belowBlock)
	} else {
		blockFriction = 1.0
	}

	// vanilla getFrictionInfluencedSpeed: entire calculation in float32
	var speed float64
	if m.onGround {
		baseSpeed := m.getEffectiveSpeed(s)
		if s.Sprinting() {
			baseSpeed *= (1.0 + SprintModifier)
		}
		// vanilla: this.getSpeed() * (0.21600002F / (blockFriction * blockFriction * blockFriction))
		// getSpeed() returns float, all arithmetic is float32
		bf := float32(blockFriction)
		speed = float64(float32(baseSpeed) * (float32(FrictionSpeedFactor) / (bf * bf * bf)))
	} else {
		speed = FlyingSpeed
	}

	dx, _, dz := moveRelative(speed, forward, strafe, yaw)
	m.velX += dx
	m.velZ += dz

	return blockFriction
}

// applyAirPhysics applies gravity/levitation and friction after collision (post-move).
// Matches LivingEntity.travelInAir: levitation replaces gravity, slow falling caps it.
func (m *Module) applyAirPhysics(s *self.Module, blockFriction float64) {
	// vanilla: float friction = blockFriction * 0.91F (float32 arithmetic)
	friction := float64(float32(blockFriction) * float32(AirFrictionMul))

	levAmp := s.EffectAmplifier(effectLevitation)
	if levAmp >= 0 {
		// levitation replaces gravity: lerp toward target upward velocity
		target := LevitationPerLevel * float64(levAmp+1)
		m.velY += (target - m.velY) * LevitationLerpFactor
	} else {
		m.velY -= m.getEffectiveGravity(s)
	}

	m.velX *= friction
	m.velZ *= friction
	m.velY *= VerticalAirFriction
}

// applyWaterInputScaled adds movement input to velocity in water (pre-collision).
func (m *Module) applyWaterInputScaled(yaw, forward, strafe float64) {
	dx, _, dz := moveRelative(WaterAcceleration, forward, strafe, yaw)
	m.velX += dx
	m.velZ += dz
}

// applyWaterPhysics applies water drag and gravity after collision (post-move).
func (m *Module) applyWaterPhysics(s *self.Module) {
	slowDown := WaterSlowdown
	if s.Sprinting() {
		slowDown = WaterSprintSlowdown
	}
	m.velX *= slowDown
	m.velY *= WaterVerticalDrag
	m.velZ *= slowDown
	m.velY -= Gravity
}

// applyLavaInputScaled adds movement input to velocity in lava (pre-collision).
func (m *Module) applyLavaInputScaled(yaw, forward, strafe float64) {
	dx, _, dz := moveRelative(WaterAcceleration, forward, strafe, yaw)
	m.velX += dx
	m.velZ += dz
}

// applyLavaPhysics applies lava drag and gravity after collision (post-move).
func (m *Module) applyLavaPhysics() {
	m.velX *= LavaSlowdown
	m.velY *= LavaVerticalDrag
	m.velZ *= LavaSlowdown
	m.velY -= Gravity * LavaGravityFactor
}

// jump applies jump velocity (LivingEntity.jumpFromGround).
// Does NOT set onGround = false — vanilla sets it later inside Entity.move()
// after collision resolution. Keeping onGround = true ensures the jump tick
// uses ground friction/speed for input, matching vanilla behavior.
func (m *Module) jump(s *self.Module, yaw float64) {
	jp := m.getJumpPower(s)
	m.velY = max(jp, m.velY)
	if s.Sprinting() {
		angle := yaw * math.Pi / 180.0
		m.velX += -math.Sin(angle) * SprintJumpBoost
		m.velZ += math.Cos(angle) * SprintJumpBoost
	}
}

// getJumpPower returns jump velocity from the server-tracked attribute.
// The server sends jump_strength with jump boost modifiers already applied.
func (m *Module) getJumpPower(s *self.Module) float64 {
	return s.AttributeValue("minecraft:jump_strength", JumpPower)
}

// GetJumpPower returns current jump power from the tracked attribute.
func (m *Module) GetJumpPower() float64 {
	s := self.From(m.client)
	if s == nil {
		return JumpPower
	}
	return m.getJumpPower(s)
}

// getEffectiveGravity returns gravity from the tracked attribute,
// capped to SlowFallingGravity when slow falling and descending (client-side logic).
func (m *Module) getEffectiveGravity(s *self.Module) float64 {
	g := s.AttributeValue("minecraft:gravity", Gravity)
	if m.velY <= 0 && s.HasEffect(effectSlowFalling) {
		return min(g, SlowFallingGravity)
	}
	return g
}

// getEffectiveSpeed returns movement speed from the server-tracked attribute.
// The server sends movement_speed with speed/slowness modifiers already applied.
func (m *Module) getEffectiveSpeed(s *self.Module) float64 {
	return max(s.AttributeValue("minecraft:movement_speed", PlayerSpeed), 0)
}

// GetEffectiveSpeed returns current movement speed from the tracked attribute.
func (m *Module) GetEffectiveSpeed() float64 {
	s := self.From(m.client)
	if s == nil {
		return PlayerSpeed
	}
	return m.getEffectiveSpeed(s)
}

// notEqual returns true if two float64 values differ by more than 1e-5.
// Matches vanilla Mth.equal (Math.abs(b - a) < 1.0E-5F).
func notEqual(a, b float64) bool {
	return math.Abs(a-b) >= 1e-5
}

// modifyInput processes raw movement input matching vanilla LocalPlayer.modifyInput:
// 1. scale by InputFriction (0.98)
// 2. scale by SneakingSpeedFactor if sneaking
// 3. normalize diagonal to unit square distance (modifyInputSpeedForSquareMovement)
func modifyInput(forward, strafe float64, sneaking bool) (float64, float64) {
	if forward == 0 && strafe == 0 {
		return 0, 0
	}

	forward *= InputFriction
	strafe *= InputFriction

	if sneaking {
		forward *= SneakingSpeedFactor
		strafe *= SneakingSpeedFactor
	}

	// modifyInputSpeedForSquareMovement: clamp magnitude to distance-to-unit-square
	length := math.Sqrt(forward*forward + strafe*strafe)
	if length > 0 {
		dirF := forward / length
		dirS := strafe / length
		absF := math.Abs(dirF)
		absS := math.Abs(dirS)
		// distanceToUnitSquare: 1 / cos(atan(min/max))
		var tan float64
		if absS > absF {
			tan = absF / absS
		} else {
			tan = absS / absF
		}
		distToSquare := math.Sqrt(1 + tan*tan)
		modifiedLength := min(length*distToSquare, 1.0)
		forward = dirF * modifiedLength
		strafe = dirS * modifiedLength
	}

	return forward, strafe
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

// nonPushableEntities lists entity types where isPushable() returns false in vanilla.
// Most non-LivingEntity types default to false; ArmorStand and Bat override to false.
var nonPushableEntities = map[string]bool{
	"minecraft:armor_stand": true, "minecraft:bat": true,
	"minecraft:item": true, "minecraft:experience_orb": true,
	"minecraft:arrow": true, "minecraft:spectral_arrow": true, "minecraft:trident": true,
	"minecraft:fireball": true, "minecraft:small_fireball": true, "minecraft:dragon_fireball": true,
	"minecraft:wither_skull": true, "minecraft:shulker_bullet": true, "minecraft:llama_spit": true,
	"minecraft:wind_charge": true, "minecraft:breeze_wind_charge": true,
	"minecraft:egg": true, "minecraft:ender_pearl": true, "minecraft:snowball": true, "minecraft:potion": true,
	"minecraft:fishing_bobber": true, "minecraft:eye_of_ender": true,
	"minecraft:tnt": true, "minecraft:falling_block": true, "minecraft:firework_rocket": true,
	"minecraft:item_frame": true, "minecraft:glow_item_frame": true, "minecraft:painting": true,
	"minecraft:leash_knot": true, "minecraft:marker": true, "minecraft:lightning_bolt": true,
	"minecraft:area_effect_cloud": true, "minecraft:evoker_fangs": true, "minecraft:end_crystal": true,
	"minecraft:interaction": true, "minecraft:text_display": true, "minecraft:block_display": true,
	"minecraft:item_display": true, "minecraft:ominous_item_spawner": true,
}

// applyEntityPushing applies pushing forces from nearby entities (Entity.push).
// Vanilla: LivingEntity.pushEntities → getPushableEntities (AABB intersection) → Entity.push
func (m *Module) applyEntityPushing(x, y, z, height float64) {
	ents := entities.From(m.client)
	if ents == nil {
		return
	}

	// find entities whose AABB intersects with the player's AABB
	hw := PlayerWidth / 2
	overlapping := ents.GetEntitiesInAABB(
		x-hw, y, z-hw,
		x+hw, y+height, z+hw,
	)
	for _, e := range overlapping {
		if nonPushableEntities[e.TypeName] {
			continue
		}
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
		m.velX -= dx * push
		m.velZ -= dz * push
	}
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
		m.client.SendPacket(&packets.C2SPlayerInput{
			Flags: ns.Uint8(flags),
		})
	}
}

// sendPosition sends position/rotation packets following MC's LocalPlayer.sendPosition logic.
// Sends sprint/sneak commands first (vanilla: sendIsSprintingIfNeeded is called inside sendPosition).
func (m *Module) sendPosition(s *self.Module) {
	// sendIsSprintingIfNeeded
	sprinting := s.Sprinting()
	if sprinting != m.lastSentSprinting {
		m.lastSentSprinting = sprinting
		actionID := ns.VarInt(4) // stop sprinting
		if sprinting {
			actionID = 3 // start sprinting
		}
		m.client.SendPacket(&packets.C2SPlayerCommand{
			EntityId: ns.VarInt(s.EntityID()),
			ActionId: actionID,
		})
	}

	// send sneaking state change
	sneaking := s.Sneaking()
	if sneaking != m.lastSentSneaking {
		m.lastSentSneaking = sneaking
		actionID := ns.VarInt(1) // stop sneaking
		if sneaking {
			actionID = 0 // start sneaking
		}
		m.client.SendPacket(&packets.C2SPlayerCommand{
			EntityId: ns.VarInt(s.EntityID()),
			ActionId: actionID,
		})
	}

	m.positionReminder++

	x, y, z := s.Position()
	yaw, pitch := s.Rotation()

	dx := x - m.lastSentX
	dy := y - m.lastSentY
	dz := z - m.lastSentZ
	moved := (dx*dx+dy*dy+dz*dz) > PositionThresholdSq || m.positionReminder >= PositionReminderMax
	rotated := yaw != m.lastSentYaw || pitch != m.lastSentPitch

	var flags ns.Int8
	if m.onGround {
		flags = 0x01
	}
	if m.horizontalCollision {
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
	} else if m.onGround != m.lastSentOnGround || m.horizontalCollision != m.lastSentHorizontalCollision {
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
	m.lastSentOnGround = m.onGround
	m.lastSentHorizontalCollision = m.horizontalCollision
}
