.PHONY: test proto
test:
	go test ./...
proto:
	protoc --go_out=. --go_opt=module=github.com/qleelulu/procmesh proto/shim/v1/shim.proto
