.PHONY: init db run test verify contract
init:
	./scripts/dev-init.sh
db:
	docker compose up -d --wait db
run:
	./scripts/dev-run.sh
test:
	GOCACHE="$(CURDIR)/.local/go-cache" go test -race ./...
verify:
	./scripts/verify.sh --postgres
contract:
	python3 scripts/build-openapi.py
