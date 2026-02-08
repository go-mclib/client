package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/go-mclib/client/pkg/client/modules/inventory"
	"github.com/go-mclib/client/pkg/client/modules/self"
	"github.com/go-mclib/client/pkg/helpers"
	"github.com/go-mclib/data/pkg/data/items"
)

func main() {
	var f helpers.Flags
	helpers.RegisterFlags(&f)
	cx := flag.Int("x", 1, "chest X coordinate")
	cy := flag.Int("y", -60, "chest Y coordinate")
	cz := flag.Int("z", 0, "chest Z coordinate")
	flag.Parse()

	c := helpers.NewClient(f)
	c.Register(inventory.New())

	inv := inventory.From(c)
	s := self.From(c)

	inv.OnContainerOpen(func(windowID int32, menuType inventory.MenuType, title string) {
		c.Logger.Printf("container opened: windowID=%d menuType=%d title=%q", windowID, menuType, title)

		// wait a tick for the server to send container contents
		time.Sleep(100 * time.Millisecond)

		slotCount := inv.ContainerSlotCount()
		c.Logger.Printf("container has %d slots", slotCount)

		// log container contents
		for i := range slotCount {
			item := inv.ContainerSlot(i)
			if item != nil && !item.IsEmpty() {
				c.Logger.Printf("  container slot %d: %s x%d", i, items.ItemName(item.ID), item.Count)
			}
		}

		// shift-click all non-empty player inventory items into the container
		moved := 0
		for i := range 36 {
			viewIdx := slotCount + i
			playerSlot := inventory.SlotMainStart + i
			item := inv.GetSlot(playerSlot)
			if item == nil || item.IsEmpty() {
				continue
			}
			c.Logger.Printf("moving %s x%d from player slot %d → container (view %d)",
				items.ItemName(item.ID), item.Count, playerSlot, viewIdx)
			if err := inv.ContainerShiftClick(viewIdx); err != nil {
				c.Logger.Printf("  shift-click failed: %v", err)
				continue
			}
			moved++
			time.Sleep(50 * time.Millisecond)
		}

		c.Logger.Printf("moved %d stacks into container", moved)

		if err := inv.CloseContainer(); err != nil {
			c.Logger.Printf("failed to close container: %v", err)
		} else {
			c.Logger.Println("container closed")
		}
	})

	s.OnSpawn(func() {
		c.Logger.Println("spawned, opening chest at", *cx, *cy, *cz)

		// small delay to let physics settle
		time.Sleep(500 * time.Millisecond)

		// look at the chest
		_ = s.LookAt(float64(*cx)+0.5, float64(*cy)+0.5, float64(*cz)+0.5)

		// right-click the chest (face=1 top, hand=0 main hand)
		if err := c.InteractBlock(*cx, *cy, *cz, 1, 0, 0.5, 0.5, 0.5); err != nil {
			fmt.Println("failed to interact with block:", err)
		}
	})

	helpers.Run(c)
}
