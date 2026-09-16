# go-mclib/client

A higher-level framework for building Minecraft (chat)bots with Go.

## Dependency Chain

```markdown
go-mclib/protocol <–––(requires)––– go-mclib/data <–––(requires)––– go-mclib/client
              ^–––––––––––––––––––––––––(requires)––––––––––––––––––––––/
```

## Capabilities / TODO

The SDK provides session-scoped chat, world, inventory, physics, navigation and combat modules. High-level policies such as authorization, bounded jobs and safe construction belong to the consuming bot.

- [x] Connecting to offline/online mode servers and remaining online (keep-alive);
- [x] Reading and sending chat packets, signed command arguments, command discovery and server-backed tab completion;
- [x] Sending simple packets (drop held item, look at specific coordinates, etc...);
- [x] Knowledge about its own health, experience, automatic respawning;
- [x] Loaded-world/chunk observation and block interactions;
- [x] Authoritative inventory and open-container snapshots; stack moves, splits, swaps, sorting, exact drops, hotbar selection and cursor recovery;
- [ ] Menu-specific crafting and automated trade execution;
- [x] Loaded-entity observation, interaction and cooldown-aware combat;
- [x] Vanilla 26.2 collision shapes, stair stepping, fluids, entity pushing and special-block movement;
- [x] Cancellable, streamed-terrain A* navigation with velocity-aware steering and changed-terrain recovery.

Context-aware sends reject stale sessions and bound queue waits. Packet submission alone is not proof of a server-side effect; consumers must observe the appropriate world, inventory or entity confirmation.

Physics uses the vanilla float-rounded dimensions and movement arithmetic, full collision geometry (including 1.5-block fences/walls/closed gates), authoritative velocity packets, and ordered teleport/tick updates. Water/lava currents, swimming, slime/beds, honey, webs, powder snow and bubble columns are modeled. Moving-piston block-entity progress and direct shulker-lid displacement are not modeled; this is not a guarantee of compatibility with every anticheat plugin.

`chat.SuggestCommandContext(ctx, "/pw ")` asks the connected server for completions without executing the command. Preserve trailing spaces; response `Start` and `Length` are UTF-16 offsets into the unchanged input. Results include optional text-component tooltips, are server-specific and non-exhaustive, and do not establish command permission. Requests expire after five seconds or on cancellation, disconnect or transfer.

Applications enforcing their own chat moderation or retention policy should set
`chat.Module.SuppressChatLogs = true` before registering the module. This disables
raw player/system/disguised chat logging without disabling callbacks or signed
message acknowledgements. The application remains responsible for filtering its
own callbacks, context and storage.

Physics tick callbacks run under the client's packet-state boundary. External controllers needing coherent cross-module state may use `Client.WithPacketState`; never call it from packet or physics-tick callbacks, and never wait for a server reply while holding it.

Tracked entity equipment applies both partial `SetEquipment` packets and vanilla's `SWAP_HANDS` entity event (55). Hand swaps preserve unknown versus explicitly empty slots and leave armor unchanged. Fetch a fresh entity snapshot to observe updates; previously returned snapshots remain isolated, point-in-time copies.

`inventory.Module.Snapshot()` returns an independent active-menu view with `WindowID`, `Revision`, `Ready`, slots and cursor. Window zero uses the 46 player protocol slots; open menus use their own slot order followed by player storage where applicable. Use `Execute(ctx, Operation{...})` outside packet/physics callbacks. Supply the observed window and revision to reject stale operations. Each click requests a full server resynchronization and waits for authoritative contents before proceeding; slot counts are never optimistically changed. `confirmed`, `sent`, `partial` and `failed` distinguish observation from submission. Hotbar selection has no vanilla acknowledgement and therefore reports `sent`. Interrupted work is not rolled back; inspect the returned snapshot, recover cursor contents with `From: -1`, and never blindly retry a drop.

Legacy `ContainerClick`, `RightClick`, `ShiftClick` and `SwapToHotbar` also wait for authoritative synchronization. Call them outside dispatch callbacks. `RequestHotbarSwap` and `SelectHotbar` remain nonblocking for queued physics-tick senders; they reject conflicting transactions and fence stale sessions. Generic transactions do not promise crafting/output-slot side effects or unknown menu layouts. Container opening is a separate interaction, not a read of hidden contents.

`interaction.UseBlock` and `interaction.UseEntity` right-click using main/offhand, observed geometry, interaction-range attributes and loaded line of sight. Pass the captured session's send callback; a physics caller can queue packets without waiting for the network. Packet submission is not confirmation: observe a server-opened menu, block change, passenger update or another appropriate effect. Entity interaction uses the vanilla 26.2 interaction record, not the old attack/interact discriminator.

`world.FindBlocks(ctx, BlockQuery{...})` returns nearest loaded matches by block-center distance, exact block names and/or attached wall-sign text. Radius is 1–128 and limit 1–50. `Complete` reports observation coverage; `Truncated` reports omitted known matches, not unknown terrain. `ContainerLabels` follows ordinary wall-sign facing to its supporting container and returns independent front/back text snapshots. Standing/hanging or merely nearby signs do not label containers; sign text is untrusted data. `GetBlockEntity` also returns an owned snapshot.

`interaction.BeginItemUse` submits one main/offhand use request with the current aim; `ReleaseItemUse` submits the vanilla release packet. Consumers own hold duration and cancellation, must preserve begin/release ordering with the captured session transport, and should observe `self.UsingItem`, inventory and other server effects. Cleanup release needs a surviving session context, not a cancelled job context; direct inventory mutation must wait behind it. Holding a bow then releasing can fire; food and drinks can finish naturally. An acknowledgement alone does not prove consumption or firing.

`pathfinding.NavigateToContext` accepts a goal even when its chunk is not loaded. Acceptance is asynchronous: `IsNavigating` includes planning and waiting, and `OnNavigationComplete(true)` means arrival at the ultimate goal, never an intermediate frontier. Bounded background searches retain that goal while newly loaded/changed terrain triggers replanning. Steering brakes for actual velocity and slipperiness, validates supported corridors, and excludes failed edges during recovery. `Stop` cancels planning and input synchronously; a frontier without progress eventually fails rather than walking into unknown terrain. `FindPathContext` remains a complete-loaded-route query. Navigation supports walking, steps/stairs, crouched clearance, bounded descents and simulated cardinal jumps; it does not implement autonomous mining, placing/bridging, or specialized swimming/climbing/elytra route planning.

## Disclaimer

This project is a work in progress. It was made to learn more about the Minecraft protocol, and also because there isn't really a good framework for building Minecraft bots with Go.

## Dependencies

- [`go-mclib/data`](https://github.com/go-mclib/data)
- [`go-mclib/protocol`](https://github.com/go-mclib/protocol)

The current dependency chain also uses the unpublished `github.com/go-mclib/nbt` workspace module. Consumers must supply that module locally; GraceBot retains its existing local replacement.

## Usage

See the [`examples`](./examples) directory for some sample bots/inspiration.

For example, to run the [`afkbot`](./examples/afkbot) example:

```bash
go run examples/afkbot/main.go -s <your_server_ip> -u "<username>"
```

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
