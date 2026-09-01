.PHONY: build frontend-dependencies generate test verify

build:
	go build -mod=readonly ./...

frontend-dependencies:
	test "$$(node --version)" = "v26.7.0"
	test "$$(npm --version)" = "12.0.2"
	npm ci --ignore-scripts

generate: frontend-dependencies
	go tool templ generate
	go tool sqlc generate --no-remote
	npm run generate:css
	install -m 0644 node_modules/htmx.org/dist/htmx.min.js internal/httpui/static/htmx-2.0.10.min.js

test:
	go test -mod=readonly ./...

verify: generate
	test -z "$$(git diff --name-only -- internal/store/db)"
	test -z "$$(git ls-files --others --exclude-standard -- internal/store/db)"
	test -z "$$(git diff --name-only -- ':(glob)internal/httpui/*_templ.go' ':(glob)internal/httpui/**/*_templ.go' internal/httpui/static)"
	test -z "$$(git ls-files --others --exclude-standard -- ':(glob)internal/httpui/*_templ.go' ':(glob)internal/httpui/**/*_templ.go' internal/httpui/static)"
	test -z "$$(gofmt -l $$(git ls-files --cached --others --exclude-standard -- '*.go'))"
	go vet -mod=readonly ./...
	go test -mod=readonly -race -cover ./...
