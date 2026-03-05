BINARY := viaduct
MAIN := ./cmd/viaduct
PID_FILE := .viaduct.pid
LOG_FILE := .viaduct.log

.PHONY: build test lint run run-bg stop status logs docker

build:
	go build -o bin/$(BINARY) $(MAIN)

test:
	go test ./...

lint:
	go vet ./...

run:
	go run $(MAIN)

run-bg:
	@if [ ! -f "viaduct.yaml" ]; then \
		echo "viaduct.yaml not found; run 'make run' once to complete onboarding first"; \
		exit 1; \
	fi
	@mkdir -p .cache/go-build .cache/go-mod
	@nohup /bin/zsh -lc 'GOCACHE=$$(pwd)/.cache/go-build GOMODCACHE=$$(pwd)/.cache/go-mod go run $(MAIN)' > $(LOG_FILE) 2>&1 & echo $$! > $(PID_FILE)
	@echo "viaduct started in background (pid $$(cat $(PID_FILE)))"
	@echo "logs: tail -f $(LOG_FILE)"

stop:
	@if [ -f "$(PID_FILE)" ]; then \
		kill $$(cat $(PID_FILE)) >/dev/null 2>&1 || true; \
		rm -f $(PID_FILE); \
		echo "viaduct stopped"; \
	else \
		echo "no pid file found ($(PID_FILE))"; \
	fi

status:
	@if [ -f "$(PID_FILE)" ] && ps -p $$(cat $(PID_FILE)) >/dev/null 2>&1; then \
		echo "viaduct running (pid $$(cat $(PID_FILE)))"; \
	else \
		[ -f "$(PID_FILE)" ] && rm -f "$(PID_FILE)" || true; \
		echo "viaduct not running"; \
	fi

logs:
	@tail -f $(LOG_FILE)

docker:
	docker build -t viaduct:latest .
