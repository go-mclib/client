package self

import (
	"github.com/go-mclib/data/pkg/packets"
	jp "github.com/go-mclib/protocol/java_protocol"
)

// EffectInstance represents an active potion effect on the player.
type EffectInstance struct {
	ID        int32 // protocol effect ID (registries.MobEffect)
	Amplifier int32 // 0 = Level I, 1 = Level II, etc.
	Duration  int32 // ticks remaining (-1 = infinite)
}

// HasEffect returns whether the player has the given effect active.
func (m *Module) HasEffect(effectID int32) bool {
	_, ok := m.activeEffects[effectID]
	return ok
}

// EffectAmplifier returns the amplifier of the given effect, or -1 if not active.
func (m *Module) EffectAmplifier(effectID int32) int32 {
	e, ok := m.activeEffects[effectID]
	if !ok {
		return -1
	}
	return e.Amplifier
}

// TickEffects decrements durations and removes expired effects.
// Matches vanilla MobEffectInstance.tickClient. Called once per tick by the physics module.
func (m *Module) TickEffects() {
	for id, e := range m.activeEffects {
		if e.Duration == -1 {
			continue // infinite
		}
		e.Duration--
		if e.Duration <= 0 {
			delete(m.activeEffects, id)
		}
	}
}

func (m *Module) handleUpdateMobEffect(pkt *jp.WirePacket) {
	var d packets.S2CUpdateMobEffect
	if err := pkt.ReadInto(&d); err != nil {
		return
	}
	if int32(d.EntityId) != int32(m.EntityID) {
		return
	}
	m.activeEffects[int32(d.EffectId)] = &EffectInstance{
		ID:        int32(d.EffectId),
		Amplifier: int32(d.Amplifier),
		Duration:  int32(d.Duration),
	}
}

func (m *Module) handleRemoveMobEffect(pkt *jp.WirePacket) {
	var d packets.S2CRemoveMobEffect
	if err := pkt.ReadInto(&d); err != nil {
		return
	}
	if int32(d.EntityId) != int32(m.EntityID) {
		return
	}
	delete(m.activeEffects, int32(d.EffectId))
}
