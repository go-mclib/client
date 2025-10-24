module github.com/go-mclib/client/examples/chatbot

go 1.25

require (
	github.com/go-mclib/client v0.0.0-20251024063720-b7fefaa913c7
	github.com/go-mclib/data v0.0.0-20251024075824-f24bdb682b63
	github.com/go-mclib/protocol v0.0.0-20251024063549-22172982029e
)

require (
	github.com/SimonMorphy/grok-go v1.0.0
	github.com/Tnze/go-mc v1.20.2 // indirect
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c // indirect
	golang.org/x/sys v0.37.0 // indirect
)

replace github.com/go-mclib/client => ../..
