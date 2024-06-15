
styx: */*.go */*/*.go
	go build ./cmd/styx

charon: cmd/charon/*.go ci/*.go
	go build ./cmd/charon

gen generate:
	go generate ./...

