package self

import (
	"github.com/go-mclib/data/pkg/data/registries"
	jp "github.com/go-mclib/protocol/java_protocol"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

// AttributeModifier represents a modifier applied to a base attribute.
type AttributeModifier struct {
	ID        string // resource identifier (e.g., "minecraft:effect.speed")
	Amount    float64
	Operation int32 // 0=add_value, 1=add_multiplied_base, 2=add_multiplied_total
}

// Attribute represents a tracked entity attribute.
type Attribute struct {
	Base      float64
	Modifiers []AttributeModifier
}

// computeValue calculates the effective value following vanilla AttributeInstance.calculateValue.
func (a *Attribute) computeValue() float64 {
	value := a.Base
	for _, mod := range a.Modifiers {
		if mod.Operation == 0 {
			value += mod.Amount
		}
	}
	saved := value
	for _, mod := range a.Modifiers {
		if mod.Operation == 1 {
			value += saved * mod.Amount
		}
	}
	for _, mod := range a.Modifiers {
		if mod.Operation == 2 {
			value *= 1.0 + mod.Amount
		}
	}
	return value
}

// AttributeValue returns the effective value of the named attribute,
// applying all modifiers in vanilla order. Returns the fallback if not tracked.
func (m *Module) AttributeValue(name string, fallback float64) float64 {
	m.mu.RLock()
	attr, ok := m.attributes[name]
	if !ok {
		m.mu.RUnlock()
		return fallback
	}
	val := attr.computeValue()
	m.mu.RUnlock()
	return val
}

func (m *Module) handleUpdateAttributes(pkt *jp.WirePacket) {
	// parse raw data: the S2CUpdateAttributes binding stores remaining data
	// as ByteArray which doesn't match the wire format, so we parse manually
	buf := ns.NewReader(pkt.Data)

	entityID, err := buf.ReadVarInt()
	if err != nil {
		return
	}
	if int32(entityID) != m.entityID {
		return
	}

	count, err := buf.ReadVarInt()
	if err != nil {
		return
	}

	type attrUpdate struct {
		name  string
		value float64
	}
	var updates []attrUpdate

	m.mu.Lock()
	for range int(count) {
		attrID, err := buf.ReadVarInt()
		if err != nil {
			break
		}
		name := registries.Attribute.ByID(int32(attrID))
		if name == "" {
			break
		}

		base, err := buf.ReadFloat64()
		if err != nil {
			break
		}

		modCount, err := buf.ReadVarInt()
		if err != nil {
			break
		}

		mods := make([]AttributeModifier, 0, int(modCount))
		for range int(modCount) {
			modID, err := buf.ReadIdentifier()
			if err != nil {
				break
			}
			amount, err := buf.ReadFloat64()
			if err != nil {
				break
			}
			op, err := buf.ReadVarInt()
			if err != nil {
				break
			}
			mods = append(mods, AttributeModifier{
				ID:        string(modID),
				Amount:    float64(amount),
				Operation: int32(op),
			})
		}

		attr := &Attribute{
			Base:      float64(base),
			Modifiers: mods,
		}
		m.attributes[name] = attr
		updates = append(updates, attrUpdate{name, attr.computeValue()})
	}
	m.mu.Unlock()

	for _, u := range updates {
		for _, cb := range m.onAttributeUpdate {
			cb(u.name, u.value)
		}
	}
}
