
styx: */*.go */*/*.go
	go build ./cmd/styx

gen generate:
	go generate ./...

