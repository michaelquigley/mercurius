TARGETS ?= ./cmd/mercurius

.PHONY: clean build test

clean:
	go clean
	rm -f $(GOPATH)/bin/mercurius

build:
	go install $(TARGETS)

test:
	go test ./... -count=1
	go vet ./...
