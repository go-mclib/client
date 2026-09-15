package pathfinding

import (
	"context"
	"math"

	"github.com/go-mclib/client/modules/collisions"
	"github.com/go-mclib/client/modules/physics"
	"github.com/go-mclib/client/modules/world"
	"github.com/go-mclib/data/blocks"
)

const simMaxTicks = 40

type jumpLanding struct {
	X, Y, Z int
	Ticks   int
}

// simulateJumps uses the live travel primitives and collision resolver. Contextual
// surfaces not represented by a dry-air trajectory are deliberately not jump edges.
func simulateJumps(ctx context.Context, col *collisions.Module, w *world.Module,
	bx, by, bz int, cfg simulationConfig,
) []jumpLanding {
	startY, ok := standSurface(col, bx, by, bz, cfg.height, cfg)
	if !ok || !nodeLoaded(w, bx, by, bz, cfg) {
		return nil
	}
	var landings []jumpLanding
	for _, yaw := range [4]float64{270, 90, 0, 180} {
		if ctx.Err() != nil {
			return nil
		}
		if landing, ok := simulateOneJump(ctx, col, w, float64(bx)+0.5, startY, float64(bz)+0.5, yaw, cfg); ok {
			landings = append(landings, landing)
		}
	}
	return landings
}

func simulateOneJump(ctx context.Context, col *collisions.Module, w *world.Module,
	startX, startY, startZ, yaw float64, cfg simulationConfig,
) (jumpLanding, bool) {
	x, y, z := startX, startY, startZ
	vx, vy, vz := 0.0, 0.0, 0.0
	onGround := col.IsOnGround(x, y, z, cfg.width)
	if !onGround || !jumpTerrain(w, x, y, z, x, y, z, cfg) {
		return jumpLanding{}, false
	}
	forward, strafe := physics.ProcessInput(1, 0, 1, 1)
	for tick := range simMaxTicks {
		if ctx.Err() != nil {
			return jumpLanding{}, false
		}
		if vx*vx+vz*vz < 9e-6 {
			vx, vz = 0, 0
		}
		if math.Abs(vy) < 0.003 {
			vy = 0
		}
		if tick == 0 {
			jx, jy, jz := physics.JumpImpulse(yaw, cfg.jumpPower, true)
			vx, vy, vz = vx+jx, math.Max(vy, jy), vz+jz
		}
		friction := 1.0
		speed := physics.SprintFlyingSpeed
		if onGround {
			friction = physics.ModifiedFriction(physics.GetBlockFrictionAt(w, x, y, z, cfg.width), cfg.frictionModifier)
			speed = physics.GroundAcceleration(cfg.speed, friction)
		}
		ax, _, az := physics.MoveRelative(speed, forward, strafe, yaw)
		vx, vz = vx+ax, vz+az
		if !jumpTerrain(w, x, y, z, x+vx, y+vy, z+vz, cfg) {
			return jumpLanding{}, false
		}
		dx, dy, dz, _, vertical := col.CollideMovement(x, y, z, cfg.width, cfg.height, vx, vy, vz, onGround, cfg.maxStep)
		xBlocked, zBlocked := vx != dx, vz != dz
		onGround = vertical && vy < 0
		if vertical {
			vy = 0
		}
		if xBlocked {
			vx = 0
		}
		if zBlocked {
			vz = 0
		}
		x, y, z = x+dx, y+dy, z+dz
		vx, vy, vz = physics.AirVelocity(vx, vy, vz, friction, cfg.gravity, cfg.airDragModifier)
		if tick > 0 && onGround {
			bx, by, bz := int(math.Floor(x)), int(math.Floor(y)), int(math.Floor(z))
			if bx == int(math.Floor(startX)) && bz == int(math.Floor(startZ)) {
				return jumpLanding{}, false
			}
			fy, ok := standSurface(col, bx, by, bz, cfg.height, cfg)
			if !ok || math.Abs(fy-y) > collisions.Epsilon || !nodeLoaded(w, bx, by, bz, cfg) {
				return jumpLanding{}, false
			}
			return jumpLanding{X: bx, Y: by, Z: bz, Ticks: tick + 1}, true
		}
		if (xBlocked || zBlocked) && vy <= 0 {
			return jumpLanding{}, false
		}
	}
	return jumpLanding{}, false
}

// Include the entire swept body and support origins, not only endpoint columns:
// skipping an unloaded chunk or a fluid between two known endpoints is unsafe.
func jumpTerrain(w *world.Module, x, y, z, nx, ny, nz float64, cfg simulationConfig) bool {
	half := cfg.width / 2
	for bx := int(math.Floor(math.Min(x, nx) - half)); bx <= int(math.Floor(math.Max(x, nx)+half)); bx++ {
		for bz := int(math.Floor(math.Min(z, nz) - half)); bz <= int(math.Floor(math.Max(z, nz)+half)); bz++ {
			for by := int(math.Floor(math.Min(y, ny))) - 1; by <= int(math.Floor(math.Max(y, ny)+cfg.height+cfg.maxStep)); by++ {
				state, loaded := w.GetLoadedBlock(bx, by, bz)
				if !loaded {
					return false
				}
				if blockDangerCost(state) >= 50 {
					return false
				}
				id, props := blocks.StateProperties(int(state))
				if props["waterlogged"] == "true" {
					return false
				}
				switch blocks.BlockName(id) {
				case "minecraft:water", "minecraft:lava", "minecraft:bubble_column",
					"minecraft:slime_block", "minecraft:honey_block", "minecraft:cobweb",
					"minecraft:soul_sand", "minecraft:sweet_berry_bush", "minecraft:powder_snow",
					"minecraft:scaffolding", "minecraft:ladder", "minecraft:vine",
					"minecraft:weeping_vines", "minecraft:weeping_vines_plant",
					"minecraft:twisting_vines", "minecraft:twisting_vines_plant":
					return false
				}
			}
		}
	}
	return true
}
