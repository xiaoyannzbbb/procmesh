.PHONY: test test-go test-acceptance test-e2e test-e2e-web proto proto-ts web web-dev bin

PROCMESH_PROTO_FILES := \
	procmesh/v1/errors.proto \
	procmesh/v1/mutation.proto \
	procmesh/v1/process_types.proto \
	procmesh/v1/process.proto \
	procmesh/v1/cluster_types.proto \
	procmesh/v1/cluster.proto \
	procmesh/v1/auth.proto \
	procmesh/v1/access.proto \
	procmesh/v1/audit.proto \
	procmesh/v1/metrics.proto \
	procmesh/v1/batch.proto \
	procmesh/v1/alert.proto \
	procmesh/v1/backup_types.proto \
	procmesh/v1/backup.proto \
	procmesh/v1/cluster_backup.proto \
	procmesh/v1/cluster_backup_agent.proto \
	procmesh/v1/peer_replication.proto \
	procmesh/v1/disaster_replication.proto \
	procmesh/v1/update.proto

WEB_PROTO_FILES := $(filter-out \
	procmesh/v1/cluster_backup_agent.proto \
	procmesh/v1/peer_replication.proto, \
	$(PROCMESH_PROTO_FILES))
test: test-go
test-go:
	@set -eu; \
	log="$$(mktemp "$${TMPDIR:-/tmp}/procmesh-go-tests.XXXXXX")"; \
	trap 'rm -f "$$log"' EXIT INT TERM; \
	packages="$$(go list -f '{{if ne .ImportPath "github.com/qleelulu/procmesh/internal/agent"}}{{.ImportPath}}{{end}}' ./...)"; \
	go test $$packages >"$$log" 2>&1 & non_agent_pid=$$!; \
	status=0; \
	./scripts/test-agent-shards.sh || status=$$?; \
	if ! wait $$non_agent_pid; then status=1; fi; \
	cat "$$log"; \
	exit $$status
test-acceptance:
	./scripts/test-agent-shards.sh -tags=acceptance -count=1 -timeout 300s
test-e2e-web:
	go test -tags='acceptance web_e2e' ./internal/agent -run '^TestP5_Playwright_' -count=1 -timeout 180s
test-e2e: test-acceptance test-e2e-web
proto:
	PATH="$$PATH:$$(go env GOPATH)/bin" protoc \
		--go_out=. --go_opt=module=github.com/qleelulu/procmesh \
		--connect-go_out=. --connect-go_opt=module=github.com/qleelulu/procmesh \
		proto/shim/v1/shim.proto
	PATH="$$PATH:$$(go env GOPATH)/bin" protoc \
		--proto_path=proto \
		--go_out=. --go_opt=module=github.com/qleelulu/procmesh \
		--connect-go_out=. --connect-go_opt=module=github.com/qleelulu/procmesh \
		$(PROCMESH_PROTO_FILES)
proto-ts:
	@test -x web/node_modules/.bin/protoc-gen-es || { echo "web/node_modules missing; run: cd web && npm ci"; exit 1; }
	mkdir -p web/src/gen
	PATH="$(CURDIR)/web/node_modules/.bin:$$PATH" protoc \
		--es_out=web/src/gen --es_opt=target=ts \
		--proto_path=proto \
		$(WEB_PROTO_FILES)
web-dev:
	cd web && npm ci && npm run dev
web:
	cd web && npm ci && npm run build
bin:
	go build -o bin/procmesh cmd/procmesh/main.go && go build -o bin/procmesh-agent cmd/procmesh-agent/main.go && go build -o bin/procmesh-shim cmd/procmesh-shim/main.go
