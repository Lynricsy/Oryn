# Oryn 构建入口。所有目标在仓库根目录执行。

GO      ?= go
BUN     ?= bun
SQLC    ?= $(shell command -v sqlc 2>/dev/null || echo $(shell $(GO) env GOPATH)/bin/sqlc)
VERSION ?= $(shell git describe --tags --exclude 'desktop-v*' --exclude 'mobile-v*' --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/Lynricsy/Oryn/internal/version.Version=$(VERSION)
BINS    := orynd orbd oryn fakeomp

.PHONY: all gen gen-go gen-sqlc gen-web check-gen lint-spec build test web desktop android image dev

all: build

## gen：由 schema/openapi.yaml 与 SQL 生成 Go / TS 代码
gen: gen-go gen-sqlc gen-web

gen-go:
	$(GO) generate ./internal/apigen/

gen-sqlc:
	@if [ -f sqlc.yaml ]; then $(SQLC) generate -f sqlc.yaml; else echo "跳过 sqlc：缺少 sqlc.yaml"; fi

gen-web:
	@if [ -f web/package.json ]; then \
		cd web && { [ -x node_modules/.bin/openapi-typescript ] || $(BUN) install --frozen-lockfile; } && \
		mkdir -p src/api && $(BUN) run gen:api; \
	else echo "跳过 openapi-typescript：缺少 web/package.json"; fi

## check-gen：生成物必须已提交且与源一致
check-gen: gen
	git diff --exit-code -- internal/apigen internal/store/db web/src/api

## lint-spec：校验 OpenAPI 规格。
## 跳过的规则：operation-4xx-response（错误统一由 `default` 响应描述）、
## no-unused-components（WS / link / provider 任务参数等消息类型不被任何 HTTP 操作引用，属有意为之）。
lint-spec:
	$(BUN) x --bun @redocly/cli@latest lint schema/openapi.yaml \
		--skip-rule operation-4xx-response --skip-rule no-unused-components

## build：静态链接的四个二进制输出到 bin/
build:
	@mkdir -p bin
	@for b in $(BINS); do \
		echo "go build bin/$$b"; \
		CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/$$b ./cmd/$$b || exit 1; \
	done

test:
	$(GO) test ./...

## web：构建前端并替换 internal/webui/dist（保留 .gitkeep）
web:
	cd web && $(BUN) install && $(BUN) run build
	find internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
	cp -R web/dist/. internal/webui/dist/

## desktop：构建桌面 App 安装包（Tauri 2，产物在 web/src-tauri/target/release/bundle/）
desktop:
	cd web && $(BUN) install && $(BUN) run tauri build

## android：构建 Android 通用 APK（需先 `source .dev/m7-env.sh`；DIST=gms 时先放好 web/src-tauri/gen/android/app/google-services.json）
DIST ?= foss
android:
	cd web && $(BUN) install && ORG_GRADLE_PROJECT_orynDistribution=$(DIST) $(BUN) run tauri android build --apk

image:
	scripts/build-orb-image.sh

dev: build
	./bin/orynd --config dev/orynd.yaml
