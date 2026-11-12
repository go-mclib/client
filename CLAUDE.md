# CLAUDE.md

`go-mclib/client` provides a high-level Go implementation of the Minecraft: Java Edition client, built on `go-mclib/data` and `go-mclib/protocol`.

## Layout

- module root (`package client`) — the `Client` core: connection/reconnect loop, login/configuration handshake, connection-scoped `Session`s, module system, auth bootstrap, swarm, `Simulator` for tests
- `./modules` — pluggable client modules, one package each (protocol, self, world, physics, collisions, pathfinding, entities, inventory, interaction, chat, playerlist, registry, combat, command_block_core, voicechat)
- `./modules/chat/internal/signing` — signed chat: message signing and last-seen tracking
- `./botcli` — CLI flag wiring, default module registration and the interactive TUI for bots
- `./internal/tui` — terminal UI for interactive mode (chat & commands)
- `./examples` — example bots and scripts
- `./minecraft_source` — (optional) symlinked decompiled Minecraft source from `go-mclib/mcsrc`; our implementation should match official client logic

The [Minecraft wiki](https://minecraft.wiki/w/Java_Edition_protocol) is useful but may be outdated. Prefer Minecraft source when available (see <https://github.com/go-mclib/mcsrc>).

Only the latest Minecraft protocol version is supported.
