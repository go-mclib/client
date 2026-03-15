package self

import (
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"github.com/go-mclib/client/pkg/client/modules/inventory"
	"github.com/go-mclib/data/pkg/packets"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
)

func (m *Module) Move(x, y, z float64, onGround, pushingAgainstWall bool) error {
	var flags ns.Int8
	if onGround {
		flags |= 0x01
	}
	if pushingAgainstWall {
		flags |= 0x02
	}

	return m.client.WritePacket(&packets.C2SMovePlayerPos{
		X:     ns.Float64(x),
		FeetY: ns.Float64(y),
		Z:     ns.Float64(z),
		Flags: flags,
	})
}

func (m *Module) MoveRelative(dx, dy, dz float64, onGround, pushingAgainstWall bool) error {
	m.mu.RLock()
	x, y, z := m.x+dx, m.y+dy, m.z+dz
	m.mu.RUnlock()
	return m.Move(x, y, z, onGround, pushingAgainstWall)
}

// LookAt sets yaw/pitch to face the given world position.
func (m *Module) LookAt(x, y, z float64) {
	m.mu.Lock()
	yaw, pitch := WorldPosToYawPitch(m.x, m.y+EyeHeight, m.z, x, y, z)
	m.yaw = float32(yaw)
	m.pitch = float32(pitch)
	m.mu.Unlock()
}

// SetRotation updates yaw and pitch.
func (m *Module) SetRotation(yaw, pitch float64) {
	m.mu.Lock()
	m.yaw = float32(yaw)
	m.pitch = float32(pitch)
	m.mu.Unlock()
}

func (m *Module) Rotate(deltaYaw, deltaPitch float64) {
	m.mu.Lock()
	newYaw := float64(m.yaw) + deltaYaw
	newPitch := float64(m.pitch) + deltaPitch

	newPitch = max(-90, min(90, newPitch))
	for newYaw < 0 {
		newYaw += 360
	}
	for newYaw >= 360 {
		newYaw -= 360
	}
	m.yaw = float32(newYaw)
	m.pitch = float32(newPitch)
	m.mu.Unlock()
}

func (m *Module) Respawn() error {
	return m.client.WritePacket(&packets.C2SClientCommand{ActionId: 0})
}

func (m *Module) UseAt(hand int8, yaw, pitch float64) error {
	return m.client.WritePacket(&packets.C2SUseItem{
		Hand:     ns.VarInt(hand),
		Sequence: ns.VarInt(m.client.NextBISequence()),
		Yaw:      ns.Float32(yaw),
		Pitch:    ns.Float32(pitch),
	})
}

func (m *Module) Use(hand int8) error {
	m.mu.RLock()
	yaw, pitch := float64(m.yaw), float64(m.pitch)
	m.mu.RUnlock()
	return m.UseAt(hand, yaw, pitch)
}

// Eat finds a food item from the given list, holds it, and eats it.
// Blocks until the food level changes or times out.
func (m *Module) Eat(foodItemIDs []int32) error {
	inv := inventory.From(m.client)
	if inv == nil {
		return errors.New("inventory module not registered")
	}

	// find first available food item
	slot := -1
	for _, id := range foodItemIDs {
		if s := inv.FindItem(id); s >= 0 {
			slot = s
			break
		}
	}
	if slot < 0 {
		return errors.New("no food items in inventory")
	}

	// move to hotbar if needed
	hotbarIdx := 0
	if slot >= inventory.SlotHotbarStart && slot < inventory.SlotHotbarEnd {
		hotbarIdx = slot - inventory.SlotHotbarStart
	} else {
		hotbarIdx = 8
		if err := inv.SwapToHotbar(slot, hotbarIdx); err != nil {
			return fmt.Errorf("swap to hotbar: %w", err)
		}
	}

	prevSlot := inv.HeldSlotIndex()
	if err := inv.SetHeldSlot(hotbarIdx); err != nil {
		return fmt.Errorf("select slot: %w", err)
	}
	defer inv.SetHeldSlot(prevSlot)
	time.Sleep(50 * time.Millisecond)

	// one-shot callback to detect food change (disarms itself after firing)
	done := make(chan struct{}, 1)
	prevFood := m.Food()
	var fired atomic.Bool
	m.OnHealthSet(func(_, food float32) {
		if fired.Load() {
			return
		}
		if int32(food) != prevFood {
			fired.Store(true)
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})

	if err := m.Use(0); err != nil {
		return fmt.Errorf("use item: %w", err)
	}

	// wait for food level to change (eating takes ~1.6s in vanilla)
	select {
	case <-done:
		return nil
	case <-time.After(4 * time.Second):
		return errors.New("eating timed out")
	}
}

// WorldPosToYawPitch calculates yaw and pitch to look from (x,y,z) at (lookX,lookY,lookZ).
// MC yaw: 0=south(+Z), 90=west(-X), 180=north(-Z), 270=east(+X).
func WorldPosToYawPitch(x, y, z, lookX, lookY, lookZ float64) (yaw, pitch float64) {
	dx := lookX - x
	dy := lookY - y
	dz := lookZ - z
	yaw = -math.Atan2(dx, dz) * 180 / math.Pi
	pitch = -math.Atan2(dy, math.Sqrt(dx*dx+dz*dz)) * 180 / math.Pi
	return
}
