.PHONY: build test up down dev
build:
	npm ci
	npm run build
	go build -o bin/secretary ./cmd/secretary
test:
	go test -race ./...
	npm test
up:
	python3 scripts/init-env.py
	./scripts/compose up --build -d
down:
	./scripts/compose down
dev:
	go run ./cmd/secretary
