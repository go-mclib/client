package main

import (
	"flag"

	"github.com/go-mclib/client/pkg/helpers"
)

func main() {
	var f helpers.Flags
	helpers.RegisterFlags(&f)
	flag.Parse()

	f.MaxReconnectAttempts = -1

	c := helpers.NewClient(f)
	helpers.Run(c)
}
