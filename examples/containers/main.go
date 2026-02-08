package main

import (
	"flag"
	"math"
	"sync"
	"time"

	"github.com/go-mclib/client/pkg/client/modules/entities"
	"github.com/go-mclib/client/pkg/client/modules/inventory"
	"github.com/go-mclib/client/pkg/client/modules/pathfinding"
	"github.com/go-mclib/client/pkg/client/modules/self"
	"github.com/go-mclib/client/pkg/client/modules/world"
	"github.com/go-mclib/client/pkg/helpers"
	"github.com/go-mclib/data/pkg/data/blocks"
	blockHitboxes "github.com/go-mclib/data/pkg/data/hitboxes/blocks"
	"github.com/go-mclib/data/pkg/data/items"
)

var containerBlockIDs = []int32{
	blocks.BlockID("minecraft:chest"),
	blocks.BlockID("minecraft:trapped_chest"),
	blocks.BlockID("minecraft:barrel"),
	blocks.BlockID("minecraft:shulker_box"),
}

// findAdjacentWalkable returns a cardinal neighbor of (bx, by, bz) where the
// player can stand: solid ground below, no collision at feet and head level.
func findAdjacentWalkable(w *world.Module, bx, by, bz int) (int, int, bool) {
	offsets := [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}
	for _, off := range offsets {
		nx, nz := bx+off[0], bz+off[1]
		below := w.GetBlock(nx, by-1, nz)
		feet := w.GetBlock(nx, by, nz)
		head := w.GetBlock(nx, by+1, nz)
		if blockHitboxes.HasCollision(below) && !blockHitboxes.HasCollision(feet) && !blockHitboxes.HasCollision(head) {
			return nx, nz, true
		}
	}
	return 0, 0, false
}

func main() {
	var f helpers.Flags
	helpers.RegisterFlags(&f)
	flag.Parse()

	c := helpers.NewClient(f)
	c.Register(entities.New())
	c.Register(pathfinding.New())
	c.Register(inventory.New())

	inv := inventory.From(c)
	s := self.From(c)
	w := world.From(c)
	pf := pathfinding.From(c)

	var mu sync.Mutex
	var storing bool

	findNearestContainer := func() (int, int, int, bool) {
		px, py, pz := float64(s.X), float64(s.Y), float64(s.Z)
		bestDist := math.MaxFloat64
		var bx, by, bz int
		found := false

		w.FindBlocks(containerBlockIDs, func(x, y, z int, _ int32) bool {
			dx, dy, dz := float64(x)-px, float64(y)-py, float64(z)-pz
			dist := dx*dx + dy*dy + dz*dz
			if dist < bestDist {
				bestDist = dist
				bx, by, bz = x, y, z
				found = true
			}
			return true
		})
		return bx, by, bz, found
	}

	hasEmptyContainerSlot := func() bool {
		for i := range inv.ContainerSlotCount() {
			item := inv.ContainerSlot(i)
			if item == nil || item.IsEmpty() {
				return true
			}
		}
		return false
	}

	hasPlayerItems := func() bool {
		for i := inventory.SlotMainStart; i < inventory.SlotHotbarEnd; i++ {
			item := inv.GetSlot(i)
			if item != nil && !item.IsEmpty() {
				return true
			}
		}
		return false
	}

	// storeItems navigates to the nearest container and stores all player inventory items.
	var storeItems func()
	storeItems = func() {
		mu.Lock()
		if storing || !hasPlayerItems() {
			mu.Unlock()
			return
		}
		storing = true
		mu.Unlock()

		defer func() {
			mu.Lock()
			storing = false
			mu.Unlock()
		}()

		cx, cy, cz, found := findNearestContainer()
		if !found {
			c.Logger.Println("no container found in loaded chunks")
			return
		}
		c.Logger.Printf("nearest container at %d, %d, %d", cx, cy, cz)

		// navigate to an adjacent walkable block if too far to interact
		dx := float64(cx) + 0.5 - float64(s.X)
		dz := float64(cz) + 0.5 - float64(s.Z)
		if math.Sqrt(dx*dx+dz*dz) > 4.0 {
			// pick a cardinal neighbor that's walkable (no collision at feet+head, solid below)
			adjX, adjZ, adjFound := findAdjacentWalkable(w, cx, cy, cz)
			if !adjFound {
				c.Logger.Println("no walkable block adjacent to container")
				return
			}

			done := make(chan bool, 1)
			pf.OnNavigationComplete(func(reached bool) {
				done <- reached
			})
			if err := pf.NavigateTo(float64(adjX)+0.5, float64(cy), float64(adjZ)+0.5); err != nil {
				c.Logger.Printf("pathfinding failed: %v", err)
				return
			}
			if !<-done {
				c.Logger.Println("could not reach container")
				return
			}
			time.Sleep(200 * time.Millisecond)
		}

		_ = s.LookAt(float64(cx)+0.5, float64(cy)+0.5, float64(cz)+0.5)
		time.Sleep(50 * time.Millisecond)

		if err := c.InteractBlock(cx, cy, cz, 1, 0, 0.5, 0.5, 0.5); err != nil {
			c.Logger.Printf("failed to interact with container: %v", err)
			return
		}

		// wait for container to open
		opened := make(chan struct{}, 1)
		inv.OnContainerOpen(func(_ int32, _ inventory.MenuType, _ string) {
			select {
			case opened <- struct{}{}:
			default:
			}
		})
		select {
		case <-opened:
		case <-time.After(2 * time.Second):
			c.Logger.Println("timed out waiting for container to open")
			return
		}

		// wait for contents
		time.Sleep(100 * time.Millisecond)

		slotCount := inv.ContainerSlotCount()
		c.Logger.Printf("container has %d slots", slotCount)

		for i := range slotCount {
			item := inv.ContainerSlot(i)
			if item != nil && !item.IsEmpty() {
				c.Logger.Printf("  slot %d: %s x%d", i, items.ItemName(item.ID), item.Count)
			}
		}

		// shift-click player items into the container
		moved := 0
		for i := range 36 {
			if !hasEmptyContainerSlot() {
				c.Logger.Println("container is full")
				break
			}
			viewIdx := slotCount + i
			playerSlot := inventory.SlotMainStart + i
			item := inv.GetSlot(playerSlot)
			if item == nil || item.IsEmpty() {
				continue
			}
			c.Logger.Printf("storing %s x%d", items.ItemName(item.ID), item.Count)
			if err := inv.ContainerShiftClick(viewIdx); err != nil {
				c.Logger.Printf("  shift-click failed: %v", err)
				continue
			}
			moved++
			time.Sleep(50 * time.Millisecond)
		}

		c.Logger.Printf("stored %d stacks", moved)

		if err := inv.CloseContainer(); err != nil {
			c.Logger.Printf("failed to close container: %v", err)
		} else {
			c.Logger.Println("container closed")
		}
	}

	// trigger storage when items appear in player inventory
	inv.OnSlotUpdate(func(index int, item *items.ItemStack) {
		if index < inventory.SlotMainStart || index >= inventory.SlotHotbarEnd {
			return
		}
		if item == nil || item.IsEmpty() {
			return
		}
		go storeItems()
	})

	// on spawn, wait for chunks then do initial store pass
	s.OnSpawn(func() {
		c.Logger.Println("spawned, waiting for chunks to load...")
		go func() {
			time.Sleep(3 * time.Second)
			storeItems()
		}()
	})

	helpers.Run(c)
}
