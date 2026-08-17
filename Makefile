.PHONY: test proto proto-ts web web-dev test-e2e bin
test:
	go test ./...
test-e2e:
	go test ./internal/agent -run TestP5_ -count=1 -timeout 180s
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
