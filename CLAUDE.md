# CLAUDE.md

`go-mclib/client` provides a high-level Go implementation of the Minecraft: Java Edition client, built on `go-mclib/data` and `go-mclib/protocol`.

## Folders

- `./client` — main client implementation
- `./examples` — example bots and scripts
- `./chat` — signed chat message helpers
- `./minecraft_source` — (optional) symlinked Minecraft source from `go-mclib/data`; our implementation should match official client logic
- `./tui` — terminal UI for interactive mode (chat & commands)

The [Minecraft wiki](https://minecraft.wiki/w/Java_Edition_protocol) is useful but may be outdated. Prefer Minecraft source when available (see <https://github.com/go-mclib/data/tree/main/decompiled>).

Only the latest Minecraft protocol version is supported.
