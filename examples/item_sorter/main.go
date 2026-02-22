package main

import (
	"encoding/json"
	"flag"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-mclib/client/pkg/client"
	"github.com/go-mclib/client/pkg/client/modules/collisions"
	"github.com/go-mclib/client/pkg/client/modules/entities"
	"github.com/go-mclib/client/pkg/client/modules/inventory"
	"github.com/go-mclib/client/pkg/client/modules/pathfinding"
	"github.com/go-mclib/client/pkg/client/modules/self"
	"github.com/go-mclib/client/pkg/client/modules/world"
	"github.com/go-mclib/client/pkg/helpers"
	"github.com/go-mclib/data/pkg/data/blocks"
	dataEntities "github.com/go-mclib/data/pkg/data/entities"
	"github.com/go-mclib/data/pkg/data/items"
	ns "github.com/go-mclib/protocol/java_protocol/net_structures"
	"github.com/go-mclib/protocol/nbt"
)

var containerBlockIDs = []int32{
	blocks.BlockID("minecraft:chest"),
	blocks.BlockID("minecraft:trapped_chest"),
	blocks.BlockID("minecraft:barrel"),
}

// customCategories maps category names to item lists for use on signs.
// Reference these with a : prefix (e.g. ":food", ":valuables").
var customCategories = map[string][]string{
	"food": {
		"minecraft:cooked_beef",
		"minecraft:cooked_porkchop",
		"minecraft:cooked_chicken",
		"minecraft:cooked_mutton",
		"minecraft:cooked_salmon",
		"minecraft:cooked_cod",
		"minecraft:bread",
		"minecraft:baked_potato",
		"minecraft:golden_carrot",
		"minecraft:apple",
	},
	"valuables": {
		"minecraft:diamond",
		"minecraft:diamond_block",
		"minecraft:emerald_block",
		"minecraft:spawner",
		"minecraft:trial_spawner",
		"minecraft:beacon",
		"minecraft:netherite_ingot",
		"minecraft:netherite_block",
		"minecraft:elytra",
		"minecraft:heavy_core",
		"minecraft:elytra",
		"minecraft:vault",
		"minecraft:reinforced_deepslate",
		"minecraft:ender_chest",
		"minecraft:golden_apple",
		"minecraft:enchanted_golden_apple",
	},
}

// foodItemNames lists items the bot may eat, ordered by preference (best first).
// Food items are never deposited into chests — they stay in the bot's inventory.
var foodItemNames = []string{
	"minecraft:cooked_porkchop",
	"minecraft:golden_carrot",
}

var (
	foodItemIDs        []int32
	resolvedCategories map[string][]int32
	wallSignBlockIDs   = map[int32]bool{}
)

const (
	signBlockEntityType   = 7
	hangingSignEntityType = 8
	blockReach            = 4.5
	scanRadius            = 128
	filterSignText        = "filter me"
	trashSignText         = "trash"
	filterDebounce        = 3 * time.Second
	itemPollInterval      = 200 * time.Millisecond
	rebuildInterval       = 10 * time.Second
	hungerThreshold       = 18 // food level (0-20) below which the bot pauses to eat
)

func init() {
	woodTypes := []string{
		"oak", "spruce", "birch", "jungle", "acacia", "dark_oak",
		"mangrove", "cherry", "bamboo", "crimson", "warped", "pale_oak",
	}
	for _, wood := range woodTypes {
		wallSignBlockIDs[blocks.BlockID("minecraft:"+wood+"_wall_sign")] = true
		wallSignBlockIDs[blocks.BlockID("minecraft:"+wood+"_wall_hanging_sign")] = true
	}
	for _, name := range foodItemNames {
		if id := items.ItemID(name); id >= 0 {
			foodItemIDs = append(foodItemIDs, id)
		}
	}
	resolvedCategories = make(map[string][]int32, len(customCategories))
	for cat, names := range customCategories {
		for _, name := range names {
			if id := items.ItemID(name); id >= 0 {
				resolvedCategories[cat] = append(resolvedCategories[cat], id)
			}
		}
	}
}

type blockPos struct{ x, y, z int }

// sorter scans for labeled chests and automatically sorts items into them.
//
// Chests labeled with item frames or signs map items to destinations.
// Chests labeled with a "filter me" sign are input chests — the bot camps
// at them, waits for items to arrive, takes everything, then deposits each
// item type into the matching labeled chest before returning.
// A chest labeled "trash" receives all items that have no other destination.
type sorter struct {
	c    *client.Client
	inv  *inventory.Module
	s    *self.Module
	w    *world.Module
	pf   *pathfinding.Module
	col  *collisions.Module
	ents *entities.Module

	mu           sync.Mutex
	labelMap     map[int32]blockPos // item ID -> destination chest
	filterChests []blockPos         // "filter me" input chests
	trashChest   *blockPos          // "trash" chest for unlabeled items

	navCh       chan bool     // current navigation result channel
	containerCh chan struct{} // current container-open signal channel
	closeCh     chan struct{} // server-initiated container close
	expectClose bool          // true when close is client-initiated

	trigger chan struct{} // buffered signal to attempt sorting
}

func newSorter(c *client.Client) *sorter {
	return &sorter{
		c:        c,
		inv:      inventory.From(c),
		s:        self.From(c),
		w:        world.From(c),
		pf:       pathfinding.From(c),
		col:      collisions.From(c),
		ents:     entities.From(c),
		labelMap: make(map[int32]blockPos),
		trigger:  make(chan struct{}, 1),
		closeCh:  make(chan struct{}, 1),
	}
}

// --- core operations ---

func (sr *sorter) navigateTo(x, y, z float64, timeout time.Duration) bool {
	ch := make(chan bool, 1)
	sr.mu.Lock()
	sr.navCh = ch
	sr.mu.Unlock()
	defer func() {
		sr.mu.Lock()
		sr.navCh = nil
		sr.mu.Unlock()
	}()

	if err := sr.pf.NavigateTo(x, y, z); err != nil {
		sr.c.Logger.Printf("pathfinding error: %v", err)
		return false
	}

	select {
	case reached := <-ch:
		return reached
	case <-time.After(timeout):
		sr.pf.Stop()
		sr.c.Logger.Println("navigation timed out")
		return false
	}
}

// openChest navigates to a reachable position and interacts with the chest.
func (sr *sorter) openChest(pos blockPos) bool {
	standX, standY, standZ, found := pathfinding.FindReachablePosition(sr.col, float64(sr.s.X), float64(sr.s.Y), float64(sr.s.Z), pos.x, pos.y, pos.z, blockReach)
	if !found {
		sr.c.Logger.Printf("no reachable position for chest at %d,%d,%d", pos.x, pos.y, pos.z)
		return false
	}

	// navigate if not already close enough
	dx := float64(standX) + 0.5 - float64(sr.s.X)
	dz := float64(standZ) + 0.5 - float64(sr.s.Z)
	if math.Sqrt(dx*dx+dz*dz) > 1.0 {
		if !sr.navigateTo(float64(standX)+0.5, float64(standY), float64(standZ)+0.5, 30*time.Second) {
			return false
		}
	}

	sr.pf.Stop()
	time.Sleep(200 * time.Millisecond)

	return sr.interactChest(pos)
}

// interactChest looks at a chest and opens it (assumes already in range).
func (sr *sorter) interactChest(pos blockPos) bool {
	if sr.inv.ContainerOpen() {
		sr.closeContainer()
		time.Sleep(100 * time.Millisecond)
	}

	sr.s.LookAt(float64(pos.x)+0.5, float64(pos.y)+0.5, float64(pos.z)+0.5)
	time.Sleep(50 * time.Millisecond)

	ch := make(chan struct{}, 1)
	sr.mu.Lock()
	sr.containerCh = ch
	sr.mu.Unlock()
	defer func() {
		sr.mu.Lock()
		sr.containerCh = nil
		sr.mu.Unlock()
	}()

	if err := sr.c.InteractBlock(pos.x, pos.y, pos.z, 1, 0, 0.5, 0.5, 0.5); err != nil {
		sr.c.Logger.Printf("interact failed: %v", err)
		return false
	}

	select {
	case <-ch:
		time.Sleep(100 * time.Millisecond)
		return true
	case <-time.After(3 * time.Second):
		sr.c.Logger.Printf("chest open timed out at %d,%d,%d", pos.x, pos.y, pos.z)
		return false
	}
}

func (sr *sorter) closeContainer() {
	if !sr.inv.ContainerOpen() {
		return
	}
	sr.mu.Lock()
	sr.expectClose = true
	sr.mu.Unlock()
	_ = sr.inv.CloseContainer()
}

// drainCloseCh clears any stale server-close signals.
func (sr *sorter) drainCloseCh() {
	select {
	case <-sr.closeCh:
	default:
	}
}

// sleepOrClose sleeps for d, returning false if the container is closed by the server.
func (sr *sorter) sleepOrClose(d time.Duration) bool {
	select {
	case <-time.After(d):
		return sr.inv.ContainerOpen()
	case <-sr.closeCh:
		return false
	}
}

// waitForItems blocks until the open container has items, or it's closed by the server.
// Periodically rebuilds the label map so runtime sign changes are picked up.
func (sr *sorter) waitForItems() bool {
	sr.drainCloseCh()
	pollTicker := time.NewTicker(itemPollInterval)
	defer pollTicker.Stop()
	rebuildTicker := time.NewTicker(rebuildInterval)
	defer rebuildTicker.Stop()

	for {
		if !sr.inv.ContainerOpen() {
			return false
		}
		if sr.containerItemCount() > 0 {
			return true
		}
		select {
		case <-pollTicker.C:
		case <-rebuildTicker.C:
			sr.buildLabelMap()
		case <-sr.closeCh:
			return false
		}
	}
}

// debounceItems waits until container item count stabilizes for filterDebounce.
func (sr *sorter) debounceItems() bool {
	for {
		prev := sr.containerItemCount()
		if !sr.sleepOrClose(filterDebounce) {
			return false
		}
		if sr.containerItemCount() == prev {
			return true
		}
	}
}

// depositItem stores all stacks of itemID into the open container.
// Returns (moved, full): moved is how many stacks were stored,
// full is true if the chest ran out of space before all stacks were stored.
func (sr *sorter) depositItem(itemID int32) (moved int, full bool) {
	slotCount := sr.inv.ContainerSlotCount()
	for i := range 36 {
		if !sr.inv.ContainerOpen() {
			break
		}
		item := sr.inv.GetSlot(inventory.SlotMainStart + i)
		if item == nil || item.IsEmpty() || item.ID != itemID {
			continue
		}
		if !sr.containerHasSpace(itemID) {
			sr.c.Logger.Println("chest is full")
			return moved, true
		}
		viewIdx := slotCount + i
		sr.c.Logger.Printf("  storing %s x%d", items.ItemName(item.ID), item.Count)
		if err := sr.inv.ContainerShiftClick(viewIdx); err != nil {
			sr.c.Logger.Printf("  shift-click failed: %v", err)
			continue
		}
		moved++
		time.Sleep(50 * time.Millisecond)
	}
	return moved, false
}

func (sr *sorter) containerHasSpace(itemID int32) bool {
	for i := range sr.inv.ContainerSlotCount() {
		cs := sr.inv.ContainerSlot(i)
		if cs == nil || cs.IsEmpty() {
			return true
		}
		if cs.ID == itemID && cs.Components != nil && cs.Count < cs.Components.MaxStackSize {
			return true
		}
	}
	return false
}

func (sr *sorter) takeAllFromContainer() int {
	taken := 0
	for i := range sr.inv.ContainerSlotCount() {
		if !sr.inv.ContainerOpen() {
			break
		}
		cs := sr.inv.ContainerSlot(i)
		if cs == nil || cs.IsEmpty() {
			continue
		}
		if err := sr.inv.ContainerShiftClick(i); err != nil {
			sr.c.Logger.Printf("shift-click failed: %v", err)
			continue
		}
		taken++
		time.Sleep(50 * time.Millisecond)
	}
	return taken
}

func (sr *sorter) containerItemCount() int {
	count := 0
	for i := range sr.inv.ContainerSlotCount() {
		cs := sr.inv.ContainerSlot(i)
		if cs != nil && !cs.IsEmpty() {
			count += int(cs.Count)
		}
	}
	return count
}

func (sr *sorter) eatIfHungry() {
	if len(foodItemIDs) == 0 {
		return
	}
	for int32(sr.s.Food) < hungerThreshold {
		sr.c.Logger.Printf("hungry (food=%d), eating...", sr.s.Food)
		if err := sr.s.Eat(foodItemIDs); err != nil {
			sr.c.Logger.Printf("failed to eat: %v", err)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// --- main loop ---

// run is the main sorter loop. It deposits any sortable inventory items,
// then cycles through filter chests — camping at each until items arrive,
// taking them, and depositing before moving to the next.
// If no filter chests exist, it waits for inventory triggers.
func (sr *sorter) run() {
	for {
		sr.eatIfHungry()
		sr.depositAll()

		sr.mu.Lock()
		filters := slices.Clone(sr.filterChests)
		sr.mu.Unlock()

		if len(filters) > 0 {
			for _, pos := range filters {
				sr.processFilterChest(pos)
				sr.buildLabelMap()
				sr.eatIfHungry()
				sr.depositAll()
			}
		} else {
			// no filter chests — wait for items to appear in inventory
			select {
			case <-sr.trigger:
			case <-time.After(10 * time.Second):
			}
		}
	}
}

func (sr *sorter) requestSort() {
	select {
	case sr.trigger <- struct{}{}:
	default:
	}
}

// processFilterChest opens a filter chest, waits for items (camping with the
// window open), debounces, and takes everything.
func (sr *sorter) processFilterChest(pos blockPos) {
	if !sr.openChest(pos) {
		return
	}

	if !sr.waitForItems() {
		sr.c.Logger.Printf("filter chest at %d,%d,%d closed", pos.x, pos.y, pos.z)
		return
	}

	if !sr.debounceItems() {
		return
	}

	taken := sr.takeAllFromContainer()
	if taken > 0 {
		sr.c.Logger.Printf("collected %d stacks from filter chest at %d,%d,%d", taken, pos.x, pos.y, pos.z)
	}
	sr.closeContainer()
}

// depositAll uses greedy grouped navigation: finds a position covering the
// most destination chests, navigates there once, deposits into all reachable
// chests by rotating, then repeats for remaining chests.
// Items that don't fit (chest full) are redirected to the trash chest.
func (sr *sorter) depositAll() {
	groups := sr.groupSortableItems()
	if len(groups) == 0 {
		return
	}

	sr.mu.Lock()
	trashChest := sr.trashChest
	sr.mu.Unlock()

	// track item IDs that overflowed from full chests
	var overflowIDs []int32

	// collect unique chest positions (exclude trash — handled separately)
	remaining := make(map[blockPos]bool, len(groups))
	for pos := range groups {
		if trashChest != nil && pos == *trashChest {
			continue // trash is deposited last
		}
		remaining[pos] = true
	}

	for len(remaining) > 0 {
		var targets [][3]int
		for pos := range remaining {
			targets = append(targets, [3]int{pos.x, pos.y, pos.z})
		}

		standX, standY, standZ, reachable, found := pathfinding.FindBestReachPosition(
			sr.col, float64(sr.s.X), float64(sr.s.Y), float64(sr.s.Z), targets, blockReach,
		)
		if !found {
			sr.c.Logger.Println("no reachable position for remaining chests")
			break
		}

		sr.c.Logger.Printf("navigating to %d,%d,%d to reach %d chest(s)", standX, standY, standZ, len(reachable))

		dx := float64(standX) + 0.5 - float64(sr.s.X)
		dz := float64(standZ) + 0.5 - float64(sr.s.Z)
		if math.Sqrt(dx*dx+dz*dz) > 1.0 {
			if !sr.navigateTo(float64(standX)+0.5, float64(standY), float64(standZ)+0.5, 30*time.Second) {
				break
			}
		}
		sr.pf.Stop()
		time.Sleep(200 * time.Millisecond)

		for _, t := range reachable {
			pos := blockPos{t[0], t[1], t[2]}
			itemIDs := groups[pos]
			if len(itemIDs) == 0 {
				delete(remaining, pos)
				continue
			}
			if !sr.interactChest(pos) {
				continue
			}
			for _, id := range itemIDs {
				moved, full := sr.depositItem(id)
				if moved > 0 {
					sr.c.Logger.Printf("stored %d stacks of %s", moved, items.ItemName(id))
				}
				if full {
					overflowIDs = append(overflowIDs, id)
				}
				if !sr.inv.ContainerOpen() {
					break
				}
			}
			sr.closeContainer()
			time.Sleep(200 * time.Millisecond)
			delete(remaining, pos)
		}

		for _, t := range reachable {
			delete(remaining, blockPos{t[0], t[1], t[2]})
		}
	}

	// deposit overflow + originally-trash items into the trash chest
	if trashChest == nil {
		return
	}
	trashIDs := groups[*trashChest]
	trashIDs = append(trashIDs, overflowIDs...)
	if len(trashIDs) == 0 {
		return
	}
	// deduplicate
	seen := make(map[int32]bool)
	var uniqueTrash []int32
	for _, id := range trashIDs {
		if !seen[id] {
			seen[id] = true
			uniqueTrash = append(uniqueTrash, id)
		}
	}
	if !sr.openChest(*trashChest) {
		return
	}
	for _, id := range uniqueTrash {
		moved, _ := sr.depositItem(id)
		if moved > 0 {
			sr.c.Logger.Printf("trashed %d stacks of %s", moved, items.ItemName(id))
		}
		if !sr.inv.ContainerOpen() {
			break
		}
	}
	sr.closeContainer()
}

// groupSortableItems returns items in the player inventory grouped by their
// destination chest. Each chest maps to a deduplicated list of item IDs.
func (sr *sorter) groupSortableItems() map[blockPos][]int32 {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	groups := make(map[blockPos][]int32)
	seen := make(map[int32]bool)

	for i := range 36 {
		item := sr.inv.GetSlot(inventory.SlotMainStart + i)
		if item == nil || item.IsEmpty() || seen[item.ID] {
			continue
		}
		if slices.Contains(foodItemIDs, item.ID) {
			continue
		}
		seen[item.ID] = true
		if pos, ok := sr.labelMap[item.ID]; ok {
			groups[pos] = append(groups[pos], item.ID)
		} else if sr.trashChest != nil {
			groups[*sr.trashChest] = append(groups[*sr.trashChest], item.ID)
		}
	}
	return groups
}

// --- label map ---

// buildLabelMap scans nearby item frames and signs within scanRadius to map
// items to destination chests. Uses individual GetBlock calls instead of
// FindBlocks to avoid holding the world RLock for long durations.
func (sr *sorter) buildLabelMap() {
	cx := int(math.Floor(float64(sr.s.X)))
	cy := int(math.Floor(float64(sr.s.Y)))
	cz := int(math.Floor(float64(sr.s.Z)))

	labelMap := make(map[int32]blockPos)
	var filterChests []blockPos
	var trashChest *blockPos

	// scan item frames within radius
	for _, typeID := range []int32{dataEntities.ItemFrame, dataEntities.GlowItemFrame} {
		for _, e := range sr.ents.GetEntitiesByType(typeID) {
			ex, ey, ez := int(math.Floor(e.X)), int(math.Floor(e.Y)), int(math.Floor(e.Z))
			if intAbs(ex-cx) > scanRadius || intAbs(ey-cy) > scanRadius || intAbs(ez-cz) > scanRadius {
				continue
			}
			itemData := e.Metadata.Get(dataEntities.ItemFrameIndexItem)
			if itemData == nil {
				continue
			}
			stack, err := items.ReadSlot(ns.NewReader(itemData))
			if err != nil || stack.IsEmpty() {
				continue
			}
			if pos, ok := findContainerNear(sr.w, ex, ey, ez); ok {
				labelMap[stack.ID] = pos
				sr.c.Logger.Printf("label: %s -> %d,%d,%d (item frame)", items.ItemName(stack.ID), pos.x, pos.y, pos.z)
			}
		}
	}

	// scan blocks within radius for signs (individual GetBlock calls to
	// avoid holding the world RLock for the entire chunk set)
	minY := max(cy-scanRadius, -64)
	maxY := min(cy+scanRadius, 319)
	for x := cx - scanRadius; x <= cx+scanRadius; x++ {
		for z := cz - scanRadius; z <= cz+scanRadius; z++ {
			for y := minY; y <= maxY; y++ {
				stateID := sr.w.GetBlock(x, y, z)
				if stateID == 0 {
					continue
				}
				blockID, _ := blocks.StateProperties(int(stateID))
				if !wallSignBlockIDs[blockID] {
					continue
				}
				sr.processSignAt(x, y, z, stateID, labelMap, &filterChests, &trashChest)
			}
		}
	}

	sr.mu.Lock()
	sr.labelMap = labelMap
	sr.filterChests = filterChests
	sr.trashChest = trashChest
	sr.mu.Unlock()

	sr.c.Logger.Printf("labels: %d items, %d filter chests, trash=%v", len(labelMap), len(filterChests), trashChest != nil)
}

func (sr *sorter) processSignAt(x, y, z int, stateID int32, labelMap map[int32]blockPos, filterChests *[]blockPos, trashChest **blockPos) {
	be := sr.w.GetBlockEntity(x, y, z)
	if be == nil || (be.Type != signBlockEntityType && be.Type != hangingSignEntityType) {
		return
	}
	lines := extractSignText(be.Data)
	if len(lines) == 0 {
		return
	}

	pos, found := findContainerForSign(sr.w, x, y, z, stateID)
	if !found {
		return
	}

	// special keywords take priority over item labels
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.EqualFold(trimmed, filterSignText) {
			*filterChests = append(*filterChests, pos)
			sr.c.Logger.Printf("filter chest at %d,%d,%d", pos.x, pos.y, pos.z)
			return
		}
		if strings.EqualFold(trimmed, trashSignText) {
			*trashChest = &pos
			sr.c.Logger.Printf("trash chest at %d,%d,%d", pos.x, pos.y, pos.z)
			return
		}
	}

	for _, line := range lines {
		for _, itemID := range resolveLabel(line) {
			labelMap[itemID] = pos
			sr.c.Logger.Printf("label: %s -> %d,%d,%d (sign)", items.ItemName(itemID), pos.x, pos.y, pos.z)
		}
	}
}

// --- callbacks ---

func (sr *sorter) setup() {
	sr.pf.OnNavigationComplete(func(reached bool) {
		sr.mu.Lock()
		ch := sr.navCh
		sr.mu.Unlock()
		if ch != nil {
			select {
			case ch <- reached:
			default:
			}
		}
	})

	sr.inv.OnContainerOpen(func(_ int32, _ inventory.MenuType, _ string) {
		sr.mu.Lock()
		ch := sr.containerCh
		sr.mu.Unlock()
		if ch != nil {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	})

	// only signal closeCh for server-initiated closes
	sr.inv.OnContainerClose(func() {
		sr.mu.Lock()
		if sr.expectClose {
			sr.expectClose = false
			sr.mu.Unlock()
			return
		}
		sr.mu.Unlock()
		select {
		case sr.closeCh <- struct{}{}:
		default:
		}
	})

	sr.inv.OnSlotUpdate(func(index int, item *items.ItemStack) {
		if index < inventory.SlotMainStart || index >= inventory.SlotHotbarEnd {
			return
		}
		if item == nil || item.IsEmpty() {
			return
		}
		sr.mu.Lock()
		_, labeled := sr.labelMap[item.ID]
		hasTrash := sr.trashChest != nil
		sr.mu.Unlock()
		if labeled || hasTrash {
			sr.requestSort()
		}
	})

	sr.s.OnSpawn(func() {
		sr.c.Logger.Println("spawned, waiting for chunks...")
		go func() {
			time.Sleep(5 * time.Second)
			sr.buildLabelMap()
			sr.requestSort()
		}()
	})
}

// --- pure helpers ---

func isContainer(blockID int32) bool {
	return slices.Contains(containerBlockIDs, blockID)
}

func findContainerNear(w *world.Module, x, y, z int) (blockPos, bool) {
	stateID := w.GetBlock(x, y, z)
	blockID, _ := blocks.StateProperties(int(stateID))
	if isContainer(blockID) {
		return blockPos{x, y, z}, true
	}
	return findAdjacentContainer(w, x, y, z)
}

func findAdjacentContainer(w *world.Module, x, y, z int) (blockPos, bool) {
	for _, off := range [][3]int{{1, 0, 0}, {-1, 0, 0}, {0, 0, 1}, {0, 0, -1}, {0, 1, 0}, {0, -1, 0}} {
		nx, ny, nz := x+off[0], y+off[1], z+off[2]
		stateID := w.GetBlock(nx, ny, nz)
		if stateID == 0 {
			continue
		}
		blockID, _ := blocks.StateProperties(int(stateID))
		if isContainer(blockID) {
			return blockPos{nx, ny, nz}, true
		}
	}
	return blockPos{}, false
}

func findContainerForSign(w *world.Module, x, y, z int, stateID int32) (blockPos, bool) {
	blockID, props := blocks.StateProperties(int(stateID))
	if wallSignBlockIDs[blockID] {
		dx, dy, dz := wallSignFacingOffset(props["facing"])
		cx, cy, cz := x+dx, y+dy, z+dz
		checkBlockID, _ := blocks.StateProperties(int(w.GetBlock(cx, cy, cz)))
		if isContainer(checkBlockID) {
			return blockPos{cx, cy, cz}, true
		}
	}
	return findAdjacentContainer(w, x, y, z)
}

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
		var tc ns.TextComponent
		if json.Unmarshal([]byte(text), &tc) == nil {
			text = tc.String()
		}
		text = strings.TrimSpace(text)
		if text != "" {
			lines = append(lines, text)
		}
	}
	return lines
}

// resolveLabel resolves a sign line to item IDs.
// Lines starting with # are treated as item tags (e.g. "#bundles", "#minecraft:swords").
// Other lines are treated as individual item names.
func resolveLabel(line string) []int32 {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}

	if strings.HasPrefix(line, "#") {
		tag := strings.ToLower(line[1:])
		if !strings.Contains(tag, ":") {
			tag = "minecraft:" + tag
		}
		return items.ItemTag(tag)
	}

	if strings.HasPrefix(line, ":") {
		return resolvedCategories[strings.ToLower(line[1:])]
	}

	name := strings.ToLower(line)
	if !strings.Contains(name, ":") {
		name = "minecraft:" + name
	}
	if id := items.ItemID(name); id >= 0 {
		return []int32{id}
	}
	return nil
}

func intAbs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func main() {
	var f helpers.Flags
	helpers.RegisterFlags(&f)
	flag.Parse()

	c := helpers.NewClient(f)
	c.MaxReconnectAttempts = -1
	c.Register(entities.New())
	c.Register(pathfinding.New())
	c.Register(inventory.New())

	sr := newSorter(c)
	sr.setup()
	go sr.run()

	helpers.Run(c)
}
