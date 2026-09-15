package pathfinding

import (
	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/physics"
)

type simulationConfig struct {
	jumpPower, speed, gravity              float64
	frictionModifier, airDragModifier      float64
	maxStep, width, height, sneakingHeight float64
}

func defaultSimulationConfig() simulationConfig {
	return simulationConfig{
		jumpPower: physics.JumpPower, speed: physics.PlayerSpeed * (1 + physics.SprintModifier), gravity: physics.Gravity,
		frictionModifier: 1, airDragModifier: 1, maxStep: collisions.StepUpHeight,
		width: playerWidth, height: playerHeight, sneakingHeight: playerSneakingHeight,
	}
}

func (m *Module) simulationConfig() simulationConfig {
	s := m.self
	scale := float32(s.AttributeValue("minecraft:scale", 1))
	gravity := s.AttributeValue("minecraft:gravity", physics.Gravity)
	if s.NoGravity() {
		gravity = 0
	}
	return simulationConfig{
		jumpPower: m.physics.GetJumpPower(), speed: s.MovementSpeed(true), gravity: gravity,
		frictionModifier: s.AttributeValue("minecraft:friction_modifier", 1),
		airDragModifier:  s.AttributeValue("minecraft:air_drag_modifier", 1),
		maxStep:          float64(float32(s.AttributeValue("minecraft:step_height", collisions.StepUpHeight))),
		width:            float64(float32(playerWidth) * scale), height: float64(float32(playerHeight) * scale),
		sneakingHeight: float64(float32(playerSneakingHeight) * scale),
	}
}
