module github.com/go-mclib/client/examples/commands

go 1.24.5

require (
	github.com/conneroisu/groq-go v0.9.5
	github.com/go-mclib/client v0.0.0-20250823175525-addda36f77ef
	github.com/go-mclib/data v0.0.0-20250820060749-8dfa569f68f4
	github.com/go-mclib/protocol v0.0.0-20250819111155-0e3a23dc2054
)

require (
	github.com/Tnze/go-mc v1.20.2 // indirect
	github.com/buger/jsonparser v1.1.1 // indirect
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c // indirect
	golang.org/x/sys v0.35.0 // indirect
)

replace github.com/go-mclib/client => ../..
