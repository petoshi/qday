QDAY_GO := $(if $(wildcard .tools/go/bin/go),.tools/go/bin/go,go)

.PHONY: build test race smoke browser dist desktop-test
build:
	$(QDAY_GO) build -trimpath -ldflags='-s -w' -o build/qday ./node/cmd/qday
	$(QDAY_GO) build -trimpath -ldflags='-s -w' -o build/qday-wallet ./node/cmd/qday-wallet
test:
	NO_PROXY='*' no_proxy='*' $(QDAY_GO) test ./core/... ./coreutils/... ./node/... ./third_party/upnp/...
race:
	NO_PROXY='*' no_proxy='*' $(QDAY_GO) test -race ./core/gateway ./coreutils/syncer ./coreutils/qday ./node/qday ./node/internal/localapp ./node/internal/portmap ./third_party/upnp/...
smoke: build
	python3 scripts/smoke.py
browser: build
	node scripts/browser-smoke.cjs
dist: build
	python3 scripts/package.py --manifest $(MANIFEST) $(if $(TARGETS),--targets $(TARGETS)) $(if $(SEEDS),--peers $(SEEDS))
desktop-test:
	python3 scripts/desktop-smoke.py
