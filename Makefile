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
	@echo "web/ not yet"
web:
	cd web && npm ci && npm run build
