package interaction

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-mclib/client"
	"github.com/go-mclib/client/modules/inventory"
	"github.com/go-mclib/client/modules/self"
	"github.com/go-mclib/data/slots"
)

const (
	// eatTimeout bounds one eat attempt; vanilla eating takes 32 ticks (1.6 s).
	eatTimeout = 4 * time.Second
	// eatPollInterval is one client tick.
	eatPollInterval = 50 * time.Millisecond
)

// Eat holds the first food item from foodItemIDs found in the inventory
// (swapping it into hotbar slot 8 if needed), starts using it and waits until
// the synchronized food level changes. The previous hotbar selection is
// restored afterwards. It gives up after ctx ends or eatTimeout elapses.
func Eat(ctx context.Context, c *client.Client, foodItemIDs []int32) error {
	me, inv := self.From(c), inventory.From(c)
	if me == nil || inv == nil {
		return errors.New("eating requires self and inventory modules")
	}

	slot := -1
	for _, id := range foodItemIDs {
		if slot = inv.FindItem(id); slot >= 0 {
			break
		}
	}
	if slot < 0 {
		return errors.New("no food items in inventory")
	}

	hotbarIdx := slot - slots.PlayerHotbarStart
	if slot < slots.PlayerHotbarStart || slot >= slots.PlayerHotbarEnd {
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

	ctx, cancel := context.WithTimeout(ctx, eatTimeout)
	defer cancel()
	prevFood := me.Vitals().Food
	if _, err := BeginItemUse(c, 0, c.WritePacket); err != nil {
		return fmt.Errorf("use item: %w", err)
	}

	ticker := time.NewTicker(eatPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("eating: %w", ctx.Err())
		case <-ticker.C:
			if me.Vitals().Food != prevFood {
				return nil
			}
		}
	}
}
