#!/usr/bin/env bash
# 以镜像默认用户离线执行；临时项目退出后删除，不依赖宿主挂载或外部服务。
set -Eeuo pipefail
trap 'echo "orb 镜像冒烟失败：第 $LINENO 行" >&2' ERR

[[ "$(id -u)" == 1000 && "$HOME" == /home/user ]]
[[ -w "$HOME/workspace" && -w "$HOME/.local/bin" ]]
sudo -n true
work="$(mktemp -d)"
cp "$HOME/.profile" "$work/profile"
trap 'cp "$work/profile" "$HOME/.profile"; rm -rf "$work"' EXIT
cd "$work"

# 工具必须已在 PATH 中；仅安装一个下载器或版本管理器不能算预装完成。
for cmd in omp orbd oryn node npm npx pnpm bun bunx python python3 pip pipx uv uvx ruff \
	go rustc cargo rustfmt cargo-clippy gh yq ruby bundle php composer lua \
	cc c++ make pkg-config cmake ninja autoconf automake libtoolize bison flex gdb strace ltrace \
	shellcheck shfmt git git-lfs ssh scp rsync curl wget openssl \
	bash sh awk sed grep find xargs diff patch file tree xxd dos2unix jq rg fd \
	zip unzip tar gzip bzip2 zstd xz 7z bc sponge timeout flock setsid \
	ps pgrep pkill killall lsof free ip ss ping dig nc socat traceroute \
	sqlite3 psql mariadb redis-cli xmllint xsltproc pdftotext pdfinfo qpdf convert ffmpeg ffprobe \
	clangd rust-analyzer gopls tsc typescript-language-server pyright-langserver bash-language-server \
	lua-language-server ruby-lsp phpactor yaml-language-server vscode-json-language-server \
	vscode-css-language-server vscode-html-language-server docker-langserver marksman \
	nano htop ncdu bat parallel pv unrar cabextract whois nmap iperf3 \
	pandoc soffice gs mutool img2pdf tesseract magick rsvg-convert cwebp avifenc cjxl heif-enc \
	optipng pngquant jpegoptim gifsicle exiftool dot qrencode zbarimg sox soxi mediainfo \
	yt-dlp aria2c http mlr duckdb \
	chromium Xvnc openbox docker dockerd containerd \
	xfce4-terminal mousepad pcmanfm code dbus-daemon xsetroot; do
	command -v "$cmd" >/dev/null || { echo "缺少工具：$cmd" >&2; exit 1; }
done

# 嵌套 Docker（M11a）：客户端与插件随镜像预装，user 在 docker 组里；守护进程只在 sysbox 类型里由 orbd 拉起
docker --version
docker compose version
docker buildx version
id -nG | tr ' ' '\n' | grep -qx docker

# 桌面应用（M25）：清单按任务栏顺序列出六个应用，字段齐全，图标文件都在镜像里；VS Code 是官方发行版。
apps=/usr/local/share/oryn/desktop-apps.json
jq -e '
	map(.id) == ["chromium", "terminal", "vscode", "files", "editor", "libreoffice"]
	and all(.[];
		(.id | test("^[a-z][a-z0-9-]{0,31}$"))
		and (.name | type == "string" and length > 0 and length <= 64)
		and (.exec | type == "array" and all(.[]; type == "string")) and (.id == "chromium" or (.exec | length > 0))
		and (.wmClass | type == "array" and length > 0 and all(.[]; type == "string" and length > 0))
		and (.desktopFile | type == "string" and endswith(".desktop"))
		and (.icon | type == "string" and test("^[a-z][a-z0-9-]*\\.(svg|png)$")))
' "$apps" >/dev/null
while IFS=$'\t' read -r id icon desktop; do
	[[ "$icon" == "$id".* ]]
	test -s "/usr/local/share/oryn/desktop-icons/$icon"
	test -f "/usr/share/applications/$desktop"
done < <(jq -r '.[] | [.id, .icon, .desktopFile] | @tsv' "$apps")
[[ "$(code --version | sed -n 1p)" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]
# LibreOffice 欢迎框靠 share/registry 下的 oryn.xcd 关掉：ooSetupLastVersion 必须等于已装版本的「主版本.次版本」。
lo="$(pacman -Q libreoffice-fresh | awk '{ print $2 }' | cut -d. -f1,2)"
grep -q "<value>$lo</value>" /usr/lib/libreoffice/share/registry/oryn.xcd
[[ "${GLYCIN_DISABLE_SANDBOX:-}" == i-know-the-risks ]] # glycin 误判 bwrap 可用的绕过，见 Dockerfile
printf '桌面应用清单 / 图标 / VS Code：通过\n'

# 等宽字体：monospace（含中文语言环境）落到 JetBrains Maple Mono，它同时带中文与 Nerd 字形。
for pattern in monospace 'monospace:lang=zh-cn'; do
	[[ "$(fc-match -f '%{family[0]}' "$pattern")" == "JetBrains Maple Mono" ]]
done
[[ "$(fc-list ':charset=4e2d e0a0' family)" == *'JetBrains Maple Mono'* ]]
# 交互 shell：提示符与窗口标题不带 user@主机名，PROMPT_COMMAND 的其它项保留。
shell="$(TERM=xterm-256color bash -lic 'printf "%s\n" "$PS1"; declare -p PROMPT_COMMAND' 2>/dev/null)"
[[ "$shell" == *'\w'* && "$shell" != *'\h'* && "$shell" != *'%s@%s'* ]]
[[ "$shell" == *__oryn_title* && "$shell" == *__systemd_osc_context_precmdline* ]]
printf '等宽字体 / 交互提示符：通过\n'

# BlackArch 源：排在官方源之后，keyring 与同步库已就位；镜像里不留 pacman 本地私钥。
[[ "$(pacman-conf --repo-list | tail -n 1)" == blackarch ]]
pacman -Q blackarch-keyring blackarch-mirrorlist >/dev/null
test -s /var/lib/pacman/sync/blackarch.db
sudo -n pacman-key --list-keys 4345771566D76038C7FEB43863EC0ADBEA87E4E3 >/dev/null
[[ -z "$(sudo -n find /etc/pacman.d/gnupg/private-keys-v1.d -mindepth 1 -print -quit)" ]]

# 不依赖交互启动文件补 PATH，且登录/交互 shell 不替换标准命令。
for mode in -c -lc -ic; do
	bash "$mode" 'set -eu
		for cmd in ls cat grep sed find; do test "$(type -t "$cmd")" = file; done
		test -z "$(alias)"
		for cmd in go cargo uv pnpm; do command -v "$cmd" >/dev/null; done
		test "$(npm prefix -g)" = "$HOME/.local"
		printf "ok\n" > "$HOME/.local/bin/oryn-smoke-path"
		chmod +x "$HOME/.local/bin/oryn-smoke-path"
		command -v oryn-smoke-path >/dev/null
		rm "$HOME/.local/bin/oryn-smoke-path"'
done
printf 'shell / PATH / sudo：通过\n'
printf '%s\n' 'export ORYN_SMOKE_PROFILE=ok' >> "$HOME/.profile"
bash -lc 'test "$ORYN_SMOKE_PROFILE" = ok'

# 本地全局包安装必须无需 sudo；不通过联网安装掩盖目录权限问题。
mkdir npm-tool
printf '%s\n' '{"name":"oryn-smoke-tool","version":"1.0.0","bin":{"oryn-smoke-tool":"cli.js"}}' > npm-tool/package.json
printf '%s\n' '#!/usr/bin/env node' 'console.log("ok")' > npm-tool/cli.js
npm install --global --offline --ignore-scripts --no-audit --no-fund ./npm-tool
[[ "$(oryn-smoke-tool)" == ok ]]
npm uninstall --global --offline --ignore-scripts --no-audit --no-fund oryn-smoke-tool
[[ "$(node -e 'console.log(6*7)')" == 42 ]]
[[ "$(bun -e 'console.log(6*7)')" == 42 ]]
pnpm --version
python -m venv venv
venv/bin/python -m pip --version
[[ "$(venv/bin/python -c 'import sqlite3, ssl; print(sqlite3.connect(":memory:").execute("select 6*7").fetchone()[0])')" == 42 ]]
printf 'JavaScript / 用户全局安装 / Python venv：通过\n'

printf '%s\n' '#include <stdio.h>' 'int main(void) { puts("42"); return 0; }' > main.c
cc -Wall -Werror main.c -o c-smoke
[[ "$(./c-smoke)" == 42 ]]
printf '%s\n' '#include <iostream>' 'int main() { std::cout << 42 << std::endl; }' > main.cpp
c++ -Wall -Werror main.cpp -o cpp-smoke
[[ "$(./cpp-smoke)" == 42 ]]
printf '%s\n' 'package main' 'import "fmt"' 'func main() { fmt.Println(42) }' > main.go
[[ "$(go run main.go)" == 42 ]]
mkdir -p rust/src
printf '%s\n' '[package]' 'name = "orb-smoke"' 'version = "0.1.0"' 'edition = "2024"' > rust/Cargo.toml
printf '%s\n' 'fn main() {' '    println!("42");' '}' > rust/src/main.rs
cargo fmt --manifest-path rust/Cargo.toml --check
cargo clippy --manifest-path rust/Cargo.toml --offline -- -D warnings
[[ "$(cargo run --manifest-path rust/Cargo.toml --offline --quiet)" == 42 ]]
[[ "$(ruby -e 'puts 6*7')" == 42 ]]
php >php.stdout 2>php.stderr <<'PHP'
<?php
foreach (["curl", "iconv", "mbstring", "openssl", "pdo_sqlite", "sqlite3", "zip"] as $extension) {
    if (!extension_loaded($extension)) {
        fwrite(STDERR, "缺少 PHP 扩展：$extension\n");
        exit(1);
    }
}
echo (new PDO("sqlite::memory:"))->query("select 6*7")->fetchColumn();
PHP
[[ "$(cat php.stdout)" == 42 && ! -s php.stderr ]]
[[ "$(lua -e 'print(6*7)')" == 42 ]]
printf 'C / C++ / Go / Rust / Ruby / PHP / Lua：通过\n'

# 语言服务器：按 omp 内置 LSP 配置的命令与参数启动，完成 initialize / shutdown 握手才算可用。
test -d "$(rustc --print sysroot)/lib/rustlib/src/rust/library/std"
mkdir lsp
python3 - "$work/lsp" <<'PY'
import json, os, pathlib, subprocess, sys, threading

root = pathlib.Path(sys.argv[1])
servers = {
    "clangd": ["clangd", "--background-index", "--clang-tidy", "--header-insertion=iwyu"],
    "rust-analyzer": ["rust-analyzer"],
    "gopls": ["gopls", "serve"],
    "typescript-language-server": ["typescript-language-server", "--stdio"],
    "pyright": ["pyright-langserver", "--stdio"],
    "ruff": ["ruff", "server"],
    "bashls": ["bash-language-server", "start"],
    "lua-language-server": ["lua-language-server"],
    "ruby-lsp": ["ruby-lsp"],
    "phpactor": ["phpactor", "language-server"],
    "yamlls": ["yaml-language-server", "--stdio"],
    "vscode-json-language-server": ["vscode-json-language-server", "--stdio"],
    "vscode-css-language-server": ["vscode-css-language-server", "--stdio"],
    "vscode-html-language-server": ["vscode-html-language-server", "--stdio"],
    "dockerls": ["docker-langserver", "--stdio"],
    "marksman": ["marksman", "server"],
}

def send(proc, msg):
    body = json.dumps(msg).encode()
    proc.stdin.write(b"Content-Length: %d\r\n\r\n" % len(body) + body)
    proc.stdin.flush()

def receive(proc, want_id):
    while True:
        length = None
        while True:
            line = proc.stdout.readline()
            if not line:
                raise RuntimeError(f"进程在响应 {want_id} 前退出")
            line = line.strip()
            if not line:
                break
            key, _, value = line.decode().partition(":")
            if key.lower() == "content-length":
                length = int(value)
        msg = json.loads(proc.stdout.read(length))
        if "method" in msg:
            if "id" in msg:
                send(proc, {"jsonrpc": "2.0", "id": msg["id"], "result": None})
            continue
        if msg.get("id") == want_id:
            if "error" in msg:
                raise RuntimeError(f"响应 {want_id} 出错：{msg['error']}")
            return msg["result"]

failed = []
for name, cmd in servers.items():
    proc = subprocess.Popen(cmd, cwd=root, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    timer = threading.Timer(180, proc.kill)
    timer.start()
    try:
        uri = root.as_uri()
        send(proc, {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
            "processId": os.getpid(), "rootUri": uri, "capabilities": {},
            "workspaceFolders": [{"uri": uri, "name": "lsp"}]}})
        result = receive(proc, 1)
        if not isinstance(result, dict) or "capabilities" not in result:
            raise RuntimeError("initialize 结果缺少 capabilities")
        send(proc, {"jsonrpc": "2.0", "method": "initialized", "params": {}})
        send(proc, {"jsonrpc": "2.0", "id": 2, "method": "shutdown"})
        receive(proc, 2)
        # 可用性以 shutdown 应答为准；lua-language-server 在 initialized 后立即收到 exit 时不会退出，交给 finally 结束进程。
        send(proc, {"jsonrpc": "2.0", "method": "exit"})
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            pass
    except Exception as exc:
        failed.append(f"{name}: {exc}")
    finally:
        timer.cancel()
        if proc.poll() is None:
            proc.kill()
        proc.wait()
if failed:
    sys.exit("LSP 握手失败：\n" + "\n".join(failed))
print(f"语言服务器 initialize 握手（{len(servers)} 个）：通过")
PY

[[ "$(printf 'answer: 42\n' | yq -r '.answer')" == 42 ]]
[[ "$(printf '{"answer":42}\n' | jq -r '.answer')" == 42 ]]
[[ "$(sqlite3 :memory: 'select 6*7;')" == 42 ]]
printf '<root><answer>42</answer></root>\n' > input.xml
[[ "$(xmllint --xpath 'string(/root/answer)' input.xml)" == 42 ]]
printf 'archive\n' > input.txt
tar -czf input.tar.gz input.txt
mkdir unpacked
tar -xzf input.tar.gz -C unpacked
cmp input.txt unpacked/input.txt
magick -size 2x2 xc:white image.png
ffprobe -v error -select_streams v:0 -show_entries stream=width,height -of csv=p=0 image.png | grep -qx '2,2'
printf 'YAML / JSON / SQLite / XML / 归档 / 图像：通过\n'

# 文档、OCR、图片、音视频与数据处理：全部离线完成一次真实转换。
fonts=/usr/share/fonts/liberation/LiberationSans-Regular.ttf
printf '# 42\n' | pandoc -f markdown -t html | grep -q '>42</h1>'
printf '42\n' > doc.txt
soffice --headless --convert-to pdf --outdir . doc.txt >/dev/null
[[ "$(pdftotext doc.pdf - | tr -d '\f' | sed '/^$/d')" == 42 ]]
gs -q -dSAFER -dBATCH -dNOPAUSE -sDEVICE=png16m -r50 -o gs.png doc.pdf
mutool draw -q -o mu.png doc.pdf 1
magick -size 360x180 xc:white -font "$fonts" -pointsize 120 -fill black -annotate +40+140 42 ocr.png
[[ "$(tesseract ocr.png - --psm 7 2>/dev/null | tr -d '[:space:]')" == 42 ]]
[[ "$(magick ocr.png -resize 50% -format '%w' info:)" == 180 ]]
img2pdf ocr.png -o img.pdf
pdfinfo img.pdf | grep -q '^Pages: *1$'
printf '%s\n' '<svg xmlns="http://www.w3.org/2000/svg" width="4" height="4"><rect width="4" height="4"/></svg>' > image.svg
rsvg-convert image.svg -o svg.png
printf 'digraph{a->b}\n' | dot -Tsvg | grep -q '<svg'
cwebp -quiet ocr.png -o ocr.webp
avifenc -q 50 ocr.png ocr.avif >/dev/null
cjxl ocr.png ocr.jxl --quiet
heif-enc ocr.png -o ocr.heic >/dev/null
cp ocr.png opt.png
optipng -quiet opt.png
pngquant --force --output quant.png ocr.png
magick ocr.png ocr.jpg
jpegoptim --quiet ocr.jpg
magick ocr.png ocr.gif
gifsicle -O3 ocr.gif -o opt.gif
[[ "$(exiftool -s3 -ImageWidth ocr.png)" == 360 ]]
qrencode -o qr.png 42
[[ "$(zbarimg --quiet --raw qr.png)" == 42 ]]
sox -n -r 8000 tone.wav synth 0.1 sine 440
[[ "$(soxi -r tone.wav)" == 8000 ]]
[[ "$(mediainfo --Inform='Audio;%SamplingRate%' tone.wav)" == 8000 ]]
[[ "$(duckdb -csv -noheader -c 'select 6*7')" == 42 ]]
[[ "$(printf 'a\n42\n' | mlr --icsv --onidx cat)" == 42 ]]
yt-dlp --version >/dev/null
aria2c --version >/dev/null
http --version >/dev/null
[[ "$(python3 - <<'PY'
import matplotlib
matplotlib.use("Agg")
import bs4, docx, lxml, numpy, openpyxl, pandas, PIL, pypdf, reportlab, requests, scipy, xlsxwriter, yaml
import matplotlib.pyplot as plt
plt.plot([0, 1], [0, 1])
plt.savefig("plot.png")
print(int(pandas.DataFrame({"a": numpy.array([6])}).a.iloc[0] * 7))
PY
)" == 42 ]]
printf '文档 / OCR / 图片 / 音频 / 数据 / Python 库：通过\n'

mkdir repo
cd repo
git init --quiet
printf 'content\n' > file.txt
git add file.txt
git -c user.name=Smoke -c user.email=smoke@example.invalid commit --quiet -m smoke
[[ "$(git branch --show-current)" == main ]]
[[ "$(git log -1 --format=%s)" == smoke ]]
git lfs version
[[ "$(git config --system --get filter.lfs.required)" == true ]]
[[ "$(git config --system --get filter.lfs.process)" == "git-lfs filter-process" ]]
git lfs version | grep -q '^git-lfs/3\.'
printf 'Git 初始化 / 提交 / LFS：通过\n'
printf 'orb 镜像离线冒烟：通过\n'
