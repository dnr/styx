

styx: */*.go */*/*.go
	go build ./cmd/styx

test:
	go test -v -exec sudo ./tests

gen generate:
	go generate ./...

