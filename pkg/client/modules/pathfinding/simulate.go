package pathfinding

import (
	"math"

	"github.com/go-mclib/client/pkg/client/modules/collisions"
	"github.com/go-mclib/client/pkg/client/modules/world"
	"github.com/go-mclib/data/pkg/data/blocks"
)

// physics constants reused from the physics module (unexported there)
const (
	simGravity             = 0.08
	simJumpPower           = float64(float32(0.42))
	simSprintJumpBoost     = 0.2
	simPlayerSpeed         = float64(float32(0.1))
	simSprintModifier      = 0.3
	simFlyingSpeed         = float64(float32(0.02))
	simDefaultFriction     = float64(float32(0.6))
	simAirFrictionMul      = float64(float32(0.91))
	simVerticalFriction    = float64(float32(0.98))
	simInputFriction       = float64(float32(0.98))
	simFrictionSpeedFactor = float64(float32(0.21600002))
	simMaxTicks            = 40
)

// JumpLanding represents where a simulated jump lands.
type JumpLanding struct {
	X, Y, Z int     // block position of landing
	Ticks   int     // ticks from jump to landing
	ExactX  float64 // exact X at landing
	ExactZ  float64 // exact Z at landing
}

// SimulateJumps simulates sprint-jumps in each cardinal direction from the given
// block position and returns the reachable landing positions.
func SimulateJumps(col *collisions.Module, w *world.Module,
	bx, by, bz int,
	jumpPower, effectiveSpeed float64,
) []JumpLanding {
	if jumpPower <= 0 {
		jumpPower = simJumpPower
	}
	if effectiveSpeed <= 0 {
		effectiveSpeed = simPlayerSpeed
	}

	startX := float64(bx) + 0.5
	startY := float64(by)
	startZ := float64(bz) + 0.5

	// cardinal directions: yaw 0=south(+Z), 90=west(-X), 180=north(-Z), 270=east(+X)
	yaws := [4]float64{270, 90, 0, 180} // +X, -X, +Z, -Z

	var landings []JumpLanding
	for _, yaw := range yaws {
		if landing, ok := simulateOneJump(col, w, startX, startY, startZ, yaw, jumpPower, effectiveSpeed); ok {
			landings = append(landings, landing)
		}
	}
	return landings
}

func simulateOneJump(col *collisions.Module, w *world.Module,
	startX, startY, startZ, yaw, jumpPower, effectiveSpeed float64,
) (JumpLanding, bool) {
	x, y, z := startX, startY, startZ
	velX, velY, velZ := 0.0, 0.0, 0.0
	onGround := true

	sprintSpeed := effectiveSpeed * (1.0 + simSprintModifier)

	for tick := range simMaxTicks {
		// jump on first tick
		if tick == 0 && onGround {
			velY = max(jumpPower, velY)
			// sprint-jump boost
			angle := yaw * math.Pi / 180.0
			velX += -math.Sin(angle) * simSprintJumpBoost
			velZ += math.Cos(angle) * simSprintJumpBoost
		}

		// movement threshold zeroing
		if velX*velX+velZ*velZ < 9e-6 {
			velX, velZ = 0, 0
		}
		if math.Abs(velY) < 0.003 {
			velY = 0
		}

		// input: always forward=1, sprint
		forward := simInputFriction // modifyInput: 0.98 * 1.0

		// get block friction
		var blockFriction float64
		if onGround {
			belowState := w.GetBlock(int(math.Floor(x)), int(math.Floor(y-0.5)), int(math.Floor(z)))
			blockFriction = getSimBlockFriction(belowState)
		} else {
			blockFriction = 1.0
		}

		// getFrictionInfluencedSpeed
		var speed float64
		if onGround {
			bf := float32(blockFriction)
			speed = float64(float32(sprintSpeed) * (float32(simFrictionSpeedFactor) / (bf * bf * bf)))
		} else {
			speed = simFlyingSpeed
		}

		// moveRelative (forward only, strafe=0)
		dx, _, dz := simMoveRelative(speed, forward, 0, yaw)
		velX += dx
		velZ += dz

		// collision resolution
		origVelY := velY
		adjX, adjY, adjZ, _, vCol := col.CollideMovement(x, y, z, playerWidth, playerHeight, velX, velY, velZ)

		if vCol {
			velY = 0
		}
		xCollided := math.Abs(velX-adjX) >= 1e-5
		zCollided := math.Abs(velZ-adjZ) >= 1e-5
		if xCollided {
			velX = 0
		}
		if zCollided {
			velZ = 0
		}

		x += adjX
		y += adjY
		z += adjZ

		onGround = vCol && origVelY < 0

		// post-collision physics: gravity + friction
		friction := float64(float32(blockFriction) * float32(simAirFrictionMul))
		velY -= simGravity
		velX *= friction
		velZ *= friction
		velY *= simVerticalFriction

		// check landing (after tick 0)
		if tick > 0 && onGround {
			landX := int(math.Floor(x))
			landY := int(math.Floor(y))
			landZ := int(math.Floor(z))

			// only consider if we've actually moved from the start block
			startBX := int(math.Floor(startX))
			startBZ := int(math.Floor(startZ))
			dist := iabs(landX-startBX) + iabs(landZ-startBZ)
			if dist >= 2 { // at least a 1-block gap
				return JumpLanding{
					X: landX, Y: landY, Z: landZ,
					Ticks:  tick + 1,
					ExactX: x, ExactZ: z,
				}, true
			}
			// landed too close, abort
			return JumpLanding{}, false
		}

		// if we hit a wall horizontally, abort
		if xCollided || zCollided {
			return JumpLanding{}, false
		}
	}

	return JumpLanding{}, false
}

// simMoveRelative computes input vector rotated by yaw (same as physics.moveRelative)
func simMoveRelative(speed, forward, strafe, yaw float64) (dx, dy, dz float64) {
	lengthSq := forward*forward + strafe*strafe
	if lengthSq < 1e-7 {
		return 0, 0, 0
	}
	if lengthSq > 1 {
		invLen := 1.0 / math.Sqrt(lengthSq)
		forward *= invLen
		strafe *= invLen
	}
	forward *= speed
	strafe *= speed
	sinYaw := math.Sin(yaw * math.Pi / 180.0)
	cosYaw := math.Cos(yaw * math.Pi / 180.0)
	dx = strafe*cosYaw - forward*sinYaw
	dz = forward*cosYaw + strafe*sinYaw
	return dx, 0, dz
}

// getSimBlockFriction returns block friction for simulation (mirrors physics.GetBlockFriction)
func getSimBlockFriction(stateID int32) float64 {
	if stateID == 0 {
		return simDefaultFriction
	}
	blockID, _ := blocks.StateProperties(int(stateID))
	name := blocks.BlockName(blockID)
	switch name {
	case "minecraft:ice", "minecraft:packed_ice":
		return float64(float32(0.98))
	case "minecraft:blue_ice":
		return float64(float32(0.989))
	case "minecraft:slime_block":
		return float64(float32(0.8))
	default:
		return simDefaultFriction
	}
}
