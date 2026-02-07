# CLAUDE.md

The `go-mclib/client` package provides a high-level Go implementation of the Minecraft: Java Edition client. It is built on top of the `go-mclib/data` (bindings and helpers for game data, such as looking up protocol IDs for stuff, creating item stacks, determining block shapes, etc.) and `go-mclib/protocol` (bindings for the network wire data and primitive data structures) packages.

## Folders

- `./client` - the main package for high-level client implementation;
- `./examples` - example bots and scripts;
- `./chat` - helpers for handling and working with signed chat messages;
- `./minecraft_source` - (optional) symlinked Minecraft: Java Edition source code (from `go-mclib/data`). Use this as a source of truth for how the official client works. Our implementation should always match the official client logic (minus Java specifics) as best as possible.
- `./tui` - terminal user interface for the client, for interactive mode (sending chat messages & commands on behalf of the bot, as it is running);

The Minecraft wiki (<https://minecraft.wiki/w/Java_Edition_protocol>) is also a great resource, however it is not always up to date. If in doubt, check the Minecraft source code (if available; if not, recommend user to symlink/copy it, see <https://github.com/go-mclib/data/tree/main/decompiled>).

## Code Style

- comments that are not part of symbol or package docstrings should start with lowercase letter, unless naming an exported symbol;
- comments should be minimal;
- comments should only document parts of the code that are less obvious;
- tests should be written in separate packages, ending with `_test`;
- after done, format the code with `go fmt ./...`;
- keep the API minimal, do not keep legacy code, unused methods, etc.
- only keep what is used "in the moment", however, do let the user know after making an important breaking change;
- do not assume cross-version compatibility, we will only support the latest protocol version of Minecraft;
- assume modern Go 1.25+ API, do not use older APIs;
