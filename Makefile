.PHONY: test test-go test-acceptance test-e2e test-e2e-web proto proto-ts web web-dev bin
test: test-go
test-go:
	go test ./...
test-acceptance:
	go test -tags=acceptance ./internal/agent -count=1 -timeout 300s
test-e2e-web:
	go test -tags='acceptance web_e2e' ./internal/agent -run '^TestP5_Playwright_' -count=1 -timeout 180s
test-e2e: test-acceptance test-e2e-web
proto:
	PATH="$$PATH:$$(go env GOPATH)/bin" protoc \
		--go_out=. --go_opt=module=github.com/qleelulu/procmesh \
		--connect-go_out=. --connect-go_opt=module=github.com/qleelulu/procmesh \
		proto/shim/v1/shim.proto \
		proto/procmesh/v1/api.proto \
		proto/procmesh/v1/errors.proto
proto-ts:
	@test -x web/node_modules/.bin/protoc-gen-es || { echo "web/node_modules missing; run: cd web && npm ci"; exit 1; }
	mkdir -p web/src/gen
	PATH="$(CURDIR)/web/node_modules/.bin:$$PATH" protoc \
		--es_out=web/src/gen --es_opt=target=ts \
		--proto_path=proto \
		proto/procmesh/v1/api.proto
web-dev:
	cd web && npm ci && npm run dev
web:
	cd web && npm ci && npm run build
bin:
	go build -o bin/procmesh cmd/procmesh/main.go && go build -o bin/procmesh-agent cmd/procmesh-agent/main.go && go build -o bin/procmesh-shim cmd/procmesh-shim/main.go
