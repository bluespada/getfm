# getfm
#
# Targets: build run install uninstall clean test cover fmt vet staticcheck check cross help
# Run `make help` for a summary.

GO      ?= go
NAME    := getfm
PKG     := ./cmd/getfm

# Optimized build. -trimpath strips local paths so the binary is reproducible
# and carries no directory names, -s -w drop the symbol table and DWARF, and
# CGO_ENABLED=0 leaves a static binary with no libc dependency. Together these
# take the binary from about 15MB to 10MB.
#
# CGO is disabled per recipe rather than exported, because `go test -race`
# needs cgo and would otherwise fail.
LDFLAGS := -s -w
BUILDF  := -trimpath -ldflags "$(LDFLAGS)" -buildvcs=false
STATIC  := CGO_ENABLED=0

# staticcheck is not part of the Go toolchain, so it is looked up on PATH and
# then in the module-aware install location.
STATICCHECK ?= staticcheck
ifeq ($(shell command -v $(STATICCHECK) 2>/dev/null),)
  STATICCHECK := $(shell go env GOPATH)/bin/staticcheck
endif

# Per-platform naming and install location.
ifeq ($(OS),Windows_NT)
  BIN    := $(NAME).exe
  # LOCALAPPDATA arrives with backslashes; forward slashes work everywhere and
  # keep the recipe readable.
  BINDIR ?= $(subst \,/,$(LOCALAPPDATA))/Microsoft/WindowsApps
else
  BIN    := $(NAME)
  BINDIR ?= $(HOME)/.local/bin
endif

DESTDIR ?=
DEST    := $(DESTDIR)$(BINDIR)/$(BIN)
OUT     := bin/$(BIN)

.DEFAULT_GOAL := build
.PHONY: all build run install uninstall clean test cover fmt vet staticcheck check cross help

## build: compile an optimized binary into bin/
build:
	@mkdir -p bin
	$(STATIC) $(GO) build $(BUILDF) -o $(OUT) $(PKG)
	@echo "built $(OUT)"

## run: build and launch the browser
run: build
	./$(OUT) $(ARGS)

## install: copy the binary into the per-user bin directory
install: build
	@mkdir -p "$(BINDIR)"
	@cp -f $(OUT) "$(DEST)"
	@chmod +x "$(DEST)" 2>/dev/null || true
	@echo "installed $(DEST)"
	@case ":$$PATH:" in *":$(BINDIR):"*) ;; \
	*) echo ""; \
	   echo "  $(BINDIR) is not on your PATH."; \
	   echo "  add this to your shell profile:"; \
	   echo ""; \
	   echo "      export PATH=\"$(BINDIR):\$$PATH\""; \
	   echo "" ;; \
	esac

## uninstall: remove the installed binary
uninstall:
	@rm -f "$(DEST)"
	@echo "removed $(DEST)"

## clean: remove build and coverage artifacts
clean:
	@rm -rf bin
	@rm -f *.out coverage.* *.coverprofile profile.cov getfm getfm.exe
	@echo "cleaned"

## test: run the suite under the race detector
test:
	$(GO) test -race -count=1 ./...

## cover: run the suite and open a coverage report
cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out

## fmt: rewrite sources with gofmt
fmt:
	$(GO) fmt ./...

## vet: run the standard static checks
vet:
	$(GO) vet ./...

## staticcheck: unused code and correctness findings
staticcheck:
	@command -v $(STATICCHECK) >/dev/null 2>&1 || { \
	  echo "$(STATICCHECK) not found. Install it with:"; \
	  echo "  go install honnef.co/go/tools/cmd/staticcheck@latest"; \
	  exit 1; \
	}
	$(STATICCHECK) ./...

## check: everything CI would run
check: fmt vet staticcheck test
	@echo "ok"

## cross: build for the platforms getfm is verified on
cross:
	@mkdir -p bin
	@for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 \
	              windows/amd64 windows/arm64; do \
	  os=$${target%/*}; arch=$${target#*/}; out=bin/$(NAME)-$$os-$$arch; \
	  if [ "$$os" = "windows" ]; then out=$$out.exe; fi; \
	  echo "  $$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
	    $(GO) build $(BUILDF) -o $$out $(PKG) || exit 1; \
	done
	@echo "built for 6 platforms into bin/"

## help: list the targets
help:
	@echo "getfm targets:"
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /' | sort
