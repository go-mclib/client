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
	m.effectsMu.Lock()
	defer m.effectsMu.Unlock()
	_, ok := m.activeEffects[effectID]
	return ok
}

// EffectAmplifier returns the amplifier of the given effect, or -1 if not active.
func (m *Module) EffectAmplifier(effectID int32) int32 {
	m.effectsMu.Lock()
	defer m.effectsMu.Unlock()
	e, ok := m.activeEffects[effectID]
	if !ok {
		return -1
	}
	return e.Amplifier
}

// TickEffects decrements durations and removes expired effects.
// Matches vanilla MobEffectInstance.tickClient. Called once per tick by the physics module.
func (m *Module) TickEffects() {
	m.effectsMu.Lock()
	defer m.effectsMu.Unlock()
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

	m.mu.RLock()
	isUs := int32(d.EntityId) == m.entityID
	m.mu.RUnlock()
	if !isUs {
		return
	}

	effectID := int32(d.EffectId)
	amp := int32(d.Amplifier)
	dur := int32(d.Duration)
	m.effectsMu.Lock()
	m.activeEffects[effectID] = &EffectInstance{
		ID:        effectID,
		Amplifier: amp,
		Duration:  dur,
	}
	m.effectsMu.Unlock()

	for _, cb := range m.onEffectAdded {
		cb(effectID, amp, dur)
	}
}

func (m *Module) handleRemoveMobEffect(pkt *jp.WirePacket) {
	var d packets.S2CRemoveMobEffect
	if err := pkt.ReadInto(&d); err != nil {
		return
	}

	m.mu.RLock()
	isUs := int32(d.EntityId) == m.entityID
	m.mu.RUnlock()
	if !isUs {
		return
	}

	effectID := int32(d.EffectId)
	m.effectsMu.Lock()
	delete(m.activeEffects, effectID)
	m.effectsMu.Unlock()

	for _, cb := range m.onEffectRemoved {
		cb(effectID)
	}
}
