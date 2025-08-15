package client

import (
	jp "github.com/go-mclib/protocol/java_protocol"
)

type Handler func(c *Client, pkt *jp.Packet)

