package self

import (
	"github.com/go-mclib/data/entities"
	"github.com/go-mclib/data/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
)

func (m *Module) handleOwnMetadata(pkt *jp.WirePacket) {
	var d packets.S2CSetEntityData
	if pkt.ReadInto(&d) != nil || int32(d.EntityId) != m.EntityID() {
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
	if pkt.ReadInto(&d) != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if int32(d.EntityId) == m.entityID {
		m.isVehicle = len(d.Passengers) != 0
	}
	if m.vehicleID == int32(d.EntityId) {
		m.vehicleID = 0
	}
	for _, id := range d.Passengers {
		if int32(id) == m.entityID {
			m.vehicleID = int32(d.EntityId)
			break
		}
	}
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
