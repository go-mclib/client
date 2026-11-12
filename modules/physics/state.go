package physics

import (
	"math"

	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/registry"
	"github.com/go-mclib/data/blocks"
	"github.com/go-mclib/data/items"
	"github.com/go-mclib/data/registries"
	"github.com/go-mclib/data/slots"
	"github.com/go-mclib/nbt"
)

var (
	leatherBootsID  = items.ID("minecraft:leather_boots")
	scaffoldingID   = blocks.ID("minecraft:scaffolding")
	effectBlindness = registries.MobEffect.Get("minecraft:blindness")
)

func isScaffolding(state int32) bool { return blocks.StateBlockID(state) == scaffoldingID }

func (m *Module) scale() float32 {
	if m.self == nil {
		return 1
	}
	return float32(max(0.0625, min(16, m.self.AttributeValue("minecraft:scale", 1))))
}

func (m *Module) bodyWidth() float64 {
	if m.self != nil && m.self.Sleeping() {
		return float64(float32(0.2))
	}
	return float64(float32(collisions.PlayerWidth) * m.scale())
}

func (m *Module) bodyHeight() float64 {
	if m.self != nil && m.self.Sleeping() {
		return float64(float32(0.2))
	}
	height := m.poseHeight
	if height == 0 {
		height = collisions.PlayerHeight
	}
	return float64(float32(height) * m.scale())
}

func (m *Module) eyeHeight() float64 {
	if m.self != nil && m.self.Sleeping() {
		return float64(float32(0.2))
	}
	height := float32(1.62)
	switch m.poseHeight {
	case collisions.PlayerSneakingHeight:
		height = 1.27
	case collisions.PlayerWidth:
		height = 0.4
	}
	return float64(height * m.scale())
}

func (m *Module) canWalkOnPowderSnow() bool {
	if m.inventory == nil {
		return false
	}
	boots := m.inventory.GetSlot(slots.PlayerArmorFeet)
	return boots != nil && boots.Count > 0 && boots.ID == leatherBootsID
}

func (m *Module) fastLava() bool {
	if m.registry == nil {
		return false
	}
	entry, ok := m.registry.EntryByIndex(registry.DimensionTypeRegistry, int(m.self.DimensionType()))
	if !ok {
		return false
	}
	attributes, ok := entry.Data["attributes"].(nbt.Compound)
	if !ok {
		return false
	}
	value, ok := attributes["minecraft:gameplay/fast_lava"].(nbt.Byte)
	return ok && value != 0
}

func (m *Module) itemUseEffects() (factor float64, canSprint bool) {
	if !m.self.UsingItem() {
		return 1, true
	}
	factor = float64(float32(0.2))
	if m.inventory == nil {
		return factor, false
	}
	item := m.inventory.HeldItem()
	if m.self.UsingOffhand() {
		item = m.inventory.GetSlot(slots.PlayerOffhand)
	}
	if item != nil && item.Components != nil && item.Components.UseEffects != nil {
		effect := item.Components.UseEffects
		return float64(float32(effect.SpeedMultiplier)), effect.CanSprint
	}
	return factor, false
}

// Player updates its pose after travel. Input crouching is calculated separately
// before travel, so the first crouch tick still uses the previous collision box.
func (m *Module) updatePose(x, y, z float64) {
	if m.self.Sleeping() {
		return
	}
	width, scale := m.bodyWidth(), m.scale()
	canFit := func(height float64) bool {
		return m.collisions.CanFitAt(x, y, z, width, float64(float32(height)*scale))
	}
	if !canFit(collisions.PlayerWidth) {
		return
	}
	desired := collisions.PlayerHeight
	if m.swimming || m.self.FallFlying() {
		desired = collisions.PlayerWidth
	} else if m.self.Sneaking() && !m.self.IsFlying() {
		desired = collisions.PlayerSneakingHeight
	}
	if !m.self.IsSpectator() && !canFit(desired) {
		if canFit(collisions.PlayerSneakingHeight) {
			desired = collisions.PlayerSneakingHeight
		} else {
			desired = collisions.PlayerWidth
		}
	}
	m.poseHeight = desired
}

func (m *Module) updateSwimming(previousEyeInWater bool, x, y, z float64) {
	if m.self.IsFlying() || m.self.IsSpectator() || !m.self.Sprinting() {
		m.swimming = false
		return
	}
	if m.swimming {
		m.swimming = m.fluid.waterHeight > 0
		return
	}
	kind, _, _ := blocks.FluidState(m.world.GetBlock(int(math.Floor(x)), int(math.Floor(y)), int(math.Floor(z))))
	m.swimming = previousEyeInWater && kind == fluidWater
}

func (m *Module) updateSprinting(crouching bool, previousEyeInWater bool) {
	s := m.self
	if !s.Sprinting() {
		return
	}
	flying := s.IsFlying()
	possible := !s.HasEffect(effectBlindness) && (s.Vitals().Food > 6 || s.MayFly())
	forward := m.forwardImpulse > float64(float32(1e-5))
	if m.swimming {
		possible = possible && m.fluid.waterHeight > 0 && (forward || m.onGround || s.Sneaking())
	} else {
		possible = possible && forward && (flying || m.fluid.waterHeight == 0 || previousEyeInWater) && (!m.horizontalCollision || m.minorHorizontalCollision)
	}
	if !m.lastSentSprinting {
		_, itemAllowsSprint := m.itemUseEffects()
		possible = possible && itemAllowsSprint && (!crouching || previousEyeInWater) && (!s.FallFlying() || previousEyeInWater)
	}
	if !possible {
		s.SetSprinting(false)
	}
}

func (m *Module) applyJumpInput(yaw float64) {
	if m.noJumpDelay > 0 {
		m.noJumpDelay--
	}
	if !m.jumping || m.self.IsFlying() {
		m.noJumpDelay = 0
		return
	}
	height := m.fluid.waterHeight
	if m.fluid.lavaHeight > 0 {
		height = m.fluid.lavaHeight
	}
	inWater := m.fluid.waterHeight > 0 && height > 0
	threshold := m.fluid.jumpThreshold()
	if inWater && (!m.onGround || height > threshold) || m.fluid.lavaHeight > 0 && (!m.onGround || m.fluid.lavaHeight > threshold) {
		m.velY += float64(float32(0.04))
	} else if (m.onGround || inWater && height <= threshold) && m.noJumpDelay == 0 {
		m.jump(m.self, yaw)
		m.noJumpDelay = 10
	}
}

func (m *Module) backOffFromEdge(x, y, z, dx, dy, dz, maxStep float64) (float64, float64) {
	if !m.self.Sneaking() || m.self.IsFlying() || dy > 0 {
		return dx, dz
	}
	halfWidth := m.bodyWidth() / 2
	canFall := func(xd, zd, depth float64) bool {
		return m.collisions.NoCollision(collisions.AABB{
			MinX: x - halfWidth + 1e-7 + xd, MinY: y - depth - 1e-7, MinZ: z - halfWidth + 1e-7 + zd,
			MaxX: x + halfWidth - 1e-7 + xd, MaxY: y, MaxZ: z + halfWidth - 1e-7 + zd,
		})
	}
	if !m.onGround && (m.fallDistance >= maxStep || canFall(0, 0, maxStep-m.fallDistance)) {
		return dx, dz
	}
	reduce := func(delta float64) float64 {
		if math.Abs(delta) <= 0.05 {
			return 0
		}
		return delta - math.Copysign(0.05, delta)
	}
	for dx != 0 && canFall(dx, 0, maxStep) {
		dx = reduce(dx)
	}
	for dz != 0 && canFall(0, dz, maxStep) {
		dz = reduce(dz)
	}
	for dx != 0 && dz != 0 && canFall(dx, dz, maxStep) {
		dx, dz = reduce(dx), reduce(dz)
	}
	return dx, dz
}

func minorCollision(dx, dz, forward, strafe, yaw float64) bool {
	x, _, z := MoveRelative(1, forward, strafe, yaw)
	desiredSq, actualSq := x*x+z*z, dx*dx+dz*dz
	if desiredSq < float64(float32(1e-5)) || actualSq < float64(float32(1e-5)) {
		return false
	}
	return math.Acos((x*dx+z*dz)/math.Sqrt(desiredSq*actualSq)) < float64(float32(0.13962634))
}

func (m *Module) applySwimSteering(x, y, z float64, pitch float32) {
	if !m.swimming {
		return
	}
	sinPitch, _ := sinCosYaw(float64(pitch))
	lookY := -sinPitch
	multiplier := 0.06
	if lookY < -0.2 {
		multiplier = 0.085
	}
	kind, _, _ := blocks.FluidState(m.world.GetBlock(int(math.Floor(x)), int(math.Floor((y+1)-0.1)), int(math.Floor(z))))
	if lookY <= 0 || m.jumping || kind != 0 {
		m.velY += (lookY - m.velY) * multiplier
	}
}

func (m *Module) applyGliding(yaw, pitch float32) {
	// Entity.calculateViewVector retains float products before widening to Vec3.
	sy, cy := sinCos(float64(-yaw * float32(math.Pi/180)))
	sp, cp := sinCos(float64(pitch * float32(math.Pi/180)))
	lookX, lookZ := float64(float32(sy)*float32(cp)), float64(float32(cy)*float32(cp))
	lookLength, speed := math.Sqrt(lookX*lookX+lookZ*lookZ), math.Sqrt(m.velX*m.velX+m.velZ*m.velZ)
	lean := float64(pitch * float32(math.Pi/180))
	lift := math.Cos(lean)
	lift *= lift
	m.velY += m.getEffectiveGravity(m.self) * (-1 + lift*0.75)
	if m.velY < 0 && lookLength > 0 {
		convert := m.velY * -0.1 * lift
		m.velX += lookX * convert / lookLength
		m.velY += convert
		m.velZ += lookZ * convert / lookLength
	}
	if lean < 0 && lookLength > 0 {
		convert := speed * -sp * 0.04
		m.velX -= lookX * convert / lookLength
		m.velY += convert * 3.2
		m.velZ -= lookZ * convert / lookLength
	}
	if lookLength > 0 {
		m.velX += (lookX/lookLength*speed - m.velX) * 0.1
		m.velZ += (lookZ/lookLength*speed - m.velZ) * 0.1
	}
	m.velX *= float64(float32(0.99))
	m.velY *= float64(float32(0.98))
	m.velZ *= float64(float32(0.99))
}
