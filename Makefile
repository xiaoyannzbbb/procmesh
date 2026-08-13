.PHONY: test proto
test:
	go test ./...
proto:
	PATH=$(PATH):/Users/qleelulu/go/1.26.0/bin protoc --go_out=. --go_opt=module=github.com/qleelulu/procmesh proto/shim/v1/shim.proto
