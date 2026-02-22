package pathfinding

import (
	"strings"

	"github.com/go-mclib/client/pkg/client/modules/world"
	"github.com/go-mclib/data/pkg/data/blocks"
)

// isWoodenDoor returns true if the block is a door that can be opened by hand
// (excludes iron doors which require redstone).
func isWoodenDoor(stateID int32) bool {
	if stateID == 0 {
		return false
	}
	blockID, _ := blocks.StateProperties(int(stateID))
	name := blocks.BlockName(blockID)
	if !strings.HasSuffix(name, "_door") {
		return false
	}
	return name != "minecraft:iron_door"
}

// isDoorOpen checks if a door block state has open=true.
func isDoorOpen(stateID int32) bool {
	_, props := blocks.StateProperties(int(stateID))
	return props["open"] == "true"
}

// isDoorLowerHalf returns true if this is the lower half of a door.
func isDoorLowerHalf(stateID int32) bool {
	_, props := blocks.StateProperties(int(stateID))
	return props["half"] == "lower"
}

// findClosedWoodenDoor checks if there's a closed wooden door blocking passage
// at the given position (checks both y and y+1 for the 2-tall door).
// Returns the door block coordinates and true if found.
func findClosedWoodenDoor(w *world.Module, x, y, z int) (doorX, doorY, doorZ int, found bool) {
	// check feet level
	feetState := w.GetBlock(x, y, z)
	if isWoodenDoor(feetState) && !isDoorOpen(feetState) {
		if isDoorLowerHalf(feetState) {
			return x, y, z, true
		}
		// upper half — door base is one below
		return x, y - 1, z, true
	}

	// check head level
	headState := w.GetBlock(x, y+1, z)
	if isWoodenDoor(headState) && !isDoorOpen(headState) {
		if isDoorLowerHalf(headState) {
			return x, y + 1, z, true
		}
		return x, y, z, true
	}

	return 0, 0, 0, false
}
