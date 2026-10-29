.PHONY: build test erde

build:
	./_tools/go-inline *.go && go fmt . &&  go build

erde: *.go pm/*.go parse/*.go luagen/*.go cmd/erde/main.go
	./_tools/go-inline *.go && go fmt . && go build ./cmd/erde

test:
	./_tools/go-inline *.go && go fmt . &&  go test ./...
