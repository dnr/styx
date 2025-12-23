
styx: */*.go */*/*.go
	go build ./cmd/styx

charon: */*.go */*/*.go
	go build ./cmd/charon

gen generate:
	go generate ./...

