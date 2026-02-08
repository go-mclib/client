package main

import (
	"flag"
	"math"
	"strings"
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
	dataEntities "github.com/go-mclib/data/pkg/data/entities"
	blockHitboxes "github.com/go-mclib/data/pkg/data/hitboxes/blocks"
	"github.com/go-mclib/data/pkg/data/items"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
	"github.com/go-mclib/protocol/nbt"
)

// container block IDs
var containerBlockIDs = []int32{
	blocks.BlockID("minecraft:chest"),
	blocks.BlockID("minecraft:trapped_chest"),
	blocks.BlockID("minecraft:barrel"),
}

// sign block IDs for wall signs (all wood types)
var wallSignBlockIDs = map[int32]bool{}

// signBlockEntityTypes covers both regular signs and hanging signs
const (
	signBlockEntityType   = 7
	hangingSignEntityType = 8
)

func init() {
	// populate wall sign block IDs for all wood types
	woodTypes := []string{
		"oak", "spruce", "birch", "jungle", "acacia", "dark_oak",
		"mangrove", "cherry", "bamboo", "crimson", "warped", "pale_oak",
	}
	for _, wood := range woodTypes {
		wallSignBlockIDs[blocks.BlockID("minecraft:"+wood+"_wall_sign")] = true
		wallSignBlockIDs[blocks.BlockID("minecraft:"+wood+"_wall_hanging_sign")] = true
	}
}

// labelEntry maps an item to the chest that stores it
type labelEntry struct {
	chestX, chestY, chestZ int
}

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

// isContainer checks if a block ID is a container
func isContainer(blockID int32) bool {
	for _, id := range containerBlockIDs {
		if id == blockID {
			return true
		}
	}
	return false
}

// findAdjacentContainer looks for a container block adjacent to (x, y, z).
func findAdjacentContainer(w *world.Module, x, y, z int) (int, int, int, bool) {
	offsets := [][3]int{{1, 0, 0}, {-1, 0, 0}, {0, 0, 1}, {0, 0, -1}, {0, 1, 0}, {0, -1, 0}}
	for _, off := range offsets {
		nx, ny, nz := x+off[0], y+off[1], z+off[2]
		stateID := w.GetBlock(nx, ny, nz)
		if stateID == 0 {
			continue
		}
		blockID, _ := blocks.StateProperties(int(stateID))
		if isContainer(blockID) {
			return nx, ny, nz, true
		}
	}
	return 0, 0, 0, false
}

// wallSignFacingOffset returns the block offset from a wall sign to the block
// it's attached to, based on the sign's facing property.
func wallSignFacingOffset(facing string) (int, int, int) {
	switch facing {
	case "south":
		return 0, 0, -1
	case "north":
		return 0, 0, 1
	case "east":
		return -1, 0, 0
	case "west":
		return 1, 0, 0
	default:
		return 0, 0, 0
	}
}

// extractSignText extracts plain text lines from a sign's block entity data.
func extractSignText(data nbt.Compound) []string {
	frontText := data.GetCompound("front_text")
	if frontText == nil {
		return nil
	}
	messages := frontText.GetList("messages")
	var lines []string
	for _, msg := range messages.Elements {
		var text string
		switch v := msg.(type) {
		case nbt.String:
			text = string(v)
		case nbt.Compound:
			text = v.GetString("text")
		}
		text = strings.TrimSpace(text)
		if text != "" {
			lines = append(lines, text)
		}
	}
	return lines
}

// resolveItemName tries to resolve an item name string to an item protocol ID.
// Accepts "minecraft:iron_ingot", "iron_ingot", or display names with underscores.
func resolveItemName(name string) int32 {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return -1
	}
	// try with namespace
	if !strings.Contains(name, ":") {
		name = "minecraft:" + name
	}
	return items.ItemID(name)
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
	ents := entities.From(c)

	var mu sync.Mutex
	var sorting bool
	labelMap := make(map[int32]*labelEntry) // item ID -> chest position

	// buildLabelMap scans item frames and signs to map items to chests.
	buildLabelMap := func() {
		mu.Lock()
		defer mu.Unlock()

		labelMap = make(map[int32]*labelEntry)

		// scan item frames for displayed items
		frameTypes := []int32{dataEntities.ItemFrame, dataEntities.GlowItemFrame}
		for _, typeID := range frameTypes {
			for _, e := range ents.GetEntitiesByType(typeID) {
				itemData := e.Metadata.Get(dataEntities.ItemFrameIndexItem)
				if itemData == nil {
					continue
				}
				buf := ns.NewReader(itemData)
				stack, err := items.ReadSlot(buf)
				if err != nil || stack.IsEmpty() {
					continue
				}

				// the block the frame is attached to is at floor(entity pos)
				bx := int(math.Floor(e.X))
				by := int(math.Floor(e.Y))
				bz := int(math.Floor(e.Z))

				// check if this block or an adjacent block is a container
				stateID := w.GetBlock(bx, by, bz)
				blockID, _ := blocks.StateProperties(int(stateID))
				if isContainer(blockID) {
					labelMap[stack.ID] = &labelEntry{bx, by, bz}
					c.Logger.Printf("label: %s -> chest at %d,%d,%d (item frame)", items.ItemName(stack.ID), bx, by, bz)
				} else if cx, cy, cz, found := findAdjacentContainer(w, bx, by, bz); found {
					labelMap[stack.ID] = &labelEntry{cx, cy, cz}
					c.Logger.Printf("label: %s -> chest at %d,%d,%d (item frame adjacent)", items.ItemName(stack.ID), cx, cy, cz)
				}
			}
		}

		// scan block entities for signs
		w.FindBlocks(func() []int32 {
			var ids []int32
			for id := range wallSignBlockIDs {
				ids = append(ids, id)
			}
			return ids
		}(), func(x, y, z int, stateID int32) bool {
			be := w.GetBlockEntity(x, y, z)
			if be == nil {
				return true
			}
			if be.Type != signBlockEntityType && be.Type != hangingSignEntityType {
				return true
			}

			lines := extractSignText(be.Data)
			if len(lines) == 0 {
				return true
			}

			// determine the container behind the sign
			blockID, props := blocks.StateProperties(int(stateID))
			var cx, cy, cz int
			var found bool
			if wallSignBlockIDs[blockID] {
				// wall sign: use facing property
				facing := props["facing"]
				dx, dy, dz := wallSignFacingOffset(facing)
				cx, cy, cz = x+dx, y+dy, z+dz
				checkID := w.GetBlock(cx, cy, cz)
				checkBlockID, _ := blocks.StateProperties(int(checkID))
				found = isContainer(checkBlockID)
			}
			if !found {
				// fallback: check adjacent blocks for a container
				cx, cy, cz, found = findAdjacentContainer(w, x, y, z)
			}
			if !found {
				return true
			}

			for _, line := range lines {
				itemID := resolveItemName(line)
				if itemID >= 0 {
					labelMap[itemID] = &labelEntry{cx, cy, cz}
					c.Logger.Printf("label: %s -> chest at %d,%d,%d (sign)", items.ItemName(itemID), cx, cy, cz)
				}
			}
			return true
		})

		c.Logger.Printf("label map built: %d items mapped to chests", len(labelMap))
	}

	// storeItem navigates to the labeled chest and stores all matching items.
	storeItem := func(targetItemID int32, entry *labelEntry) {
		mu.Lock()
		if sorting {
			mu.Unlock()
			return
		}
		sorting = true
		mu.Unlock()

		defer func() {
			mu.Lock()
			sorting = false
			mu.Unlock()
		}()

		cx, cy, cz := entry.chestX, entry.chestY, entry.chestZ
		c.Logger.Printf("storing %s at chest %d,%d,%d", items.ItemName(targetItemID), cx, cy, cz)

		// find a reachable position with LOS to the chest
		adjX, adjY, adjZ, adjFound := findReachableWithLOS(w, col, cx, cy, cz)
		if !adjFound {
			c.Logger.Println("no reachable block with line-of-sight to chest")
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
				c.Logger.Println("could not reach chest")
				return
			}
			time.Sleep(200 * time.Millisecond)
		}

		_ = s.LookAt(float64(cx)+0.5, float64(cy)+0.5, float64(cz)+0.5)
		time.Sleep(50 * time.Millisecond)

		if err := c.InteractBlock(cx, cy, cz, 1, 0, 0.5, 0.5, 0.5); err != nil {
			c.Logger.Printf("failed to interact with chest: %v", err)
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
			c.Logger.Println("timed out waiting for chest to open")
			return
		}
		time.Sleep(100 * time.Millisecond)

		slotCount := inv.ContainerSlotCount()

		// shift-click matching items from player inventory into the container
		moved := 0
		for i := range 36 {
			playerSlot := inventory.SlotMainStart + i
			item := inv.GetSlot(playerSlot)
			if item == nil || item.IsEmpty() || item.ID != targetItemID {
				continue
			}
			// check container has space
			hasSpace := false
			for j := range slotCount {
				cs := inv.ContainerSlot(j)
				if cs == nil || cs.IsEmpty() {
					hasSpace = true
					break
				}
			}
			if !hasSpace {
				c.Logger.Println("chest is full")
				break
			}
			viewIdx := slotCount + i
			c.Logger.Printf("  storing %s x%d", items.ItemName(item.ID), item.Count)
			if err := inv.ContainerShiftClick(viewIdx); err != nil {
				c.Logger.Printf("  shift-click failed: %v", err)
				continue
			}
			moved++
			time.Sleep(50 * time.Millisecond)
		}

		c.Logger.Printf("stored %d stacks of %s", moved, items.ItemName(targetItemID))

		if err := inv.CloseContainer(); err != nil {
			c.Logger.Printf("failed to close container: %v", err)
		}
	}

	// trigger sorting when items appear in player inventory
	inv.OnSlotUpdate(func(index int, item *items.ItemStack) {
		if index < inventory.SlotMainStart || index >= inventory.SlotHotbarEnd {
			return
		}
		if item == nil || item.IsEmpty() {
			return
		}

		mu.Lock()
		entry := labelMap[item.ID]
		mu.Unlock()

		if entry == nil {
			return
		}
		go storeItem(item.ID, entry)
	})

	// on spawn, wait for chunks then build label map
	s.OnSpawn(func() {
		c.Logger.Println("spawned, waiting for chunks to load...")
		go func() {
			time.Sleep(5 * time.Second)
			buildLabelMap()
		}()
	})

	helpers.Run(c)
}
