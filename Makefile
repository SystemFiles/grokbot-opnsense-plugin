.PHONY: build test vet
build:
	./scripts/build.sh
test: vet
	go test ./...
vet:
	go vet ./...
