.PHONY: sqlc test test-integration

test:
	go test ./...

test-integration:
	go test -tags integration ./test/integration/...

sqlc:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@latest generate
