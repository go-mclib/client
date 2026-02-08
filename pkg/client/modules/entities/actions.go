package entities

import "math"

// GetEntity returns an entity by ID, or nil if not found.
func (m *Module) GetEntity(id int32) *Entity {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.entities[id]
}

// GetAllEntities returns all tracked entities (excluding the player's own entity).
func (m *Module) GetAllEntities() []*Entity {
	ownID := m.ownEntityID()
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Entity, 0, len(m.entities))
	for _, e := range m.entities {
		if e.ID != ownID {
			result = append(result, e)
		}
	}
	return result
}

// GetEntitiesByType returns all entities with the given type ID.
func (m *Module) GetEntitiesByType(typeID int32) []*Entity {
	ownID := m.ownEntityID()
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []*Entity
	for _, e := range m.entities {
		if e.TypeID == typeID && e.ID != ownID {
			result = append(result, e)
		}
	}
	return result
}

// GetNearbyEntities returns all entities within the given radius of (x, y, z).
func (m *Module) GetNearbyEntities(x, y, z, radius float64) []*Entity {
	ownID := m.ownEntityID()
	radiusSq := radius * radius
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []*Entity
	for _, e := range m.entities {
		if e.ID == ownID {
			continue
		}
		dx := e.X - x
		dy := e.Y - y
		dz := e.Z - z
		if dx*dx+dy*dy+dz*dz <= radiusSq {
			result = append(result, e)
		}
	}
	return result
}

// GetClosestEntity returns the closest entity matching the filter, or nil.
func (m *Module) GetClosestEntity(x, y, z float64, filter func(*Entity) bool) *Entity {
	ownID := m.ownEntityID()
	m.mu.RLock()
	defer m.mu.RUnlock()
	var closest *Entity
	closestDistSq := math.MaxFloat64
	for _, e := range m.entities {
		if e.ID == ownID {
			continue
		}
		if filter != nil && !filter(e) {
			continue
		}
		dx := e.X - x
		dy := e.Y - y
		dz := e.Z - z
		distSq := dx*dx + dy*dy + dz*dz
		if distSq < closestDistSq {
			closestDistSq = distSq
			closest = e
		}
	}
	return closest
}

// GetEntityByUUID returns the entity with the given UUID, or nil.
func (m *Module) GetEntityByUUID(uuid [16]byte) *Entity {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, e := range m.entities {
		if e.UUID == uuid {
			return e
		}
	}
	return nil
}

// GetEntityCount returns the number of tracked entities.
func (m *Module) GetEntityCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entities)
}
