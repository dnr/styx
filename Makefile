

styx: */*.go */*/*.go
	go build ./cmd/styx

# Be careful, this can lock up the cachefiles subsystem
testlocal:
	go test -v -exec sudo ./tests

testvm:
	# Tests require internet access (for now) so disable sandbox.
	# Need to run nix-build with sudo to allow disabling sandbox.
	sudo nix-build --no-out-link --option sandbox false testvm.nix

gen generate:
	go generate ./...

