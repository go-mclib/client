package entities

import (
	"sync"

	"github.com/go-mclib/client/pkg/client"
	"github.com/go-mclib/client/pkg/client/modules/self"
	"github.com/go-mclib/data/pkg/data/entities"
	entity_hitboxes "github.com/go-mclib/data/pkg/data/hitboxes/entities"
	"github.com/go-mclib/data/pkg/data/packet_ids"
	"github.com/go-mclib/data/pkg/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

const ModuleName = "entities"

type Entity struct {
	ID       int32
	UUID     [16]byte
	TypeID   int32
	TypeName string

	X, Y, Z          float64
	Yaw, Pitch       float32
	HeadYaw          float32
	VelX, VelY, VelZ float64
	OnGround         bool
	Width, Height    float64
	EyeHeight        float64
	Metadata         entities.Metadata
}

type Module struct {
	client *client.Client

	mu       sync.RWMutex
	entities map[int32]*Entity

	onEntitySpawn     []func(e *Entity)
	onEntityRemove    []func(entityID int32)
	onEntityMove      []func(e *Entity)
	onEntityVelocity  []func(e *Entity)
	onEntityDamage    []func(entityID, sourceTypeID, sourceCauseID, sourceDirectID int32)
	onEntityAnimation []func(entityID int32, animation uint8)
	onHurtAnimation   []func(entityID int32, yaw float32)
}

func New() *Module {
	return &Module{
		entities: make(map[int32]*Entity),
	}
}

func (m *Module) Name() string          { return ModuleName }
func (m *Module) Init(c *client.Client) { m.client = c }

func (m *Module) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entities = make(map[int32]*Entity)
}

func From(c *client.Client) *Module {
	mod := c.Module(ModuleName)
	if mod == nil {
		return nil
	}
	return mod.(*Module)
}

// events

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
func (m *Module) OnHurtAnimation(cb func(entityID int32, yaw float32)) {
	m.onHurtAnimation = append(m.onHurtAnimation, cb)
}

func (m *Module) HandlePacket(pkt *jp.WirePacket) {
	switch pkt.PacketID {
	case packet_ids.S2CAddEntityID:
		m.handleAddEntity(pkt)
	case packet_ids.S2CRemoveEntitiesID:
		m.handleRemoveEntities(pkt)
	case packet_ids.S2CMoveEntityPosID:
		m.handleMoveEntityPos(pkt)
	case packet_ids.S2CMoveEntityPosRotID:
		m.handleMoveEntityPosRot(pkt)
	case packet_ids.S2CMoveEntityRotID:
		m.handleMoveEntityRot(pkt)
	case packet_ids.S2CSetEntityMotionID:
		m.handleSetEntityMotion(pkt)
	case packet_ids.S2CEntityPositionSyncID:
		m.handleEntityPositionSync(pkt)
	case packet_ids.S2CSetEntityDataID:
		m.handleSetEntityData(pkt)
	case packet_ids.S2CDamageEventID:
		m.handleDamageEvent(pkt)
	case packet_ids.S2CAnimateID:
		m.handleAnimate(pkt)
	case packet_ids.S2CHurtAnimationID:
		m.handleHurtAnimation(pkt)
	}
}

func (m *Module) handleAddEntity(pkt *jp.WirePacket) {
	var d packets.S2CAddEntity
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	typeName := entities.EntityTypeName(int32(d.Type))
	width, height, eyeHeight := entity_hitboxes.Dimensions(typeName)

	e := &Entity{
		ID:        int32(d.EntityId),
		UUID:      [16]byte(d.EntityUuid),
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
	}

	m.mu.Lock()
	m.entities[e.ID] = e
	m.mu.Unlock()

	for _, cb := range m.onEntitySpawn {
		cb(e)
	}
}

func (m *Module) handleRemoveEntities(pkt *jp.WirePacket) {
	// parse manually: S2CRemoveEntities is a VarInt array (count + elements),
	// but the packet struct treats it as ByteArray which misreads the format
	buf := ns.NewReader(pkt.Data)
	count, err := buf.ReadVarInt()
	if err != nil {
		return
	}

	ids := make([]int32, 0, int(count))
	for range int(count) {
		id, err := buf.ReadVarInt()
		if err != nil {
			break
		}
		ids = append(ids, int32(id))
	}

	m.mu.Lock()
	for _, id := range ids {
		delete(m.entities, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		for _, cb := range m.onEntityRemove {
			cb(id)
		}
	}
}

func (m *Module) handleMoveEntityPos(pkt *jp.WirePacket) {
	var d packets.S2CMoveEntityPos
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	m.mu.Lock()
	e := m.entities[int32(d.EntityId)]
	if e != nil {
		e.X += float64(d.DeltaX) / 4096.0
		e.Y += float64(d.DeltaY) / 4096.0
		e.Z += float64(d.DeltaZ) / 4096.0
		e.OnGround = bool(d.OnGround)
	}
	m.mu.Unlock()

	if e != nil {
		for _, cb := range m.onEntityMove {
			cb(e)
		}
	}
}

func (m *Module) handleMoveEntityPosRot(pkt *jp.WirePacket) {
	var d packets.S2CMoveEntityPosRot
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	m.mu.Lock()
	e := m.entities[int32(d.EntityId)]
	if e != nil {
		e.X += float64(d.DeltaX) / 4096.0
		e.Y += float64(d.DeltaY) / 4096.0
		e.Z += float64(d.DeltaZ) / 4096.0
		e.Yaw = float32(d.Yaw.Degrees())
		e.Pitch = float32(d.Pitch.Degrees())
		e.OnGround = bool(d.OnGround)
	}
	m.mu.Unlock()

	if e != nil {
		for _, cb := range m.onEntityMove {
			cb(e)
		}
	}
}

func (m *Module) handleMoveEntityRot(pkt *jp.WirePacket) {
	var d packets.S2CMoveEntityRot
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	m.mu.Lock()
	e := m.entities[int32(d.EntityId)]
	if e != nil {
		e.Yaw = float32(d.Yaw.Degrees())
		e.Pitch = float32(d.Pitch.Degrees())
		e.OnGround = bool(d.OnGround)
	}
	m.mu.Unlock()
}

func (m *Module) handleSetEntityMotion(pkt *jp.WirePacket) {
	var d packets.S2CSetEntityMotion
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	m.mu.Lock()
	e := m.entities[int32(d.EntityId)]
	if e != nil {
		e.VelX = d.Velocity.X
		e.VelY = d.Velocity.Y
		e.VelZ = d.Velocity.Z
	}
	m.mu.Unlock()

	if e != nil {
		for _, cb := range m.onEntityVelocity {
			cb(e)
		}
	}
}

func (m *Module) handleEntityPositionSync(pkt *jp.WirePacket) {
	var d packets.S2CEntityPositionSync
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	m.mu.Lock()
	e := m.entities[int32(d.EntityId)]
	if e != nil {
		e.X = float64(d.X)
		e.Y = float64(d.Y)
		e.Z = float64(d.Z)
		e.VelX = float64(d.VelocityX)
		e.VelY = float64(d.VelocityY)
		e.VelZ = float64(d.VelocityZ)
		e.Yaw = float32(d.Yaw)
		e.Pitch = float32(d.Pitch)
		e.OnGround = bool(d.OnGround)
	}
	m.mu.Unlock()

	if e != nil {
		for _, cb := range m.onEntityMove {
			cb(e)
		}
	}
}

func (m *Module) handleSetEntityData(pkt *jp.WirePacket) {
	var d packets.S2CSetEntityData
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	m.mu.Lock()
	e := m.entities[int32(d.EntityId)]
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
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	for _, cb := range m.onEntityDamage {
		cb(int32(d.EntityId), int32(d.SourceTypeId), int32(d.SourceCauseId), int32(d.SourceDirectId))
	}
}

func (m *Module) handleAnimate(pkt *jp.WirePacket) {
	var d packets.S2CAnimate
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	for _, cb := range m.onEntityAnimation {
		cb(int32(d.EntityId), uint8(d.Animation))
	}
}

func (m *Module) handleHurtAnimation(pkt *jp.WirePacket) {
	var d packets.S2CHurtAnimation
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	for _, cb := range m.onHurtAnimation {
		cb(int32(d.EntityId), float32(d.Yaw))
	}
}

// ownEntityID returns the player's own entity ID, or -1 if self module is not registered.
func (m *Module) ownEntityID() int32 {
	s := self.From(m.client)
	if s == nil {
		return -1
	}
	return int32(s.EntityID)
}
