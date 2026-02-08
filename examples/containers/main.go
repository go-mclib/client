package main

import (
	"flag"
	"math"
	"sync"
	"time"

	"github.com/go-mclib/client/pkg/client/modules/collisions"
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
	blocks.BlockID("minecraft:white_shulker_box"),
	blocks.BlockID("minecraft:orange_shulker_box"),
	blocks.BlockID("minecraft:magenta_shulker_box"),
	blocks.BlockID("minecraft:light_blue_shulker_box"),
	blocks.BlockID("minecraft:yellow_shulker_box"),
	blocks.BlockID("minecraft:lime_shulker_box"),
	blocks.BlockID("minecraft:pink_shulker_box"),
	blocks.BlockID("minecraft:gray_shulker_box"),
	blocks.BlockID("minecraft:light_gray_shulker_box"),
	blocks.BlockID("minecraft:cyan_shulker_box"),
	blocks.BlockID("minecraft:purple_shulker_box"),
	blocks.BlockID("minecraft:blue_shulker_box"),
	blocks.BlockID("minecraft:brown_shulker_box"),
	blocks.BlockID("minecraft:green_shulker_box"),
	blocks.BlockID("minecraft:red_shulker_box"),
	blocks.BlockID("minecraft:black_shulker_box"),
}

// barrels can always be opened regardless of blocks above
var barrelBlockID = blocks.BlockID("minecraft:barrel")

const blockReach = 4.5

// findReachableWithLOS searches for a standable position within reach of (bx, by, bz)
// that has line-of-sight to the target block center. Returns the closest one.
func findReachableWithLOS(w *world.Module, col *collisions.Module, bx, by, bz int) (int, int, int, bool) {
	targetX := float64(bx) + 0.5
	targetY := float64(by) + 0.5
	targetZ := float64(bz) + 0.5

	r := int(math.Ceil(blockReach))
	bestDist := math.MaxFloat64
	var bestX, bestY, bestZ int
	found := false

	for dx := -r; dx <= r; dx++ {
		for dz := -r; dz <= r; dz++ {
			for dy := -r; dy <= r; dy++ {
				nx, ny, nz := bx+dx, by+dy, bz+dz
				below := w.GetBlock(nx, ny-1, nz)
				feet := w.GetBlock(nx, ny, nz)
				head := w.GetBlock(nx, ny+1, nz)
				if !blockHitboxes.HasCollision(below) || blockHitboxes.HasCollision(feet) || blockHitboxes.HasCollision(head) {
					continue
				}
				eyeX := float64(nx) + 0.5
				eyeY := float64(ny) + self.EyeHeight
				eyeZ := float64(nz) + 0.5
				ddx := eyeX - targetX
				ddy := eyeY - targetY
				ddz := eyeZ - targetZ
				dist := math.Sqrt(ddx*ddx + ddy*ddy + ddz*ddz)
				if dist > blockReach {
					continue
				}
				if dist >= bestDist {
					continue
				}
				if col != nil {
					hit, _, _, _ := col.RaycastBlocks(eyeX, eyeY, eyeZ, targetX, targetY, targetZ)
					if hit {
						continue
					}
				}
				bestDist = dist
				bestX, bestY, bestZ = nx, ny, nz
				found = true
			}
		}
	}
	return bestX, bestY, bestZ, found
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
	col := collisions.From(c)

	var mu sync.Mutex
	var storing bool

	findNearestContainer := func() (int, int, int, bool) {
		px, py, pz := float64(s.X), float64(s.Y), float64(s.Z)
		bestDist := math.MaxFloat64
		var bx, by, bz int
		found := false

		w.FindBlocks(containerBlockIDs, func(x, y, z int, stateID int32) bool {
			// chests and shulker boxes can't open with a full block above; barrels are fine
			blockID, _ := blocks.StateProperties(int(stateID))
			if blockID != barrelBlockID && blockHitboxes.IsFullBlock(w.GetBlock(x, y+1, z)) {
				return true
			}
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

		// find a reachable position with LOS to the container
		adjX, adjY, adjZ, adjFound := findReachableWithLOS(w, col, cx, cy, cz)
		if !adjFound {
			c.Logger.Println("no reachable block with line-of-sight to container")
			return
		}

		// navigate there if too far
		dx := float64(adjX) + 0.5 - float64(s.X)
		dz := float64(adjZ) + 0.5 - float64(s.Z)
		if math.Sqrt(dx*dx+dz*dz) > 1.0 {
			done := make(chan bool, 1)
			pf.OnNavigationComplete(func(reached bool) {
				done <- reached
			})
			if err := pf.NavigateTo(float64(adjX)+0.5, float64(adjY), float64(adjZ)+0.5); err != nil {
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
