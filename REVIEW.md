# Go Code Review: github.com/go-mclib/client

**Scope**: entire module
**Go version**: 1.25
**Files reviewed**: 109

## Simplification Tradeoffs

These proposals require relaxing a requirement or restructuring boundaries. Each includes what changes so you can make an informed decision.

| Location | Complexity | Proposed Simplification | What Changes | Estimated Gain |
|----------|-----------|------------------------|--------------|----------------|
| actions.go:1-100; modules/self/actions.go:26-48,67-101; modules/interaction/ | Player actions are sent from four places with different guarantees: root `Client` raw writers, self raw writers (`Move`/`MoveRelative` bypass physics last-sent tracking, `Use`/`UseAt` bypass `BeginItemUse` validation), validated interaction (session-bound `send`) and inventory transactions. `self.MoveRelative` shares a name with unrelated `physics.MoveRelative`. | Make `interaction` the single home for world/entity actions; delete root actions.go and self's raw senders (`Move`, `MoveRelative`, `Rotate`, `Use`, `UseAt`), keeping `LookAt`/`SetRotation`/`Respawn`. | Done: root actions.go deleted (raw helpers in `interaction`, `inventory.SetCreativeSlot`), `self.Use`/`UseAt`/`Eat` removed (`interaction.BeginItemUse`/`interaction.Eat`). **Declined:** `self.Move`/`MoveRelative`/`Rotate` are kept — nothing else provides raw movement/rotation packets, so removing them would drop a user-facing feature. | ~150 lines removed; action entry points go from 4 to 2. |
