BINARY := build/dsmon

.PHONY: build install test test-ui fmt vet clean

build:
	go build -trimpath -o $(BINARY) ./cmd/dsmon

install: build
	install -Dm755 $(BINARY) $(HOME)/.local/bin/dsmon

test:
	go test ./...

test-ui:
	go test ./internal/tui

fmt:
	gofmt -w ./cmd ./internal

vet:
	go vet ./...

clean:
	rm -rf build

