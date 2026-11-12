package self

import (
	"github.com/go-mclib/data/packets"
	"github.com/go-mclib/data/registries"
	jp "github.com/go-mclib/protocol/javaproto"
)

// Attribute is a synchronized entity attribute: base value plus modifiers.
// Convert a decoded packets.AttributeSnapshot with Attribute(snapshot).
type Attribute packets.AttributeSnapshot

// Value returns the effective value following vanilla
// AttributeInstance.calculateValue: add_value, then add_multiplied_base, then
// add_multiplied_total.
func (a Attribute) Value() float64 { return a.valueWithout("") }

// valueWithout calculates the value while ignoring the modifier with the given ID.
func (a Attribute) valueWithout(skipModifier string) float64 {
	value := float64(a.Base)
	for _, mod := range a.Modifiers {
		if string(mod.ID) != skipModifier && mod.Operation == 0 {
			value += float64(mod.Amount)
		}
	}
	saved := value
	for _, mod := range a.Modifiers {
		if string(mod.ID) != skipModifier && mod.Operation == 1 {
			value += saved * float64(mod.Amount)
		}
	}
	for _, mod := range a.Modifiers {
		if string(mod.ID) != skipModifier && mod.Operation == 2 {
			value *= 1.0 + float64(mod.Amount)
		}
	}
	return value
}

// AttributeValue returns the effective value of the named attribute,
// applying all modifiers in vanilla order. Returns the fallback if not tracked.
func (m *Module) AttributeValue(name string, fallback float64) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	attr, ok := m.attributes[name]
	if !ok {
		return fallback
	}
	return attr.Value()
}

// MovementSpeed predicts a local sprint toggle without double-applying a
// synchronized minecraft:sprinting modifier from the server.
func (m *Module) MovementSpeed(sprinting bool) float64 {
	m.mu.RLock()
	value := float64(float32(0.1))
	if attr, ok := m.attributes["minecraft:movement_speed"]; ok {
		value = attr.valueWithout("minecraft:sprinting")
	}
	m.mu.RUnlock()
	if sprinting {
		value *= 1 + float64(float32(0.3))
	}
	return float64(float32(max(0, min(1024, value))))
}

// scaleLocked returns the minecraft:scale attribute clamped like
// LivingEntity.getScale. Requires mu.
func (m *Module) scaleLocked() float32 {
	value := 1.0
	if attr, ok := m.attributes["minecraft:scale"]; ok {
		value = attr.Value()
	}
	return float32(max(0.0625, min(16, value)))
}

func (m *Module) handleUpdateAttributes(pkt *jp.WirePacket) {
	if id, ok := peekEntityID(pkt); !ok || id != m.EntityID() {
		return
	}
	var d packets.S2CUpdateAttributes
	if !m.client.Decode(pkt, &d) {
		return
	}

	type attrUpdate struct {
		name  string
		value float64
	}
	updates := make([]attrUpdate, 0, len(d.Attributes))

	m.mu.Lock()
	for _, snapshot := range d.Attributes {
		name := registries.Attribute.ByID(int32(snapshot.Type))
		if name == "" {
			continue
		}
		attr := Attribute(snapshot)
		m.attributes[name] = attr
		updates = append(updates, attrUpdate{name, attr.Value()})
	}
	m.mu.Unlock()

	for _, u := range updates {
		for _, cb := range m.onAttributeUpdate {
			cb(u.name, u.value)
		}
	}
}
