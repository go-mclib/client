package client

import (
	"log"

	jp "github.com/go-mclib/protocol/java_protocol"
)

type Client struct {
	*jp.TCPClient
	Handlers      []Handler
	Logger        *log.Logger
	OutgoingQueue chan *jp.Packet
}

func NewClient(serverAddr string) *Client {
	return &Client{
		TCPClient:     jp.NewTCPClient(),
		OutgoingQueue: make(chan *jp.Packet, 100),
	}
}

func (c *Client) Run() {
	go func() {
		for pkt := range c.OutgoingQueue {
			err := c.WritePacket(pkt)
			if err != nil {
				c.Logger.Println("error writing packet", pkt, "from queue:", err)
			}
		}
	}()

	for {
		pkt, err := c.ReadPacket()
		if err != nil {
			c.Logger.Println("error reading packet:", err)
			continue
		}

		for _, handler := range c.Handlers {
			handler(c, pkt)
		}
	}
}

func (c *Client) AddHandler(handler Handler) {
	c.Handlers = append(c.Handlers, handler)
}
