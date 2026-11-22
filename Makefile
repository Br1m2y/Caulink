BIN_DIR := bin
GO      ?= go

.PHONY: all build test lint fmt vet probe clean check

all: check

build:
	$(GO) build -trimpath -o $(BIN_DIR)/ ./cmd/...

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

lint: fmt vet

fmt:
	$(GO) fmt ./...

check: vet test build

## probe: Phase 0 多 IP 并发测速。需要 root/管理员、Linux 或 Windows 多网卡环境。
probe:
	$(GO) run ./cmd/phase0probe -ips "$(IPS)" -url "$(URL)" -runs 3

clean:
	rm -rf $(BIN_DIR)
