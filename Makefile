# Watchyard: wy and the hub (Go), plus targets for the desktop app (app/) and the Android app (phone/).
#
#   make build       build ./dist/wy
#   make test        go test ./...
#   make lint        gofmt check + go vet
#   make spec-check  load docs/spec/schema.sql into a temp SQLite db, validate docs/spec/api.yaml
#   make fixtures    regenerate testdata/ (spends a little real Claude usage; see testdata/README.md)
#   make check       lint + test + spec-check (what CI runs)
#   make e2e-setup   setup E2E in the Fedora and Ubuntu containers (podman or docker; E2E_ENGINE=...)
#                    or natively: test/e2e/setup/run.sh --native
#   make e2e-machines  wy machine add / pull / remove against an sshd container (WY_E2E_RUNTIME=podman|docker;
#                    skips when no container runtime works)
#   make e2e-terminal  wy board / term / open / ssh end to end (test/e2e/terminal; fake opener, temp HOME)
#   make e2e-android the Android app against a real local hub on the emulator (test/e2e/android; ~15 min,
#                    builds the APK once, ~6 min; skips without the SDK/AVD/KVM; never part of check)
#   make pane-check  the Claude Code pane mod gate (scripts/pane-check.sh and its --self-test; needs claude)
#   make release-check  a release build of wy contains no test seams (fakeclock, fakeopen, peerstest, trusttest)
#   make app-test    the desktop app (app/): npm ci --ignore-scripts, lint, unit tests (no display)
#   make app-smoke   the Electron smoke S1-S15 (Linux: under xvfb-run; a real hub built from here)
#   make app-dist    the bundled wy for this machine + the app packages in app/dist (never signed;
#                    Linux without libcrypt.so.1: the AppImage only, see APP_DIST_LINUX)
#   make app-e2e     the packaged AppImage end to end (its own e2e build in app/dist/e2e, which alone
#                    allows --remote-debugging-port; Linux, xvfb-run)
#   make phone-deps  install phone/node_modules from the lockfile (npm ci)
#   make phone-check the phone app (phone/): npm ci, tsc, eslint, jest, secrets check, and the
#                    android + ios prebuild checks into a temp copy (never signed, nothing uploaded)
#   make phone-prebuild  only the two prebuild checks (phone.md P14)
#   make phone-android-debug  optional: expo prebuild into phone/android (gitignored) and an
#                    unsigned debug compile (needs ANDROID_HOME and JDK 17+; never distributed)
#   make phone-apk-release  YOU run it, not an agent: expo prebuild + Gradle assembleRelease signed with
#                    your own keystore (WY_ANDROID_KEYSTORE, WY_ANDROID_KEY_ALIAS, WY_ANDROID_KEYSTORE_PASSWORD
#                    or WY_ANDROID_SIGNING_PROPERTIES; refuses without them) -> phone/dist/{apk,SHA256SUMS,manifest.json}

GO       ?= go
PYTHON   ?= python3
SQLITE3  ?= sqlite3
VERSION  ?= $(shell git describe --tags --match 'v*' --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null)
LDFLAGS  := -s -w \
	-X github.com/mraj602/watchyard/internal/version.Version=$(VERSION) \
	-X github.com/mraj602/watchyard/internal/version.Commit=$(COMMIT)
GOFILES   = $(shell git ls-files --cached --others --exclude-standard -- '*.go')

.PHONY: all build test lint fmt spec-check fixtures check clean e2e-setup e2e-setup-fedora e2e-setup-ubuntu e2e-machines \
	e2e-terminal e2e-android pane-check release-check app-deps app-test app-smoke app-dist app-e2e \
	phone-deps phone-check phone-prebuild phone-android-debug phone-apk-release

all: check build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o dist/wy ./cmd/wy

test:
	$(GO) test ./...
	$(GO) test -tags fakeinbox,peerstest ./internal/peers/... ./cmd/wy
	$(GO) test -tags trusttest -run Trust ./internal/peers ./internal/version ./cmd/wy
	$(GO) test -tags fakeinbox ./test/e2e/peers

lint:
	@out="$$(gofmt -l $(GOFILES))"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	$(GO) vet ./...

fmt:
	gofmt -w $(GOFILES)

# The Go test in internal/hub/store applies schema.sql with modernc.org/sqlite (the driver
# the hub uses). The sqlite3 CLI check runs too when the CLI is installed.
spec-check:
	$(GO) test -run 'TestSpecSchemaApplies|TestRecordKeepsRawBody' ./internal/hub/store
	@if command -v $(SQLITE3) >/dev/null 2>&1; then \
		tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
		$(SQLITE3) -bail "$$tmp/spec.db" < docs/spec/schema.sql && \
		$(SQLITE3) "$$tmp/spec.db" 'PRAGMA foreign_key_check; PRAGMA integrity_check;' | grep -qx ok && \
		echo "schema.sql: loads in sqlite3 $$($(SQLITE3) --version | cut -d' ' -f1)"; \
	else echo "schema.sql: sqlite3 CLI not installed, skipped CLI check"; fi
	@$(PYTHON) -c 'import sys, yaml; d = yaml.safe_load(open("docs/spec/api.yaml")); \
		assert d.get("openapi", "").startswith("3.1"), "not OpenAPI 3.1"; \
		print("api.yaml: parses,", len(d["paths"]), "paths")'
	@if $(PYTHON) -c 'import openapi_spec_validator' 2>/dev/null; then \
		$(PYTHON) -c 'import yaml; from openapi_spec_validator import validate; \
			validate(yaml.safe_load(open("docs/spec/api.yaml"))); print("api.yaml: valid OpenAPI 3.1")'; \
	else echo "api.yaml: openapi-spec-validator not installed, skipped full validation"; fi

# testdata/ is produced by scripts/fixtures (see testdata/README.md). Pass CAPDIR=<dir> to
# reuse an earlier capture instead of making new Claude runs.
fixtures:
	scripts/fixtures/regenerate.sh $(CAPDIR)
	scripts/fixtures/check-redaction.sh

check: lint test spec-check

E2E_ENGINE ?=
e2e-setup: e2e-setup-fedora e2e-setup-ubuntu

e2e-setup-fedora:
	test/e2e/setup/run.sh fedora $(E2E_ENGINE)

e2e-setup-ubuntu:
	test/e2e/setup/run.sh ubuntu $(E2E_ENGINE)

# The end-to-end test for pull machines (docs/spec/machines.md): real SSH into a container, never the real HOME.
# -v so a skip says why; the test picks podman, else docker, unless WY_E2E_RUNTIME is set.
e2e-machines:
	$(GO) test -count=1 -v -timeout 15m ./test/e2e/machines

# The Android app on the emulator against a real hub on this host (test/e2e/android/README.md).
# Not in `make check`: it needs the Android SDK, an AVD, KVM and minutes.
e2e-android:
	test/e2e/android/run.sh

# The desktop app. npm runs in app/ only.
# Smoke and e2e use temp HOME / WY_HOME / user data folders; on Linux they run under xvfb-run
# when it is installed (unset WAYLAND_DISPLAY so Electron uses X11).
# A private session bus too: under xvfb, Electron and Chrome still reach the desktop's D-Bus
# and test alerts would pop up as real notifications. dbus-run-session gives the run its own
# bus; without it (some CI images) the bus address points nowhere instead.
NPM      ?= npm
NOBUS    := $(shell command -v dbus-run-session >/dev/null 2>&1 && echo 'dbus-run-session --' || echo 'env DBUS_SESSION_BUS_ADDRESS=unix:path=/nonexistent/wy-no-bus')
XVFB     := $(shell command -v xvfb-run >/dev/null 2>&1 && echo 'env -u WAYLAND_DISPLAY -u XDG_SESSION_TYPE xvfb-run -a --server-args="-screen 0 1440x940x24"')
XVFB     := $(if $(XVFB),$(NOBUS) $(XVFB),$(NOBUS))

# Installs app/node_modules with the Electron binary, unless they are there already.
app-deps:
	cd app && { node -e "require('fs').accessSync(require('electron'))" 2>/dev/null || $(NPM) ci; }

# Unit tests need no Electron binary (CI runs npm ci --ignore-scripts); reuse node_modules if present.
app-test:
	cd app && { test -d node_modules || $(NPM) ci --ignore-scripts; } && $(NPM) run lint && $(NPM) test

# The phone app (docs/spec/phone.md P13, P14). No target here signs, uploads or runs eas.
phone-deps:
	cd phone && $(NPM) ci

phone-check: phone-deps
	cd phone && $(NPM) run check
	$(MAKE) phone-prebuild

phone-prebuild:
	cd phone && node scripts/prebuild-check.mjs android && node scripts/prebuild-check.mjs ios

phone-android-debug: phone-deps
	@test -n "$$ANDROID_HOME" || { echo "phone-android-debug: set ANDROID_HOME to the Android SDK"; exit 1; }
	cd phone && WY_APP_VARIANT=development npx expo prebuild --platform android --no-install --clean
	cd phone/android && ./gradlew :app:assembleDebug
	@echo "unsigned debug APK: phone/android/app/build/outputs/apk/debug/ (never distribute it)"

# The signed release build (phone.md P16.1). Never run by CI; see phone/README.md.
phone-apk-release:
	cd phone && ./scripts/release-apk.sh --preflight
	$(MAKE) phone-deps
	cd phone && ./scripts/release-apk.sh

app-smoke: app-deps
	CGO_ENABLED=0 $(GO) build -o app/node_modules/.cache/wy-smoke/wy ./cmd/wy
	cd app && WY_BIN="$$PWD/node_modules/.cache/wy-smoke/wy" $(XVFB) $(NPM) run smoke

# On Linux the deb and rpm targets run electron-builder's bundled fpm, whose Ruby needs
# libcrypt.so.1 (Fedora 40+ ships only libcrypt.so.2 unless libxcrypt-compat is installed).
# Without it the local build makes the AppImage alone instead of failing after it; set
# APP_DIST_LINUX="AppImage deb rpm" (or any list) to choose. CI runs `npm run dist` itself
# and builds all three, as electron-builder.yml says.
APP_DIST_LINUX ?= $(shell [ "$$(uname -s)" = Linux ] && ! { ldconfig -p 2>/dev/null | grep -q 'libcrypt\.so\.1 '; } && echo AppImage)
app-dist: app-deps
	$(if $(APP_DIST_LINUX),@echo "app-dist: Linux targets: $(APP_DIST_LINUX) (deb and rpm need libcrypt.so.1 for fpm: dnf install libxcrypt-compat)")
	cd app && node scripts/fetch-wy.mjs auto auto && CSC_IDENTITY_AUTO_DISCOVERY=false $(NPM) run dist -- --publish never $(if $(APP_DIST_LINUX),--linux $(APP_DIST_LINUX))

# The e2e build bakes watchyard.allowCdp=true into the asar (desktop.md C2); app-dist never does.
app-e2e: app-deps
	cd app && node scripts/fetch-wy.mjs auto auto && scripts/dist-e2e.sh
	cd app && $(XVFB) $(NPM) run e2e

clean:
	rm -rf dist

# The terminal scenarios (docs/spec/terminal.md), with `wy` built with -tags fakeopen.
e2e-terminal:
	$(GO) test -count=1 -v -timeout 15m ./test/e2e/terminal

pane-check:
	scripts/pane-check.sh
	scripts/pane-check.sh --self-test

release-check:
	scripts/ci/check-release-seams.sh

