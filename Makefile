.PHONY: sqlc

sqlc:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@latest generate
