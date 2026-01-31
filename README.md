# go-mclib/client

A higher-level framework for building Minecraft (chat)bots with Go.

## Dependency Chain

[go-mclib/protocol](https://github.com/go-mclib/protocol) <–––(requires)––– [go-mclib/data](https://github.com/go-mclib/data) <–––(requires)––– **[go-mclib/client](https://github.com/go-mclib/client)**

## Capabilities / TODO

The bot is NOT ready for complex tasks yet. In its current state, it can join a server, remain online and chat.
It can also listen and send any packet, but there are no "smart" features yet. Here's the full list:

- [x] Connecting to offline/online mode servers and remaining online (keep-alive);
- [x] Reading and sending chat packets & signed chat messages;
- [x] Sending simple packets (drop held item, look at specific coordinates, etc...);
- [x] Knowledge about its own health, experience, automatic respawning;
- [ ] Knowledge about the world/chunk data, so that blocks can be placed/broken/interacted with;
- [ ] Inventory/GUI management (move items in inventory, take/store items in containers, crafting...);
- [ ] Knowledge about entities in chunks around the bot, so that they can be attacked/interacted with;
- [ ] Knowledge about block shapes
  - [ ] Movement;
  - [ ] Gravity
- [ ] Smart movement using an algo (A*?);

## Disclaimer

This project is a work in progress. It was made to learn more about the Minecraft protocol, and also because there isn't really a good framework for building Minecraft bots with Go.

## Dependencies

- [`go-mclib/data`](https://github.com/go-mclib/data)
- [`go-mclib/protocol`](https://github.com/go-mclib/protocol)

## Usage

See the [`examples`](./examples) directory for some sample bots/inspiration.

For example, to run the [`paintball`](./examples/paintball) example:

```bash
go run examples/paintball/paintball.go -s <your_server_ip> -u "<username (omit this parameter for Microsoft auth)>"
```

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
