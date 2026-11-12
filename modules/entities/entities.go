package entities

import (
	"math"
	"sync"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/playerlist"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/data/entities"
	"github.com/go-mclib/data/packetids"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

const ModuleName = "entities"

type Entity struct {
	ID       int32
	UUID     [16]byte
	TypeID   int32
	TypeName string

	X, Y, Z                                              float64
	Yaw, Pitch                                           float32
	HeadYaw                                              float32
	VelX, VelY, VelZ                                     float64
	OnGround                                             bool
	Width, Height                                        float64
	EyeHeight                                            float64
	SpawnData                                            int32 // extra data from S2CAddEntity (e.g. block state for falling blocks)
	Metadata                                             entities.Metadata
	Equipment                                            map[uint8]EquipmentItem
	physicsX, physicsY, physicsZ                         float64
	interpolationSteps                                   int
	positionSteps                                        []packets.PositionStep
	stepStartX, stepStartY, stepStartZ                   float64
	currentStepTicks, remainingTicks, interpolationSpeed float32
	peekAmount                                           float32
	scoreName                                            string
	scale                                                float32 // zero until the server synchronizes the scale attribute
}

type Module struct {
	client *client.Client

	// optional deps (entities degrades gracefully without them)
	self       *self.Module
	collisions *collisions.Module
	playerlist *playerlist.Module

	mu          sync.RWMutex
	entities    map[int32]*Entity
	entityOrder []int32           // insertion order, matching EntityTickList on the client
	vehicles    map[int32]int32   // passenger ID -> vehicle ID; includes local player
	teams       map[string]int32  // team name -> collision rule
	memberTeams map[string]string // scoreboard entry -> team name

	onEntitySpawn     []func(e *Entity)
	onEntityRemove    []func(entityID int32)
	onEntityMove      []func(e *Entity)
	onEntityVelocity  []func(e *Entity)
	onEntityDamage    []func(entityID, sourceTypeID, sourceCauseID, sourceDirectID int32)
	onEntityAnimation []func(entityID int32, animation uint8)
	onSwingAnimation  []func(entityID, hand, animationType, duration int32)
	onHurtAnimation   []func(entityID int32, yaw float32)
}

func New() *Module {
	m := &Module{}
	m.Reset()
	return m
}

func (m *Module) Name() string { return ModuleName }
func (m *Module) Init(c *client.Client) {
	m.client = c
	c.OnTransfer(m.Reset)
	c.OnDisconnect(m.Reset)
}

func (m *Module) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clearInstancesLocked()
	m.teams = make(map[string]int32)
	m.memberTeams = make(map[string]string)
}

// clearInstancesLocked forgets every tracked entity and passenger link.
// Scoreboard teams are kept: they survive dimension changes.
func (m *Module) clearInstancesLocked() {
	m.entities = make(map[int32]*Entity)
	m.entityOrder = nil
	m.vehicles = make(map[int32]int32)
}

func (m *Module) Wire() error {
	m.self = self.From(m.client)
	m.collisions = collisions.From(m.client)
	m.playerlist = playerlist.From(m.client)
	return nil
}

func From(c *client.Client) *Module { return client.ModuleAs[*Module](c, ModuleName) }

func (m *Module) OnEntitySpawn(cb func(e *Entity)) { m.onEntitySpawn = append(m.onEntitySpawn, cb) }
func (m *Module) OnEntityRemove(cb func(entityID int32)) {
	m.onEntityRemove = append(m.onEntityRemove, cb)
}
func (m *Module) OnEntityMove(cb func(e *Entity)) { m.onEntityMove = append(m.onEntityMove, cb) }
func (m *Module) OnEntityVelocity(cb func(e *Entity)) {
	m.onEntityVelocity = append(m.onEntityVelocity, cb)
}
func (m *Module) OnEntityDamage(cb func(entityID, sourceTypeID, sourceCauseID, sourceDirectID int32)) {
	m.onEntityDamage = append(m.onEntityDamage, cb)
}
func (m *Module) OnEntityAnimation(cb func(entityID int32, animation uint8)) {
	m.onEntityAnimation = append(m.onEntityAnimation, cb)
}

// OnSwingAnimation observes hand (0 main, 1 off), type (0 none, 1 whack,
// 2 stab), and duration in ticks separately from wake/critical animations.
func (m *Module) OnSwingAnimation(cb func(entityID, hand, animationType, duration int32)) {
	m.onSwingAnimation = append(m.onSwingAnimation, cb)
}
func (m *Module) OnHurtAnimation(cb func(entityID int32, yaw float32)) {
	m.onHurtAnimation = append(m.onHurtAnimation, cb)
}

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	if m.client.State() != jp.StatePlay {
		return
	}
	switch pkt.PacketID {
	case packetids.S2CRespawnID:
		// Scoreboard teams survive dimension changes, unlike entity instances.
		m.mu.Lock()
		m.clearInstancesLocked()
		m.mu.Unlock()
	case packetids.S2CSetPassengersID:
		m.handleSetPassengers(pkt)
	case packetids.S2CSetPlayerTeamID:
		m.handleSetPlayerTeam(pkt)
	case packetids.S2CUpdateAttributesID:
		m.handleUpdateAttributes(pkt)
	case packetids.S2CEntityEventID:
		m.handleEntityEvent(pkt)
	case packetids.S2CSetEquipmentID:
		m.handleSetEquipment(pkt)
	case packetids.S2CAddEntityID:
		m.handleAddEntity(pkt)
	case packetids.S2CRemoveEntitiesID:
		m.handleRemoveEntities(pkt)
	case packetids.S2CForgetLevelChunkID:
		m.handleForgetLevelChunk(pkt)
	case packetids.S2CMoveEntityPosID:
		m.handleMoveEntityPos(pkt)
	case packetids.S2CMoveEntityPosRotID:
		m.handleMoveEntityPosRot(pkt)
	case packetids.S2CMoveEntityRotID:
		m.handleMoveEntityRot(pkt)
	case packetids.S2CSetEntityMotionID:
		m.handleSetEntityMotion(pkt)
	case packetids.S2CEntityPositionSyncID:
		m.handleEntityPositionSync(pkt)
	case packetids.S2CSetEntityDataID:
		m.handleSetEntityData(pkt)
	case packetids.S2CDamageEventID:
		m.handleDamageEvent(pkt)
	case packetids.S2CAnimateID:
		m.handleAnimate(pkt)
	case packetids.S2CSwingAnimationID:
		m.handleSwingAnimation(pkt)
	case packetids.S2CHurtAnimationID:
		m.handleHurtAnimation(pkt)
	}
}

func (m *Module) handleAddEntity(pkt *jp.WirePacket) {
	var d packets.S2CAddEntity
	if !m.client.Decode(pkt, &d) {
		return
	}

	typeName := entities.TypeName(int32(d.Type))
	width, height, eyeHeight := entities.Dimensions(typeName)

	e := &Entity{
		ID:        int32(d.EntityID),
		UUID:      [16]byte(d.EntityUUID),
		TypeID:    int32(d.Type),
		TypeName:  typeName,
		X:         float64(d.X),
		Y:         float64(d.Y),
		Z:         float64(d.Z),
		Yaw:       float32(d.Yaw.Degrees()),
		Pitch:     float32(d.Pitch.Degrees()),
		HeadYaw:   float32(d.HeadYaw.Degrees()),
		VelX:      d.Velocity.X,
		VelY:      d.Velocity.Y,
		VelZ:      d.Velocity.Z,
		Width:     float64(width),
		Height:    float64(height),
		EyeHeight: float64(eyeHeight),
		SpawnData: int32(d.Data),
	}
	e.physicsX, e.physicsY, e.physicsZ = e.X, e.Y, e.Z
	e.scoreName = ns.UUID(e.UUID).String()

	m.mu.Lock()
	m.trackEntityLocked(e)
	m.mu.Unlock()

	for _, cb := range m.onEntitySpawn {
		cb(cloneEntity(e))
	}
}

func (m *Module) handleRemoveEntities(pkt *jp.WirePacket) {
	var d packets.S2CRemoveEntities
	if !m.client.Decode(pkt, &d) {
		return
	}

	m.mu.Lock()
	for _, id := range d.EntityIds {
		m.removeTrackedEntityLocked(int32(id))
	}
	m.mu.Unlock()

	for _, id := range d.EntityIds {
		for _, cb := range m.onEntityRemove {
			cb(int32(id))
		}
	}
}

func (m *Module) handleForgetLevelChunk(pkt *jp.WirePacket) {
	var d packets.S2CForgetLevelChunk
	if !m.client.Decode(pkt, &d) {
		return
	}

	cx, cz := int32(d.ChunkX), int32(d.ChunkZ)
	var removed []int32

	m.mu.Lock()
	for id, e := range m.entities {
		ecx := int32(math.Floor(e.X / 16))
		ecz := int32(math.Floor(e.Z / 16))
		if ecx == cx && ecz == cz {
			m.removeTrackedEntityLocked(id)
			removed = append(removed, id)
		}
	}
	m.mu.Unlock()

	for _, id := range removed {
		for _, cb := range m.onEntityRemove {
			cb(id)
		}
	}
}

func (m *Module) handleMoveEntityPos(pkt *jp.WirePacket) {
	var d packets.S2CMoveEntityPos
	if !m.client.Decode(pkt, &d) {
		return
	}

	m.mu.Lock()
	e := m.entities[int32(d.EntityID)]
	if e != nil {
		e.moveDelta(d.Delta, e.Yaw, e.Pitch)
		e.OnGround = bool(d.OnGround)
		if len(m.onEntityMove) > 0 {
			e = cloneEntity(e)
		}
	}
	m.mu.Unlock()

	if e != nil {
		for _, cb := range m.onEntityMove {
			cb(cloneEntity(e))
		}
	}
}

func (m *Module) handleMoveEntityPosRot(pkt *jp.WirePacket) {
	var d packets.S2CMoveEntityPosRot
	if !m.client.Decode(pkt, &d) {
		return
	}

	m.mu.Lock()
	e := m.entities[int32(d.EntityID)]
	if e != nil {
		e.moveDelta(d.Delta, float32(d.Yaw.Degrees()), float32(d.Pitch.Degrees()))
		e.OnGround = bool(d.OnGround)
		if len(m.onEntityMove) > 0 {
			e = cloneEntity(e)
		}
	}
	m.mu.Unlock()

	if e != nil {
		for _, cb := range m.onEntityMove {
			cb(cloneEntity(e))
		}
	}
}

func (m *Module) handleMoveEntityRot(pkt *jp.WirePacket) {
	var d packets.S2CMoveEntityRot
	if !m.client.Decode(pkt, &d) {
		return
	}

	m.mu.Lock()
	e := m.entities[int32(d.EntityID)]
	if e != nil {
		e.moveTo(e.X, e.Y, e.Z, float32(d.Yaw.Degrees()), float32(d.Pitch.Degrees()), false)
		e.OnGround = bool(d.OnGround)
	}
	m.mu.Unlock()
}

func (m *Module) handleSetEntityMotion(pkt *jp.WirePacket) {
	var d packets.S2CSetEntityMotion
	if !m.client.Decode(pkt, &d) {
		return
	}

	m.mu.Lock()
	e := m.entities[int32(d.EntityID)]
	if e != nil {
		e.VelX = d.Velocity.X
		e.VelY = d.Velocity.Y
		e.VelZ = d.Velocity.Z
		if len(m.onEntityVelocity) > 0 {
			e = cloneEntity(e)
		}
	}
	m.mu.Unlock()

	if e != nil {
		for _, cb := range m.onEntityVelocity {
			cb(cloneEntity(e))
		}
	}
}

func (m *Module) handleEntityPositionSync(pkt *jp.WirePacket) {
	var d packets.S2CEntityPositionSync
	if !m.client.Decode(pkt, &d) {
		return
	}

	m.mu.Lock()
	e := m.entities[int32(d.EntityID)]
	if e != nil {
		e.movePath(d.Path, float32(d.Yaw), float32(d.Pitch), true)
		e.OnGround = bool(d.OnGround)
		if len(m.onEntityMove) > 0 {
			e = cloneEntity(e)
		}
	}
	m.mu.Unlock()

	if e != nil {
		for _, cb := range m.onEntityMove {
			cb(cloneEntity(e))
		}
	}
}

func (m *Module) handleSetEntityData(pkt *jp.WirePacket) {
	var d packets.S2CSetEntityData
	if !m.client.Decode(pkt, &d) {
		return
	}

	m.mu.Lock()
	e := m.entities[int32(d.EntityID)]
	if e != nil {
		// merge entries instead of replacing — S2CSetEntityData only sends
		// dirty entries, so replacing would lose previously set values
		for _, entry := range d.Metadata {
			e.Metadata.Set(entry.Index, entry.Serializer, entry.Data)
		}
	}
	m.mu.Unlock()
}

func (m *Module) handleDamageEvent(pkt *jp.WirePacket) {
	var d packets.S2CDamageEvent
	if !m.client.Decode(pkt, &d) {
		return
	}

	for _, cb := range m.onEntityDamage {
		cb(int32(d.EntityID), int32(d.SourceTypeID), int32(d.SourceCauseID), int32(d.SourceDirectID))
	}
}

func (m *Module) handleAnimate(pkt *jp.WirePacket) {
	var d packets.S2CAnimate
	if !m.client.Decode(pkt, &d) {
		return
	}

	for _, cb := range m.onEntityAnimation {
		cb(int32(d.EntityID), uint8(d.Animation))
	}
}

func (m *Module) handleSwingAnimation(pkt *jp.WirePacket) {
	var d packets.S2CSwingAnimation
	if !m.client.Decode(pkt, &d) {
		return
	}
	for _, cb := range m.onSwingAnimation {
		cb(int32(d.EntityID), int32(d.Hand), int32(d.Type), int32(d.Duration))
	}
}

func (m *Module) handleHurtAnimation(pkt *jp.WirePacket) {
	var d packets.S2CHurtAnimation
	if !m.client.Decode(pkt, &d) {
		return
	}

	for _, cb := range m.onHurtAnimation {
		cb(int32(d.EntityID), float32(d.Yaw))
	}
}

// ownEntityID returns the player's own entity ID, or -1 if self module is not registered.
func (m *Module) ownEntityID() int32 {
	if m.self == nil {
		return -1
	}
	return m.self.EntityID()
}
