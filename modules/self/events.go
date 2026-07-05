package self

func (m *Module) OnDeath(cb func())   { m.onDeath = append(m.onDeath, cb) }
func (m *Module) OnSpawn(cb func())   { m.onSpawn = append(m.onSpawn, cb) }
func (m *Module) OnRespawn(cb func()) { m.onRespawn = append(m.onRespawn, cb) }
func (m *Module) OnHealthSet(cb func(health, food float32)) {
	m.onHealthSet = append(m.onHealthSet, cb)
}
func (m *Module) OnPosition(cb func(x, y, z float64)) { m.onPosition = append(m.onPosition, cb) }
func (m *Module) OnGameEvent(cb func(event uint8, value float32)) {
	m.onGameEvent = append(m.onGameEvent, cb)
}
func (m *Module) OnGamemodeChange(cb func(gamemode uint8)) {
	m.onGamemodeChange = append(m.onGamemodeChange, cb)
}
func (m *Module) OnDimensionChange(cb func(dimensionName string)) {
	m.onDimensionChange = append(m.onDimensionChange, cb)
}
func (m *Module) OnEffectAdded(cb func(effectID, amplifier, duration int32)) {
	m.onEffectAdded = append(m.onEffectAdded, cb)
}
func (m *Module) OnEffectRemoved(cb func(effectID int32)) {
	m.onEffectRemoved = append(m.onEffectRemoved, cb)
}
func (m *Module) OnDifficultyChange(cb func(difficulty uint8, locked bool)) {
	m.onDifficultyChange = append(m.onDifficultyChange, cb)
}
func (m *Module) OnAbilitiesChange(cb func(flags int8, flySpeed, fovMod float32)) {
	m.onAbilitiesChange = append(m.onAbilitiesChange, cb)
}
func (m *Module) OnTimeUpdate(cb func(worldAge, timeOfDay int64)) {
	m.onTimeUpdate = append(m.onTimeUpdate, cb)
}
func (m *Module) OnExperienceChange(cb func(bar float32, level, total int32)) {
	m.onExperienceChange = append(m.onExperienceChange, cb)
}
func (m *Module) OnAttributeUpdate(cb func(name string, value float64)) {
	m.onAttributeUpdate = append(m.onAttributeUpdate, cb)
}
