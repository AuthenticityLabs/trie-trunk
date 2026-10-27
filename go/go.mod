module trie-trunk/go

go 1.24

require (
	github.com/coder/websocket v1.8.15
	github.com/fxamacker/cbor/v2 v2.9.2
	github.com/pelletier/go-toml/v2 v2.2.3
	github.com/quic-go/quic-go v0.59.1
	github.com/x448/float16 v0.8.4
	golang.org/x/sys v0.35.0
)

require (
	github.com/quic-go/qpack v0.6.0 // indirect
	golang.org/x/crypto v0.41.0 // indirect
	golang.org/x/net v0.43.0 // indirect
	golang.org/x/text v0.28.0 // indirect
)

require github.com/AuthenticityLabs/pill-packer/go v0.0.0

replace github.com/AuthenticityLabs/pill-packer/go => ../../pill-packer/go
