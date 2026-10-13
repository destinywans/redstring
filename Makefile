.PHONY: init db run web-api web-dev web-build test verify contract
init:
	./scripts/dev-init.sh
db:
	docker compose up -d --wait db
run:
	./scripts/dev-run.sh
web-api:
	./scripts/dev-web-api.sh
web-dev:
	pnpm --dir web dev
web-build:
	VITE_API_MODE=http pnpm --dir web build
test:
	GOCACHE="$(CURDIR)/.local/go-cache" go test -race ./...
verify:
	./scripts/verify.sh --postgres
contract:
	python3 scripts/build-openapi.py
