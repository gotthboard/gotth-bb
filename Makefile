.PHONY: build generate test verify

build:
	go build -mod=readonly ./...

generate:
	go tool sqlc generate --no-remote

test:
	go test -mod=readonly ./...

verify:
	go tool sqlc generate --no-remote
	test -z "$$(git diff --name-only -- internal/store/db)"
	test -z "$$(git ls-files --others --exclude-standard -- internal/store/db)"
	test -z "$$(gofmt -l $$(git ls-files --cached --others --exclude-standard -- '*.go'))"
	go vet -mod=readonly ./...
	go test -mod=readonly -race -cover ./...
