# go-mclib/client

A higher-level framework for building Minecraft (chat)bots with Go.

## Disclaimer

This project is a work in progress. It was made to learn more about the Minecraft protocol, and also because there isn't really a good framework for building bots with Go.

Since the tears and sweat are real, it is unlikely that I will continue maintaining this before taking a break from it.

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
