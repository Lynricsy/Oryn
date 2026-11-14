# Oryn orb 镜像：每个线程一台的隔离开发机。
# 由 scripts/build-orb-image.sh 构建；上下文只含三个二进制与冒烟脚本。
# x64 使用官方镜像，ARM64 使用公开每日构建的社区镜像；构建脚本强制拉取最新版本。
ARG TARGETARCH=amd64
FROM archlinux:latest AS base-amd64
FROM menci/archlinuxarm:latest AS base-arm64
FROM base-${TARGETARCH} AS orb
ARG TARGETARCH

ENV LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    TZ=Etc/UTC \
    PAGER=cat \
    GIT_PAGER=cat \
    GH_PAGER=cat \
    GH_PROMPT_DISABLED=1 \
    GIT_TERMINAL_PROMPT=0 \
    GCM_INTERACTIVE=never \
    NO_COLOR=1 \
    PYTHONUNBUFFERED=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# Arch 不支持部分升级；刷新包索引时同时完整升级，不安装 AUR 构建助手。
# QEMU 用户态模拟不提供 Landlock：只在构建下载时关闭该沙箱，包签名校验仍启用。
# go-yq 是 Mike Farah yq v4；valkey 提供兼容的 redis-cli，但不启动数据库服务。
RUN pacman --disable-sandbox -Syu --noconfirm --needed \
      base-devel ca-certificates curl wget openssl git git-lfs openssh rsync sudo bash dash \
      coreutils diffutils findutils grep sed gawk patch file tree less vim dos2unix \
      jq go-yq ripgrep fd zip unzip tar gzip bzip2 zstd xz 7zip bc moreutils \
      procps-ng psmisc lsof time util-linux tmux glibc tzdata \
      iproute2 iptables iputils bind openbsd-netcat socat traceroute \
      cmake ninja autoconf automake libtool bison flex gdb strace ltrace shfmt \
      libffi sqlite zlib readline python python-pip python-pipx ruff \
      nodejs npm pnpm bun uv go rust github-cli \
      ruby ruby-bundler php php-sqlite composer lua postgresql-libs mariadb-clients valkey \
      libxml2 libxslt poppler qpdf imagemagick ffmpeg \
      tigervnc xorg-setxkbmap xkeyboard-config openbox chromium \
      ttf-liberation noto-fonts-cjk noto-fonts-emoji xorg-xdpyinfo xorg-xwininfo xorg-xprop xorg-xauth xclip \
 && printf '%s\n' 'en_US.UTF-8 UTF-8' > /etc/locale.gen && locale-gen \
 && php -r 'foreach (["curl", "iconv", "mbstring", "openssl", "pdo_sqlite", "sqlite3", "zip"] as $extension) { if (!extension_loaded($extension)) { echo "extension=$extension\n"; } }' > /etc/php/conf.d/oryn.ini \
 && install -d /usr/local/share/oryn \
 && pacman -Q > /usr/local/share/oryn/packages.txt \
 && pacman -Scc --noconfirm \
 && chromium --version && openbox --version | head -1
# Chromium 受管策略（root 所有，不随 Profile 归档传播）：禁止安装任何扩展、代理固定直连。
# 防止被注入的 agent 往独占持有的 Profile 里放扩展或代理设置、经保存长期驻留（M8 安全审查 F2）。
RUN mkdir -p /etc/chromium/policies/managed \
 && printf '%s\n' '{' \
      '  "ExtensionInstallBlocklist": ["*"],' \
      '  "ProxySettings": { "ProxyMode": "direct" }' \
      '}' > /etc/chromium/policies/managed/oryn.json \
 && chmod 0644 /etc/chromium/policies/managed/oryn.json && jq -e . /etc/chromium/policies/managed/oryn.json
ENV ORYN_DESKTOP=1

# ARM 软件仓库没有 ShellCheck；双架构统一使用上游最新静态发行版并校验其摘要。
RUN case "$TARGETARCH" in amd64) a=x86_64 ;; arm64) a=aarch64 ;; *) exit 1 ;; esac \
 && release="$(curl -fsSL https://api.github.com/repos/koalaman/shellcheck/releases/latest)" \
 && version="$(jq -er .tag_name <<< "$release")" \
 && asset="shellcheck-${version}.linux.${a}.tar.xz" \
 && url="$(jq -er --arg n "$asset" '.assets[] | select(.name == $n) | .browser_download_url' <<< "$release")" \
 && sha="$(jq -er --arg n "$asset" '.assets[] | select(.name == $n) | .digest | select(startswith("sha256:")) | ltrimstr("sha256:")' <<< "$release")" \
 && curl -fsSLo /tmp/shellcheck.tar.xz "$url" \
 && echo "$sha  /tmp/shellcheck.tar.xz" | sha256sum -c - \
 && tar -xJf /tmp/shellcheck.tar.xz -C /usr/local/bin --strip-components=1 "shellcheck-${version}/shellcheck" \
 && rm /tmp/shellcheck.tar.xz \
 && shellcheck --version > /usr/local/share/oryn/shellcheck-version.txt

# Docker（M11a 嵌套 Docker）：Docker 官方静态发行版（download.docker.com，与 Debian trixie apt 源同一版本）
# 加 buildx / compose 插件，版本与摘要全部钉死。Arch 仓库是滚动版本、无法钉版本，所以不用 pacman 的 docker。
# 不启用任何自启动：只有带 Docker 的 orb 类型（sysbox 运行时，ORYN_NESTED_DOCKER=1）由 orbd 以 sudo 监督 dockerd；
# 其它类型的 orb 里 docker 命令存在但连不上守护进程。dockerd 依赖的 iptables 在上面的 pacman 列表里。
ARG DOCKER_VERSION=29.9.3
ARG DOCKER_SHA256_AMD64=3fb5d1f13014c18cc54945bc177c58a9056a482dada031e17d12cf2e6922c973
ARG DOCKER_SHA256_ARM64=1b27fd739378ef938401e81ce5afb73b7ad7432d4f598814a9ff5f826bb464d2
ARG BUILDX_VERSION=0.39.0
ARG BUILDX_SHA256_AMD64=5fb31a097304fbf6dd4038649a0decf32c2b9510e4047acd7d68b9d700c46194
ARG BUILDX_SHA256_ARM64=722cdcdfdd6f3fb943fa1ba75102d057feb18ba72aab93b1b0a8cf2b2b48a25f
ARG COMPOSE_VERSION=5.7.1
ARG COMPOSE_SHA256_AMD64=af426722a8af5de812fcee30cc1c39f0639f5f723cbadefb25e5d4db054064cf
ARG COMPOSE_SHA256_ARM64=f2f49f903240895fa764c0dca3a14548b754ad9426f6b609ccaf55ec5a3aa2a5
RUN case "$TARGETARCH" in \
      amd64) a=x86_64 ga=amd64 docker_sha="$DOCKER_SHA256_AMD64" buildx_sha="$BUILDX_SHA256_AMD64" compose_sha="$COMPOSE_SHA256_AMD64" ;; \
      arm64) a=aarch64 ga=arm64 docker_sha="$DOCKER_SHA256_ARM64" buildx_sha="$BUILDX_SHA256_ARM64" compose_sha="$COMPOSE_SHA256_ARM64" ;; \
      *) exit 1 ;; \
    esac \
 && plugins=/usr/local/lib/docker/cli-plugins \
 && curl -fsSLo /tmp/docker.tgz "https://download.docker.com/linux/static/stable/${a}/docker-${DOCKER_VERSION}.tgz" \
 && echo "${docker_sha}  /tmp/docker.tgz" | sha256sum -c - \
 && tar -xzf /tmp/docker.tgz -C /usr/local/bin --strip-components=1 --no-same-owner \
 && rm /tmp/docker.tgz \
 && install -d "$plugins" \
 && curl -fsSLo "$plugins/docker-buildx" "https://github.com/docker/buildx/releases/download/v${BUILDX_VERSION}/buildx-v${BUILDX_VERSION}.linux-${ga}" \
 && echo "${buildx_sha}  $plugins/docker-buildx" | sha256sum -c - \
 && curl -fsSLo "$plugins/docker-compose" "https://github.com/docker/compose/releases/download/v${COMPOSE_VERSION}/docker-compose-linux-${a}" \
 && echo "${compose_sha}  $plugins/docker-compose" | sha256sum -c - \
 && chmod 0755 "$plugins/docker-buildx" "$plugins/docker-compose" \
 && groupadd --system docker \
 && { docker --version && dockerd --version && containerd --version && runc --version \
      && docker buildx version && docker compose version; } > /usr/local/share/oryn/docker-version.txt

# 用户 user（uid 1000，免密 sudo）
RUN useradd --create-home --uid 1000 --shell /bin/bash user \
 && usermod -aG docker user \
 && echo 'user ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/user && chmod 0440 /etc/sudoers.d/user \
 && install -d -o user -g user /home/user/workspace /home/user/.local /home/user/.local/bin \
 && git config --system init.defaultBranch main \
 && git config --system core.pager cat \
 && git config --system color.ui false \
 && git lfs install --system --skip-repo

# 普通进程与登录 shell 都能找到用户安装的工具；不注入 alias、函数或 BASH_ENV。
ENV HOME=/home/user
ENV PATH=/home/user/.local/bin:/home/user/.cargo/bin:/home/user/go/bin:$PATH
RUN printf '%s\n' 'export PATH="$HOME/.local/bin:$HOME/.cargo/bin:$HOME/go/bin:$PATH"' > /etc/profile.d/oryn-toolchains.sh \
 && printf '%s\n' '# 保留标准命令语义，不定义别名、补全或彩色提示。' \
      'case $- in *i*) PS1="\\u@\\h:\\w\\$ " ;; esac' > /home/user/.bashrc \
 && printf '%s\n' 'prefix=${HOME}/.local' > /home/user/.npmrc \
 && printf '%s\n' '[ ! -f "$HOME/.profile" ] || . "$HOME/.profile"' \
      '[ ! -f "$HOME/.bashrc" ] || . "$HOME/.bashrc"' > /home/user/.bash_profile \
 && touch /home/user/.profile \
 && chown user:user /home/user/.bashrc /home/user/.bash_profile /home/user/.profile /home/user/.npmrc

COPY --chmod=0755 omp orbd oryn /usr/local/bin/
COPY --chmod=0755 smoke-test.sh /usr/local/lib/oryn/smoke-test.sh

ENV ORYN_ORB=1
USER user
WORKDIR /home/user/workspace
# 预解压 omp 的 natives 到 ~/.omp/natives/，避免首个线程启动时再解压（--version 不加载原生插件，--smoke-test 会）；
# 冒烟产生的日志与运行目录不留在镜像里
RUN omp --version && omp --smoke-test && rm -rf /home/user/.omp/logs /home/user/.omp/run \
 && test -n "$(ls -A /home/user/.omp/natives)"

CMD ["orbd", "run"]
