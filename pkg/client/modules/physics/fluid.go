package physics

import (
	"math"
	"strconv"

	"github.com/go-mclib/client/pkg/client/modules/world"
	"github.com/go-mclib/data/pkg/data/blocks"
	block_shapes "github.com/go-mclib/data/pkg/data/hitboxes/blocks"
)

const (
	WaterFlowScale = 0.014
	LavaFlowScale  = 0.007
)

// applyFluidPushing applies flow forces from water/lava currents.
// Called before travel() in the tick, matching Entity.baseTick() in vanilla.
func (m *Module) applyFluidPushing(x, y, z float64, w *world.Module) {
	hw := PlayerWidth / 2
	// deflate AABB by 0.001 as MC does
	minX := x - hw + 0.001
	minY := y + 0.001
	minZ := z - hw + 0.001
	maxX := x + hw - 0.001
	maxY := y + PlayerHeight - 0.001
	maxZ := z + hw - 0.001

	var totalX, totalY, totalZ float64
	var count int
	var isLavaFlow bool

	for bx := int(math.Floor(minX)); bx <= int(math.Floor(maxX)); bx++ {
		for by := int(math.Floor(minY)); by <= int(math.Floor(maxY)); by++ {
			for bz := int(math.Floor(minZ)); bz <= int(math.Floor(maxZ)); bz++ {
				stateID := w.GetBlock(bx, by, bz)
				if stateID == 0 {
					continue
				}
				blockID, props := blocks.StateProperties(int(stateID))
				if blockID != waterBlockID && blockID != lavaBlockID {
					continue
				}

				level := parseLevel(props["level"])
				amount := fluidAmount(level)
				fluidHeight := float64(amount) / 9.0

				// check if entity is submerged in this fluid block
				fluidTop := float64(by) + fluidHeight
				submerge := fluidTop - minY
				if submerge <= 0 {
					continue
				}

				fx, fy, fz := getFluidFlow(w, blockID, bx, by, bz)
				if fx == 0 && fy == 0 && fz == 0 {
					continue
				}

				// scale by submersion if < 0.4
				if submerge < 0.4 {
					fx *= submerge
					fy *= submerge
					fz *= submerge
				}

				totalX += fx
				totalY += fy
				totalZ += fz
				count++

				if blockID == lavaBlockID {
					isLavaFlow = true
				}
			}
		}
	}

	if count == 0 {
		return
	}

	totalX /= float64(count)
	totalY /= float64(count)
	totalZ /= float64(count)

	// for players, MC keeps the magnitude (doesn't normalize)
	scale := WaterFlowScale
	if isLavaFlow {
		scale = LavaFlowScale
	}

	totalX *= scale
	totalY *= scale
	totalZ *= scale

	m.VelX += totalX
	m.VelY += totalY
	m.VelZ += totalZ
}

// getFluidFlow computes the flow direction at a fluid block.
// Matches FlowingFluid.getFlow() from vanilla.
func getFluidFlow(w *world.Module, fluidBlockID int32, bx, by, bz int) (flowX, flowY, flowZ float64) {
	stateID := w.GetBlock(bx, by, bz)
	_, props := blocks.StateProperties(int(stateID))
	level := parseLevel(props["level"])

	falling := level >= 8
	effectiveLevel := level
	if falling {
		effectiveLevel -= 8
	}
	currentHeight := float64(fluidAmount(effectiveLevel)) / 9.0

	dirs := [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}
	for _, dir := range dirs {
		nx, nz := bx+dir[0], bz+dir[1]
		neighborState := w.GetBlock(nx, by, nz)
		neighborID, neighborProps := blocks.StateProperties(int(neighborState))

		var distance float64
		if neighborID == fluidBlockID {
			nLevel := parseLevel(neighborProps["level"])
			nEffective := nLevel
			if nLevel >= 8 {
				nEffective -= 8
			}
			neighborHeight := float64(fluidAmount(nEffective)) / 9.0
			distance = currentHeight - neighborHeight
		} else if !block_shapes.HasCollision(neighborState) {
			// non-solid block: check below for fluid flowing down
			belowState := w.GetBlock(nx, by-1, nz)
			belowID, belowProps := blocks.StateProperties(int(belowState))
			if belowID == fluidBlockID {
				bLevel := parseLevel(belowProps["level"])
				bEffective := bLevel
				if bLevel >= 8 {
					bEffective -= 8
				}
				belowHeight := float64(fluidAmount(bEffective)) / 9.0
				distance = currentHeight - (belowHeight - 8.0/9.0)
			}
		}

		flowX += float64(dir[0]) * distance
		flowZ += float64(dir[1]) * distance
	}

	// falling water: add downward flow if adjacent to a solid face
	if falling {
		for _, dir := range dirs {
			nx, nz := bx+dir[0], bz+dir[1]
			neighborState := w.GetBlock(nx, by, nz)
			aboveState := w.GetBlock(nx, by+1, nz)
			if block_shapes.HasCollision(neighborState) || block_shapes.HasCollision(aboveState) {
				flowY = -6.0
				break
			}
		}
	}

	// normalize
	length := math.Sqrt(flowX*flowX + flowY*flowY + flowZ*flowZ)
	if length > 0 {
		flowX /= length
		flowY /= length
		flowZ /= length
	}

	return
}

// fluidAmount converts a level (0-15) to an amount (1-8).
func fluidAmount(level int) int {
	if level <= 0 {
		return 8 // source block
	}
	if level > 7 {
		return 1
	}
	return 8 - level
}

func parseLevel(s string) int {
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
