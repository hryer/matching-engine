.PHONY: test race run replay smoke stop

test:
	go test ./...

race:
	go test ./... -race -count=10

run:
	go run ./cmd/server

replay:
	go test ./internal/engine -run TestDeterministicReplay -count=1000

smoke:
	./scripts/smoke.sh

stop:
	@lsof -ti:8080 | xargs -r kill -TERM 2>/dev/null || true
	@sleep 0.3
	@lsof -ti:8080 | xargs -r kill -KILL 2>/dev/null || true
	@echo "port 8080 freed"
