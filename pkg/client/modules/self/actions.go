package self

import (
	"errors"
	"fmt"
	"math"
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
	return m.Move(float64(m.X)+dx, float64(m.Y)+dy, float64(m.Z)+dz, onGround, pushingAgainstWall)
}

func (m *Module) LookAt(x, y, z float64) error {
	yaw, pitch := WorldPosToYawPitch(float64(m.X), float64(m.Y)+EyeHeight, float64(m.Z), x, y, z)
	return m.SetRotation(yaw, pitch)
}

func (m *Module) SetRotation(yaw, pitch float64) error {
	m.Yaw = ns.Float32(yaw)
	m.Pitch = ns.Float32(pitch)
	return m.client.WritePacket(&packets.C2SMovePlayerRot{
		Yaw:   ns.Float32(yaw),
		Pitch: ns.Float32(pitch),
		Flags: 0x01, // on ground
	})
}

func (m *Module) Rotate(deltaYaw, deltaPitch float64) error {
	newYaw := float64(m.Yaw) + deltaYaw
	newPitch := float64(m.Pitch) + deltaPitch

	if newPitch > 90 {
		newPitch = 90
	} else if newPitch < -90 {
		newPitch = -90
	}
	for newYaw < 0 {
		newYaw += 360
	}
	for newYaw >= 360 {
		newYaw -= 360
	}
	return m.SetRotation(newYaw, newPitch)
}

func (m *Module) Respawn() error {
	return m.client.WritePacket(&packets.C2SClientCommand{ActionId: 0})
}

func (m *Module) UseAt(hand int8, yaw, pitch float64) error {
	return m.client.WritePacket(&packets.C2SUseItem{
		Hand:     ns.VarInt(hand),
		Sequence: 0,
		Yaw:      ns.Float32(yaw),
		Pitch:    ns.Float32(pitch),
	})
}

func (m *Module) Use(hand int8) error {
	return m.UseAt(hand, float64(m.Yaw), float64(m.Pitch))
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

	prevFood := int32(m.Food)
	if err := m.Use(0); err != nil {
		return fmt.Errorf("use item: %w", err)
	}

	// wait for food level to change (eating takes ~1.6s in vanilla)
	deadline := time.After(4 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if int32(m.Food) != prevFood {
				return nil
			}
		case <-deadline:
			return errors.New("eating timed out")
		}
	}
}

// WorldPosToYawPitch calculates yaw and pitch to look from (x,y,z) at (lookX,lookY,lookZ).
// Matches MC convention: yaw 0=south(+Z), 90=west(-X), -90/270=east(+X), 180=north(-Z).
func WorldPosToYawPitch(x, y, z, lookX, lookY, lookZ float64) (yaw, pitch float64) {
	dx := lookX - x
	dy := lookY - y
	dz := lookZ - z
	yaw = math.Atan2(dz, dx)*180/math.Pi - 90
	pitch = -math.Atan2(dy, math.Sqrt(dx*dx+dz*dz)) * 180 / math.Pi
	return
}
