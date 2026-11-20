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

# BlackArch 源：只启用源与密钥，不预装安全工具；需要时由 agent 或 setup 用 pacman 安装。
# 按官方 strap.sh 的流程展开（不执行远程脚本）：blackarch-keyring 包的签名必须来自下列签名主密钥
# （与 strap.sh / blackarch-trusted 保持一致），在临时 GNUPGHOME 里校验通过后才导入 pacman 密钥环。
# 官方 Arch 镜像不带 pacman 本地私钥：为 populate 临时生成一把，签完即删，与上游镜像的处理一致，
# 已签入的信任保留，各 orb 不共享可用的本地私钥。源追加在官方源之后，同名包仍取官方版本。
ARG BLACKARCH_SIGNERS="8F9A9793CB8591147C2EC70566E0CDBD1E01F333 A0917C4147A37007CB54C1CFD295AA940EFDDF62 4345771566D76038C7FEB43863EC0ADBEA87E4E3 F9A6E68A711354D84A9B91637533BAFE69A25079"
RUN case "$TARGETARCH" in amd64) a=x86_64 ;; arm64) a=aarch64 ;; *) exit 1 ;; esac \
 && tmp="$(mktemp -d)" && cd "$tmp" \
 && repo="https://blackarch.org/blackarch/blackarch/os/${a}" \
 && curl -fsSLO "$repo/blackarch.db" \
 && pkg="$(bsdtar -xOf blackarch.db 'blackarch-keyring-*/desc' | awk '/^%FILENAME%$/ { getline; print; exit }')" \
 && [[ -n "$pkg" ]] \
 && curl -fsSLO "$repo/$pkg" && curl -fsSLO "$repo/$pkg.sig" \
 && export GNUPGHOME="$tmp/gnupg" && install -d -m 0700 "$GNUPGHOME" \
 && for fpr in $BLACKARCH_SIGNERS; do \
      gpg --batch --keyserver hkps://keyserver.ubuntu.com --recv-keys "$fpr" \
      || gpg --batch --keyserver hkps://keys.openpgp.org --recv-keys "$fpr" || true; \
    done \
 && signer="$(gpg --batch --status-fd 1 --verify "$pkg.sig" "$pkg" 2>/dev/null | awk '$2 == "VALIDSIG" { print $NF }')" \
 && [[ -n "$signer" && " $BLACKARCH_SIGNERS " == *" $signer "* ]] \
 && gpgconf --kill all && unset GNUPGHOME \
 && mkdir pkg && bsdtar -xf "$pkg" -C pkg usr/share/pacman/keyrings \
 && cp pkg/usr/share/pacman/keyrings/* /usr/share/pacman/keyrings/ \
 && pacman-key --init && pacman-key --populate blackarch \
 && pacman -U --noconfirm --overwrite '/usr/share/pacman/keyrings/blackarch*' "$pkg" \
 && gpgconf --homedir /etc/pacman.d/gnupg --kill all \
 && rm -rf /etc/pacman.d/gnupg/openpgp-revocs.d/* /etc/pacman.d/gnupg/private-keys-v1.d/* \
      /etc/pacman.d/gnupg/pubring.gpg~ /etc/pacman.d/gnupg/pubring.kbx~ /etc/pacman.d/gnupg/S.* \
 && curl -fsSLo /etc/pacman.d/blackarch-mirrorlist https://blackarch.org/blackarch-mirrorlist \
 && printf '\n[blackarch]\nInclude = /etc/pacman.d/blackarch-mirrorlist\n' >> /etc/pacman.conf \
 && cd / && rm -rf "$tmp"

# Arch 不支持部分升级；刷新包索引时同时完整升级，不安装 AUR 构建助手。
# QEMU 用户态模拟不提供 Landlock：只在构建下载时关闭该沙箱，包签名校验仍启用。
# go-yq 是 Mike Farah yq v4；valkey 提供兼容的 redis-cli，但不启动数据库服务。
# 语言服务器对应 omp 内置 LSP 配置里镜像已有工具链的语言：clang 提供 clangd，rust-src 供 rust-analyzer 解析标准库；
# typescript 6 的 tsc 不支持 --lsp，omp 会自动改用 typescript-language-server。Python 只装 pyright 一个类型检查器，避免诊断重复。
# ruby-lsp 会把 debug gem 写进自己组合的 Gemfile；预装 ruby-debug，离线或项目未 bundle install 时它仍能启动。
# 非开发工具覆盖文档转换、OCR、音视频、图片优化、下载与数据处理；Python 常用库由 pacman 装入系统环境，可直接 import。
# blackarch-mirrorlist 接管上一步下载的同名镜像列表，之后随系统升级更新。
# extra/sox 14.8.0.1-2 链接 libebur128.so.1 却未声明依赖（官方 archlinux 镜像单装 sox 即复现），显式补装；上游补上依赖后可删。
RUN pacman --disable-sandbox -Syu --noconfirm --needed --overwrite /etc/pacman.d/blackarch-mirrorlist \
      blackarch-mirrorlist \
      base-devel ca-certificates curl wget openssl git git-lfs openssh rsync sudo bash dash \
      coreutils diffutils findutils grep sed gawk patch file tree less vim nano dos2unix \
      jq go-yq ripgrep fd zip unzip tar gzip bzip2 zstd xz 7zip unrar cabextract bc moreutils parallel pv \
      procps-ng psmisc lsof time util-linux tmux htop ncdu bat glibc tzdata \
      iproute2 iptables iputils bind openbsd-netcat socat traceroute whois nmap iperf3 \
      cmake ninja autoconf automake libtool bison flex gdb strace ltrace shfmt clang \
      libffi sqlite zlib readline python python-pip python-pipx ruff \
      nodejs npm pnpm bun uv go rust rust-src github-cli \
      ruby ruby-bundler php php-sqlite composer lua postgresql-libs mariadb-clients valkey \
      gopls typescript typescript-language-server pyright bash-language-server lua-language-server ruby-lsp ruby-debug \
      yaml-language-server vscode-json-languageserver vscode-css-languageserver vscode-html-languageserver \
      dockerfile-language-server \
      libxml2 libxslt poppler qpdf ghostscript mupdf-tools img2pdf libreoffice-fresh \
      tesseract tesseract-data-eng tesseract-data-chi_sim tesseract-data-chi_tra \
      imagemagick librsvg libwebp-utils libjxl libheif libavif optipng pngquant jpegoptim gifsicle \
      perl-image-exiftool graphviz qrencode zbar ffmpeg sox libebur128 mediainfo yt-dlp aria2 httpie miller duckdb \
      python-numpy python-pandas python-scipy python-matplotlib python-pillow python-requests python-beautifulsoup4 \
      python-lxml python-yaml python-openpyxl python-xlsxwriter python-docx python-pypdf python-reportlab \
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

# ARM 软件仓库缺少下列工具（PHP 的 phpactor 两边都缺）；双架构统一使用上游最新发行版并按 GitHub 发布的摘要校验。
RUN case "$TARGETARCH" in amd64) a=x86_64 ga=x64 pa=amd64 ;; arm64) a=aarch64 ga=arm64 pa=arm64 ;; *) exit 1 ;; esac \
 && fetch() { \
      local release asset url sha; \
      release="$(curl -fsSL "https://api.github.com/repos/$1/releases/latest")"; \
      asset="${2//@V@/$(jq -er .tag_name <<< "$release")}"; \
      url="$(jq -er --arg n "$asset" '.assets[] | select(.name == $n) | .browser_download_url' <<< "$release")"; \
      sha="$(jq -er --arg n "$asset" '.assets[] | select(.name == $n) | .digest | select(startswith("sha256:")) | ltrimstr("sha256:")' <<< "$release")"; \
      curl -fsSLo "$3" "$url" && echo "$sha  $3" | sha256sum -c -; \
    } \
 && fetch koalaman/shellcheck "shellcheck-@V@.linux.${a}.tar.xz" /tmp/shellcheck.tar.xz \
 && tar -xJf /tmp/shellcheck.tar.xz -C /usr/local/bin --strip-components=1 --wildcards '*/shellcheck' \
 && fetch jgm/pandoc "pandoc-@V@-linux-${pa}.tar.gz" /tmp/pandoc.tar.gz \
 && tar -xzf /tmp/pandoc.tar.gz -C /usr/local/bin --strip-components=2 --wildcards '*/bin/pandoc' \
 && fetch rust-lang/rust-analyzer "rust-analyzer-${a}-unknown-linux-gnu.gz" /tmp/rust-analyzer.gz \
 && gzip -dc /tmp/rust-analyzer.gz > /usr/local/bin/rust-analyzer \
 && fetch artempyanykh/marksman "marksman-linux-${ga}" /usr/local/bin/marksman \
 && fetch phpactor/phpactor phpactor.phar /usr/local/bin/phpactor \
 && chmod 0755 /usr/local/bin/rust-analyzer /usr/local/bin/marksman /usr/local/bin/phpactor \
 && rm /tmp/shellcheck.tar.xz /tmp/pandoc.tar.gz /tmp/rust-analyzer.gz \
 && { shellcheck --version | sed -n 2p; pandoc --version | head -1; rust-analyzer --version; \
      printf 'marksman %s\n' "$(marksman --version)"; phpactor --version; } > /usr/local/share/oryn/upstream-versions.txt

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
# Arch 的 perl 脚本（如 exiftool）装在 /usr/bin/*_perl，原本只由登录 shell 的 perlbin.sh 补进 PATH。
ENV HOME=/home/user
ENV PATH=/home/user/.local/bin:/home/user/.cargo/bin:/home/user/go/bin:$PATH:/usr/bin/site_perl:/usr/bin/vendor_perl:/usr/bin/core_perl
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
