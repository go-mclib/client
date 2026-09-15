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
- [x] Inventory snapshots, selected-slot swaps and block placement with server confirmation support;
- [ ] General-purpose container automation and crafting;
- [x] Loaded-entity observation, interaction and cooldown-aware combat;
- [x] Vanilla 26.2 collision shapes, stair stepping, fluids, entity pushing and special-block movement;
- [x] Cancellable A* pathfinding in loaded terrain.

Context-aware sends reject stale sessions and bound queue waits. Packet submission alone is not proof of a server-side effect; consumers must observe the appropriate world, inventory or entity confirmation.

Physics uses the vanilla float-rounded dimensions and movement arithmetic, full collision geometry (including 1.5-block fences/walls/closed gates), authoritative velocity packets, and ordered teleport/tick updates. Water/lava currents, swimming, slime/beds, honey, webs, powder snow and bubble columns are modeled. Moving-piston block-entity progress and direct shulker-lid displacement are not modeled; this is not a guarantee of compatibility with every anticheat plugin.

`chat.SuggestCommandContext(ctx, "/pw ")` asks the connected server for completions without executing the command. Preserve trailing spaces; response `Start` and `Length` are UTF-16 offsets into the unchanged input. Results include optional text-component tooltips, are server-specific and non-exhaustive, and do not establish command permission. Requests expire after five seconds or on cancellation, disconnect or transfer.

Physics tick callbacks run under the client's packet-state boundary. External controllers needing coherent cross-module state may use `Client.WithPacketState`; never call it from packet or physics-tick callbacks, and never wait for a server reply while holding it.

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
