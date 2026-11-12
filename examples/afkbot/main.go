package main

import (
	"flag"

	"github.com/go-mclib/client/botcli"
)

func main() {
	var f botcli.Flags
	botcli.RegisterFlags(&f)
	flag.Parse()

	f.MaxReconnectAttempts = -1

	c := botcli.NewClient(f)
	botcli.Run(c, f)
}
