.DEFAULT_GOAL := build
GOBIN ?= $(shell go env GOPATH)/bin

.PHONY: clean build test push

clean:
	go clean
	rm -f $(GOPATH)/bin/mercurius

build:
	go install ./...

test:
	go test ./... -count=1
	go vet ./...

push:
	push vendor ${GOBIN}/mercurius mercurius