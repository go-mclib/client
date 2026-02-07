# Pathfinding

## Goal

Implement A* pathfinding that accounts for gaps in block hitboxes, danger avoidance, and entity avoidance.

## Available Data

- **Block shapes**: 5,128 unique collision shapes as `[]hitboxes.AABB` (6 floats: MinX/Y/Z, MaxX/Y/Z, block-local 0.0-1.0 coords). Lookup: state ID -> `blocks.CollisionShape(stateID)`.
- **Player dimensions**: width=0.6, height=1.8 (from `hitboxes/entities`).
- **Block access**: `WorldStore.GetBlock(x, y, z)` -> state ID.
- **Block classification**: `blocks.HasCollision()`, `blocks.IsFullBlock()`.

## Algorithm: Block-Grid A* with Sub-Block Collision Checks

Keep A* on the integer block grid, but replace binary passability checks with AABB-aware checks.

### Node Evaluation ("Can the player exist here?")

Instead of "does this block have collision?", ask: "Is there a position within this block's XZ footprint where the player's 0.6x1.8x0.6 AABB doesn't collide with any surrounding block shapes?"

Fast path (covers ~95% of blocks):

- `!HasCollision(stateID)` -> PASSABLE (air, flowers, etc.)
- `IsFullBlock(stateID)` -> BLOCKED (stone, dirt, etc.)
- Otherwise -> scan for gap (partial blocks only: stairs, walls, fences, trapdoors, etc.)

Gap scan: iterate candidate player positions within the block (player center from bx+0.3 to bx+0.7 in ~0.05 increments on X and Z), check if the player's AABB at that position intersects any world-space block collision AABBs from surrounding blocks.

AABB intersection = 6 float comparisons, very fast.

### Edge Evaluation ("Can the player move between nodes?")

- Both nodes FULLY_PASSABLE -> valid, no sweep needed.
- Either involves a partial block -> sweep the player AABB along the movement axis in small increments and check collisions.

### Danger Costs (A* edge weights)

| Block/Condition          | Cost Modifier                          |
|--------------------------|----------------------------------------|
| Normal block             | 1.0                                    |
| Magma block (below feet) | +100 or infinite                       |
| Lava nearby              | +50 per adjacent                       |
| Cactus                   | +100                                   |
| Entity within N blocks   | +20 per entity (infinite for hostiles) |
| Berry bush               | +5                                     |
| Powder snow              | +20                                    |
| Soul sand                | +3 (slows movement)                    |

## Performance Estimate

For a 100-block path (~10,000 nodes explored):

- Full/empty fast path (95%): negligible
- Partial block scans (5%): 500 x ~81 positions x ~9 blocks x ~3 AABBs = ~1M float comparisons -> ~1-2ms
- Edge sweeps (rare): <0.5ms
- **Total: ~2-4ms**

Even 500+ block paths should be under 20ms.

## Implementation Steps

1. Add AABB helper methods (`Intersects()`, `Offset()`, `Expand()`) on `hitboxes.AABB` in `go-mclib/data`.
2. Implement `canPlayerFitAt(world, x, y, z) bool` - the core collision check replacing binary passability.
3. Implement standard A* on the block grid with the new passability function.
4. Add danger cost map - lookup from block state ID to cost modifier.
5. Add entity avoidance - query nearby entities during pathfinding, inflate positions into cost penalties.

## Prerequisites

- Entity tracking (need to know where entities are for avoidance).
- Module system (pathfinding should be a module).

## References

- Block shapes: `go-mclib/data/pkg/data/hitboxes/blocks/block_shapes_gen.go`
- Entity dimensions: `go-mclib/data/pkg/data/hitboxes/entities/entity_hitboxes_gen.go`
- AABB type: `go-mclib/data/pkg/data/hitboxes/hitboxes.go`
- World access: `client/world_store.go`
- Movement: `client/world_helpers.go`
