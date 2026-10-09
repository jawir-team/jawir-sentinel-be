.PHONY: sqlc test

test:
	go test ./...

sqlc:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@latest generate
