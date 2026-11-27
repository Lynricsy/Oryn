# Oryn orb 镜像：每个线程一台的隔离开发机。
# 由 scripts/build-orb-image.sh 构建；上下文只含三个二进制、冒烟脚本与桌面应用清单相关的三个文件。
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
# 桌面应用（M25）：终端用 xfce4-terminal 而不是 Amp 用的 foot——foot 只支持 Wayland，orb 桌面是 X11（Xvnc）；
# xfce4-terminal 是 GTK3（VTE），不需要 OpenGL，无障碍树可读。文件管理器 pcmanfm（Arch 的 pcmanfm 就是 GTK3 版）、编辑器 mousepad。
# dbus、at-spi2-core 本是 chromium 的依赖，显式列出，免得上游依赖变化后被带走：orbd 靠它们起 session dbus 与 AT-SPI。
# adwaita-icon-theme-legacy 同理（gtk3 的依赖）：pcmanfm 的图标 system-file-manager 只有这个主题里有。
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
      python-lxml python-yaml python-openpyxl python-xlsxwriter python-docx python-pypdf python-reportlab python-typing_extensions \
      tigervnc xorg-setxkbmap xkeyboard-config openbox chromium xorg-xsetroot \
      xfce4-terminal mousepad pcmanfm dbus at-spi2-core adwaita-icon-theme-legacy \
      ttf-liberation noto-fonts-cjk noto-fonts-emoji xorg-xdpyinfo xorg-xwininfo xorg-xprop xorg-xauth xclip \
 && printf '%s\n' 'en_US.UTF-8 UTF-8' > /etc/locale.gen && locale-gen \
 && php -r 'foreach (["curl", "iconv", "mbstring", "openssl", "pdo_sqlite", "sqlite3", "zip"] as $extension) { if (!extension_loaded($extension)) { echo "extension=$extension\n"; } }' > /etc/php/conf.d/oryn.ini \
 && install -d /usr/local/share/oryn \
 && pacman -Q > /usr/local/share/oryn/packages.txt \
 && pacman -Scc --noconfirm \
 && chromium --version && openbox --version | head -1
# python-pptx（M31 Office 文件）：Arch 官方仓库与 Arch Linux ARM 都没有这个包，按 PyPI 钉死版本装进系统环境；
# 它的依赖 lxml、Pillow、XlsxWriter、typing_extensions 都已由上面的 pacman 装好，所以 --no-deps。
RUN pip install --no-cache-dir --no-deps --break-system-packages --root-user-action=ignore python-pptx==1.0.2 \
 && python3 -c 'import pptx; print("python-pptx", pptx.__version__)'
# Chromium 受管策略（root 所有，不随 Profile 归档传播）：禁止安装任何扩展、代理固定直连。
# 防止被注入的 agent 往独占持有的 Profile 里放扩展或代理设置、经保存长期驻留（M8 安全审查 F2）。
RUN mkdir -p /etc/chromium/policies/managed \
 && printf '%s\n' '{' \
      '  "ExtensionInstallBlocklist": ["*"],' \
      '  "ProxySettings": { "ProxyMode": "direct" }' \
      '}' > /etc/chromium/policies/managed/oryn.json \
 && chmod 0644 /etc/chromium/policies/managed/oryn.json && jq -e . /etc/chromium/policies/managed/oryn.json
ENV ORYN_DESKTOP=1
# gdk-pixbuf（≥2.44）经 glycin 解码图片与图标，glycin 默认把解码器放进 bwrap 沙箱。orb 的 seccomp 允许 userns 但不允许
# mount，bwrap 报「Failed to make / slave」；glycin 的可用性检测只认几种 namespace 报错，误判 bwrap 可用，pcmanfm、mousepad
# 加载图标时直接 abort（M25 4.1 真机发现）。放在镜像 ENV：orbd 的 baseEnv 继承它，agent 经 computer 打开的应用也拿得到；
# 不放进 orbd，免得 process provider 在宿主上开桌面时顺手关掉宿主自己的解码沙箱。orb 容器本身是隔离边界，agent 在里面
# 本就能执行任意代码，关掉它不扩大权限。这是上游 glycin 的检测缺陷，上游修好后删掉。
ENV GLYCIN_DISABLE_SANDBOX=i-know-the-risks

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

# VS Code（M25 桌面应用）：微软官方 tarball。不用 pacman 的 code：那是 Code - OSS，走 Open VSX，部分微软扩展装不了。
# 与上面 fetch 同一种做法：从更新 API 取 url 与 sha256hash，下载后校验摘要再解压到 /opt/vscode（约 1.1GB）。
# tarball 不带桌面项；写入 code.desktop，omp computer 的 apps.list 才能找到它。chrome-sandbox 的 setuid 位随 tarball 保留。
RUN case "$TARGETARCH" in amd64) va=linux-x64 ;; arm64) va=linux-arm64 ;; *) exit 1 ;; esac \
 && release="$(curl -fsSL "https://update.code.visualstudio.com/api/update/${va}/stable/latest")" \
 && url="$(jq -er .url <<< "$release")" && sha="$(jq -er .sha256hash <<< "$release")" \
 && curl -fsSLo /tmp/vscode.tar.gz "$url" && echo "$sha  /tmp/vscode.tar.gz" | sha256sum -c - \
 && install -d /opt/vscode \
 && tar -xzf /tmp/vscode.tar.gz -C /opt/vscode --strip-components=1 --no-same-owner \
 && rm /tmp/vscode.tar.gz \
 && ln -s /opt/vscode/bin/code /usr/local/bin/code \
 && test -f /opt/vscode/resources/app/resources/linux/code.png \
 && printf '%s\n' '[Desktop Entry]' \
      'Type=Application' \
      'Name=Visual Studio Code' \
      'GenericName=Text Editor' \
      'Comment=Code Editing. Redefined.' \
      'Exec=code %F' \
      'Icon=/opt/vscode/resources/app/resources/linux/code.png' \
      'StartupNotify=false' \
      'StartupWMClass=code' \
      'Categories=TextEditor;Development;IDE;' \
      'MimeType=text/plain;inode/directory;' > /usr/share/applications/code.desktop \
 && printf 'vscode %s\n' "$(jq -er .productVersion <<< "$release")" >> /usr/local/share/oryn/upstream-versions.txt

# 等宽字体：JetBrains Maple Mono（JetBrains Mono + Maple Mono 合成：中英文 2:1 对齐，带 Nerd Font 字形）。
# fontconfig 把 monospace 指向它，xfce4-terminal、VS Code 的编辑器与终端、mousepad、Chromium 网页里的等宽文本都用它。
# 选 NF-XX-NL-HT 版：带 Nerd 字形；不缩窄中文，保持 2:1 列对齐；不带连字，截图里的 != 仍是两个字符，agent 看屏幕不会误读；
# 带 hinting，Xvnc 1280x800 这种低分辨率下更清楚。只装常规、粗体、斜体、粗斜体四个字重（约 74MB）。
# 与上面 fetch 同一种做法：取上游最新发行版，按 GitHub 发布的摘要校验。
RUN --mount=type=bind,source=oryn-monospace.conf,target=/tmp/oryn-monospace.conf \
    release="$(curl -fsSL https://api.github.com/repos/SpaceTimee/Fusion-JetBrainsMapleMono/releases/latest)" \
 && asset=JetBrainsMapleMono-NF-XX-NL-HT.zip \
 && url="$(jq -er --arg n "$asset" '.assets[] | select(.name == $n) | .browser_download_url' <<< "$release")" \
 && sha="$(jq -er --arg n "$asset" '.assets[] | select(.name == $n) | .digest | select(startswith("sha256:")) | ltrimstr("sha256:")' <<< "$release")" \
 && curl -fsSLo /tmp/jbmm.zip "$url" && echo "$sha  /tmp/jbmm.zip" | sha256sum -c - \
 && unzip -q -j /tmp/jbmm.zip -d /usr/share/fonts/jetbrains-maple-mono JetBrainsMapleMono-{Regular,Bold,Italic,BoldItalic}.ttf \
 && unzip -q -j /tmp/jbmm.zip -d /usr/share/licenses/jetbrains-maple-mono LICENSE.txt \
 && rm /tmp/jbmm.zip \
 && install -m 0644 /tmp/oryn-monospace.conf /etc/fonts/conf.d/55-oryn-monospace.conf \
 && fc-cache -f \
 && [[ "$(fc-match -f '%{family[0]}' monospace)" == "JetBrains Maple Mono" ]] \
 && printf 'jetbrains-maple-mono %s\n' "$(jq -er .tag_name <<< "$release")" >> /usr/local/share/oryn/upstream-versions.txt

# 桌面应用清单（M25）：orbd 读 /usr/local/share/oryn/desktop-apps.json 上报任务栏应用，图标取自各应用自带的文件，
# 由 desktop-icons.sh 解析后复制到 /usr/local/share/oryn/desktop-icons/ 并回写清单的 icon 字段；找不到图标就构建失败。
# LibreOffice：share/registry 下的 xcd 把 ooSetupLastVersion 设成当前版本并关掉每日提示，首次启动不弹欢迎框；
# xcd 写坏会让 LibreOffice 连 --version 都崩溃，所以装完再跑一次。三个文件只经 bind mount 读取，不留在镜像里。
RUN --mount=type=bind,source=desktop-apps.json,target=/tmp/oryn-desktop/desktop-apps.json \
    --mount=type=bind,source=desktop-icons.sh,target=/tmp/oryn-desktop/desktop-icons.sh \
    --mount=type=bind,source=libreoffice-oryn.xcd,target=/tmp/oryn-desktop/libreoffice-oryn.xcd \
    bash /tmp/oryn-desktop/desktop-icons.sh /tmp/oryn-desktop/desktop-apps.json /usr/local/share/oryn \
 && lo="$(libreoffice --version | awk '{ print $2 }' | cut -d. -f1,2)" && [[ "$lo" =~ ^[0-9]+\.[0-9]+$ ]] \
 && sed "s/@VERSION@/$lo/" /tmp/oryn-desktop/libreoffice-oryn.xcd > /usr/lib/libreoffice/share/registry/oryn.xcd \
 && xmllint --noout /usr/lib/libreoffice/share/registry/oryn.xcd \
 && grep -q "<value>$lo</value>" /usr/lib/libreoffice/share/registry/oryn.xcd \
 && libreoffice --version

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

# 设计画布引擎（M29）：引擎源（internal/designengine/engine，不含 node_modules）由构建脚本放进上下文的 design-engine/，
# 按 lock 用 npm ci 预装到 /usr/local/share/oryn/design-engine/<版本>/。版本由构建脚本从 designengine.Version 取来作构建参数，
# 这里按同一算法（各文件 sha256 清单的 sha256 前 12 位）复算，不一致就构建失败，保证 orbd 写出的引擎与预装的 node_modules 同版。
# orbd 把引擎源写到 ~/.oryn/design/engine/<版本>/，node_modules 软链到这里；版本不符时才在引擎目录里 npm ci。
# 最后用一个两帧样例跑一次 vite build：原生依赖（esbuild、rollup、lightningcss、tailwind oxide）按架构装错时这里就失败。
ARG DESIGN_ENGINE_VERSION
RUN --mount=type=bind,source=design-engine,target=/tmp/oryn-design-engine \
    [[ "$DESIGN_ENGINE_VERSION" =~ ^[0-9a-f]{12}$ ]] \
 && dir="/usr/local/share/oryn/design-engine/$DESIGN_ENGINE_VERSION" \
 && install -d "$dir" && cp -a /tmp/oryn-design-engine/. "$dir/" \
 && got="$(cd "$dir" && LC_ALL=C sha256sum $(LC_ALL=C ls) | sha256sum | cut -c1-12)" \
 && [[ "$got" == "$DESIGN_ENGINE_VERSION" ]] \
 && (cd "$dir" && npm ci --no-audit --no-fund --cache /tmp/oryn-npm-cache) \
 && rm -rf /tmp/oryn-npm-cache \
 && chown -R root:root "$dir" && chmod -R a+rX,go-w "$dir" \
 && smoke="$(mktemp -d)" && mkdir -p "$smoke/src/frames" \
 && printf '%s\n' '@theme { --color-accent: #0f766e; }' > "$smoke/src/theme.css" \
 && printf '%s\n' 'import { Link } from "oryn:design";' \
      'export default function A() { return <Link to="b" className="text-accent">b</Link>; }' > "$smoke/src/frames/a.tsx" \
 && printf '%s\n' 'export default function B() { return <p>b</p>; }' > "$smoke/src/frames/b.tsx" \
 && ORYN_DESIGN_DIR="$smoke" node "$dir/node_modules/vite/bin/vite.js" build --config "$dir/vite.config.mjs" --logLevel warn \
 && [[ "$(cd "$smoke/dist" && LC_ALL=C ls | tr '\n' ' ')" == "a.html assets b.html frames.json " ]] \
 && jq -e '.[0].id == "a" and .[0].links == ["b"]' "$smoke/dist/frames.json" \
 && grep -q 'text-accent' "$smoke"/dist/assets/*.css \
 && rm -rf "$smoke" "$dir/node_modules/.vite-temp" \
 && printf 'design-engine %s\n' "$DESIGN_ENGINE_VERSION" >> /usr/local/share/oryn/upstream-versions.txt

# 用户 user（uid 1000，免密 sudo）
RUN useradd --create-home --uid 1000 --shell /bin/bash user \
 && usermod -aG docker user \
 && echo 'user ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/user && chmod 0440 /etc/sudoers.d/user \
 && install -d -o user -g user /home/user/workspace /home/user/.local /home/user/.local/bin \
 && git config --system init.defaultBranch main \
 && git config --system core.pager cat \
 && git config --system color.ui false \
 && git lfs install --system --skip-repo

# 普通进程与登录 shell 都能找到用户安装的工具；不注入 alias 或 BASH_ENV。
# Arch 的 perl 脚本（如 exiftool）装在 /usr/bin/*_perl，原本只由登录 shell 的 perlbin.sh 补进 PATH。
ENV HOME=/home/user
ENV PATH=/home/user/.local/bin:/home/user/.cargo/bin:/home/user/go/bin:$PATH:/usr/bin/site_perl:/usr/bin/vendor_perl:/usr/bin/core_perl
RUN printf '%s\n' 'export PATH="$HOME/.local/bin:$HOME/.cargo/bin:$HOME/go/bin:$PATH"' > /etc/profile.d/oryn-toolchains.sh \
 && printf '%s\n' 'prefix=${HOME}/.local' > /home/user/.npmrc \
 && printf '%s\n' '[ ! -f "$HOME/.profile" ] || . "$HOME/.profile"' \
      '[ ! -f "$HOME/.bashrc" ] || . "$HOME/.bashrc"' > /home/user/.bash_profile \
 && touch /home/user/.profile \
 && chown user:user /home/user/.bash_profile /home/user/.profile /home/user/.npmrc
# 交互 shell：提示符与窗口标题只留工作目录，不带 user@<orb ID>；说明见文件内注释。
COPY --chown=user:user --chmod=0644 bashrc /home/user/.bashrc

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
