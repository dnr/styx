
styx: */*.go */*/*.go
	go build ./cmd/styx

charon: */*.go */*/*.go
	go build ./cmd/charon

spin: cmd/spin/*.go common/cobrautil/*.go
	go build ./cmd/spin

gen generate:
	go generate ./...

