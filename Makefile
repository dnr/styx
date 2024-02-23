

styx: */*.go */*/*.go
	go build ./cmd/styx

generate:
	go generate ./...

