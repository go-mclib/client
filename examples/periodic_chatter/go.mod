module github.com/go-mclib/client/examples/commands

go 1.25

require (
	github.com/conneroisu/groq-go v0.9.5
	github.com/go-mclib/client v0.0.0-20251024063720-b7fefaa913c7
	github.com/go-mclib/data v0.0.0-20251024063138-811f53e053f0
	github.com/go-mclib/protocol v0.0.0-20251024062106-9e1063000339
)

require (
	github.com/Tnze/go-mc v1.20.2 // indirect
	github.com/buger/jsonparser v1.1.1 // indirect
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c // indirect
	golang.org/x/sys v0.37.0 // indirect
)

replace github.com/go-mclib/client => ../..
