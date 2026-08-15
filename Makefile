.PHONY: test proto proto-ts web
test:
	go test ./...
proto:
	PATH="$$PATH:$$(go env GOPATH)/bin" protoc \
		--go_out=. --go_opt=module=github.com/qleelulu/procmesh \
		--connect-go_out=. --connect-go_opt=module=github.com/qleelulu/procmesh \
		proto/shim/v1/shim.proto \
		proto/procmesh/v1/api.proto
proto-ts:
	@test -x web/node_modules/.bin/protoc-gen-es || { echo "web/node_modules missing; run: cd web && npm ci"; exit 1; }
	mkdir -p web/src/gen
	PATH="$(CURDIR)/web/node_modules/.bin:$$PATH" protoc \
		--es_out=web/src/gen --es_opt=target=ts \
		--proto_path=proto \
		proto/procmesh/v1/api.proto
web:
	cd web && npm ci && npm run build
