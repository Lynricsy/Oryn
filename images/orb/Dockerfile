# Oryn orb 镜像：每个线程一台的隔离开发机。
# 由 scripts/build-orb-image.sh 构建；构建上下文是只含 omp / orbd / oryn 三个二进制的暂存目录。
FROM debian:13-slim

# 架构：amd64 | arm64（docker buildx 自动注入；经典 builder 下由构建脚本传入）
ARG TARGETARCH=amd64

# 工具链版本（显式锁定，升级时一起改）
ARG NODE_VERSION=26.13.1
ARG BUN_VERSION=1.4.4
ARG UV_VERSION=0.13.7
ARG GO_VERSION=1.27.4
ARG RUSTUP_VERSION=1.29.3
ARG RUST_VERSION=1.99.1
ARG GH_VERSION=2.103.3

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl git openssh-client sudo bash procps less jq ripgrep fd-find unzip zstd xz-utils \
      build-essential pkg-config python3 python3-venv python3-pip tmux locales \
 && ln -s /usr/bin/fdfind /usr/local/bin/fd \
 && sed -i 's/^# *en_US.UTF-8 UTF-8/en_US.UTF-8 UTF-8/' /etc/locale.gen && locale-gen \
 && rm -rf /var/lib/apt/lists/*

# 桌面栈（M8）：Xvnc + openbox + Chromium + CJK / emoji 字体；ORYN_DESKTOP=1 让 orbd 启用桌面
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      tigervnc-standalone-server x11-xkb-utils xkb-data openbox chromium \
      fonts-liberation fonts-noto-cjk fonts-noto-color-emoji x11-utils xclip \
 && rm -rf /var/lib/apt/lists/* \
 && Xtigervnc -version 2>&1 | head -1 && chromium --version && openbox --version | head -1
ENV ORYN_DESKTOP=1

# Node.js（当前 LTS），校验官方 SHASUMS256
RUN case "$TARGETARCH" in amd64) a=x64 ;; arm64) a=arm64 ;; *) echo "unsupported $TARGETARCH"; exit 1 ;; esac \
 && f="node-v${NODE_VERSION}-linux-${a}.tar.xz" \
 && cd /tmp && curl -fsSLO "https://nodejs.org/dist/v${NODE_VERSION}/${f}" \
 && curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/SHASUMS256.txt" | grep " ${f}\$" | sha256sum -c - \
 && tar -xJf "$f" -C /usr/local --strip-components=1 --exclude='*.md' --exclude=LICENSE \
 && rm -f "$f" && node --version && npm --version

# bun（x64 用 baseline 变体，与 omp 构建目标一致）
RUN case "$TARGETARCH" in amd64) a=x64-baseline ;; arm64) a=aarch64 ;; esac \
 && cd /tmp && curl -fsSLo bun.zip "https://github.com/oven-sh/bun/releases/download/bun-v${BUN_VERSION}/bun-linux-${a}.zip" \
 && unzip -q bun.zip && install -m 0755 "bun-linux-${a}/bun" /usr/local/bin/bun && ln -s bun /usr/local/bin/bunx \
 && rm -rf bun.zip "bun-linux-${a}" && bun --version

# uv
RUN case "$TARGETARCH" in amd64) a=x86_64 ;; arm64) a=aarch64 ;; esac \
 && curl -fsSL "https://github.com/astral-sh/uv/releases/download/${UV_VERSION}/uv-${a}-unknown-linux-gnu.tar.gz" \
    | tar -xz -C /usr/local/bin --strip-components=1 \
 && uv --version

# Go
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" | tar -xz -C /usr/local \
 && /usr/local/go/bin/go version
ENV PATH=/usr/local/go/bin:$PATH

# gh
RUN curl -fsSL "https://github.com/cli/cli/releases/download/v${GH_VERSION}/gh_${GH_VERSION}_linux_${TARGETARCH}.tar.gz" \
    | tar -xz -C /tmp \
 && install -m 0755 "/tmp/gh_${GH_VERSION}_linux_${TARGETARCH}/bin/gh" /usr/local/bin/gh \
 && rm -rf "/tmp/gh_${GH_VERSION}_linux_${TARGETARCH}" && gh --version

# 用户 user（uid 1000，免密 sudo）
RUN useradd --create-home --uid 1000 --shell /bin/bash user \
 && echo 'user ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/user && chmod 0440 /etc/sudoers.d/user

# Rust（rustup minimal profile；工具链目录归 user 所有，便于 agent 自行增减组件）
ENV RUSTUP_HOME=/usr/local/rustup \
    CARGO_HOME=/usr/local/cargo
ENV PATH=/usr/local/cargo/bin:$PATH
RUN case "$TARGETARCH" in amd64) a=x86_64 ;; arm64) a=aarch64 ;; esac \
 && curl -fsSLo /tmp/rustup-init "https://static.rust-lang.org/rustup/archive/${RUSTUP_VERSION}/${a}-unknown-linux-gnu/rustup-init" \
 && chmod +x /tmp/rustup-init \
 && /tmp/rustup-init -y --no-modify-path --profile minimal --default-toolchain "${RUST_VERSION}" \
 && rm /tmp/rustup-init && chown -R user:user "$RUSTUP_HOME" "$CARGO_HOME" \
 && rustc --version && cargo --version

# 登录 shell 会由 /etc/profile 重置 PATH，这里把 Go / Rust 工具链补回去
RUN printf '%s\n' 'export RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo' \
      'export PATH=/usr/local/go/bin:/usr/local/cargo/bin:$PATH' > /etc/profile.d/oryn-toolchains.sh

COPY --chmod=0755 omp orbd oryn /usr/local/bin/

ENV ORYN_ORB=1
USER user
WORKDIR /home/user/workspace
# 预解压 omp 的 natives 到 ~/.omp/natives/，避免首个线程启动时再解压（--version 不加载原生插件，--smoke-test 会）；
# 冒烟产生的日志与运行目录不留在镜像里
RUN omp --version && omp --smoke-test && rm -rf /home/user/.omp/logs /home/user/.omp/run \
 && test -n "$(ls -A /home/user/.omp/natives)"

CMD ["orbd", "run"]
