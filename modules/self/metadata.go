package self

import (
	"github.com/go-mclib/data/entities"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/javaproto"
	ns "github.com/go-mclib/protocol/javaproto/wire"
)

// peekEntityID reads the leading VarInt entity ID of an entity packet, so
// handlers can skip foreign entities without decoding the whole payload.
func peekEntityID(pkt *jp.WirePacket) (int32, bool) {
	id, err := ns.NewReader(pkt.Data).ReadVarInt()
	return int32(id), err == nil
}

func (m *Module) handleOwnMetadata(pkt *jp.WirePacket) {
	if id, ok := peekEntityID(pkt); !ok || id != m.EntityID() {
		return
	}
	var d packets.S2CSetEntityData
	if !m.client.Decode(pkt, &d) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range d.Metadata {
		if len(entry.Data) == 0 {
			continue
		}
		switch int(entry.Index) {
		case entities.EntityIndexFlags:
			m.entityFlags = entry.Data[0]
		case entities.EntityIndexNoGravity:
			m.noGravity = entry.Data[0] != 0
		case entities.LivingEntityIndexLivingFlags:
			m.livingFlags = entry.Data[0]
		case entities.LivingEntityIndexSleepingPos:
			m.sleeping = entry.Data[0] != 0
		}
	}
}

func (m *Module) handlePassengers(pkt *jp.WirePacket) {
	var d packets.S2CSetPassengers
	if !m.client.Decode(pkt, &d) {
		return
	}
	m.mu.Lock()
	if int32(d.EntityID) == int32(m.login.EntityID) {
		m.isVehicle = len(d.Passengers) != 0
	}
	previous := m.vehicleID
	if m.vehicleID == int32(d.EntityID) {
		m.vehicleID = 0
	}
	for _, id := range d.Passengers {
		if int32(id) == int32(m.login.EntityID) {
			m.vehicleID = int32(d.EntityID)
			break
		}
	}
	current := m.vehicleID
	m.mu.Unlock()
	m.notifyVehicleChange(previous, current)
}

func (m *Module) notifyVehicleChange(previous, current int32) {
	if previous == current {
		return
	}
	for _, cb := range m.onVehicleChange {
		cb(previous, current)
	}
}

// VehicleID returns the entity ID of the vehicle the player rides, or 0.
// self is the single owner of this state; physics reads it from here.
func (m *Module) VehicleID() int32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.vehicleID
}

// NoGravity reports the server-synchronized entity gravity flag.
func (m *Module) NoGravity() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.noGravity
}

// Sleeping reports the synchronized bed position's presence.
func (m *Module) Sleeping() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sleeping
}

// FallFlying reports the synchronized gliding flag.
func (m *Module) FallFlying() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.entityFlags&0x80 != 0
}

// IsVehicle reports whether the player currently carries passengers.
func (m *Module) IsVehicle() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.isVehicle
}

// UsingItem reports whether the server has acknowledged continuous item use.
func (m *Module) UsingItem() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.livingFlags&1 != 0
}

// UsingOffhand distinguishes the active hand during continuous item use.
func (m *Module) UsingOffhand() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.livingFlags&2 != 0
}
