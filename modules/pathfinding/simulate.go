package pathfinding

import (
	"math"

	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/physics"
	"github.com/go-mclib/client/modules/world"
)

const simMaxTicks = 40

// jumpLanding represents where a simulated jump lands.
type jumpLanding struct {
	X, Y, Z int // block position of landing
	Ticks   int // ticks from jump to landing
}

// simulateJumps simulates sprint-jumps in each cardinal direction from the given
// block position and returns the reachable landing positions.
func simulateJumps(col *collisions.Module, w *world.Module,
	bx, by, bz int,
	jumpPower, effectiveSpeed float64,
) []jumpLanding {
	if jumpPower <= 0 {
		jumpPower = physics.JumpPower
	}
	if effectiveSpeed <= 0 {
		effectiveSpeed = physics.PlayerSpeed
	}

	startX := float64(bx) + 0.5
	startY := float64(by)
	startZ := float64(bz) + 0.5

	// cardinal directions: yaw 0=south(+Z), 90=west(-X), 180=north(-Z), 270=east(+X)
	yaws := [4]float64{270, 90, 0, 180} // +X, -X, +Z, -Z

	var landings []jumpLanding
	for _, yaw := range yaws {
		if landing, ok := simulateOneJump(col, w, startX, startY, startZ, yaw, jumpPower, effectiveSpeed); ok {
			landings = append(landings, landing)
		}
	}
	return landings
}

func simulateOneJump(col *collisions.Module, w *world.Module,
	startX, startY, startZ, yaw, jumpPower, effectiveSpeed float64,
) (jumpLanding, bool) {
	x, y, z := startX, startY, startZ
	velX, velY, velZ := 0.0, 0.0, 0.0
	onGround := true

	sprintSpeed := effectiveSpeed * (1.0 + physics.SprintModifier)

	for tick := range simMaxTicks {
		// jump on first tick
		if tick == 0 && onGround {
			velY = max(jumpPower, velY)
			// sprint-jump boost
			angle := yaw * math.Pi / 180.0
			velX += -math.Sin(angle) * physics.SprintJumpBoost
			velZ += math.Cos(angle) * physics.SprintJumpBoost
		}

		// movement threshold zeroing
		if velX*velX+velZ*velZ < 9e-6 {
			velX, velZ = 0, 0
		}
		if math.Abs(velY) < 0.003 {
			velY = 0
		}

		// input: always forward=1, sprint
		forward := physics.InputFriction // modifyInput: 0.98 * 1.0

		// get block friction
		var blockFriction float64
		if onGround {
			belowState := w.GetBlock(int(math.Floor(x)), int(math.Floor(y-0.5)), int(math.Floor(z)))
			blockFriction = physics.GetBlockFriction(belowState)
		} else {
			blockFriction = 1.0
		}

		// getFrictionInfluencedSpeed
		var speed float64
		if onGround {
			bf := float32(blockFriction)
			speed = float64(float32(sprintSpeed) * (float32(physics.FrictionSpeedFactor) / (bf * bf * bf)))
		} else {
			speed = physics.FlyingSpeed
		}

		// moveRelative (forward only, strafe=0)
		dx, _, dz := physics.MoveRelative(speed, forward, 0, yaw)
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
		friction := float64(float32(blockFriction) * float32(physics.AirFrictionMul))
		velY -= physics.Gravity
		velX *= friction
		velZ *= friction
		velY *= physics.VerticalAirFriction

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
				return jumpLanding{
					X: landX, Y: landY, Z: landZ,
					Ticks: tick + 1,
				}, true
			}
			// landed too close, abort
			return jumpLanding{}, false
		}

		// if we hit a wall horizontally, abort
		if xCollided || zCollided {
			return jumpLanding{}, false
		}
	}

	return jumpLanding{}, false
}
