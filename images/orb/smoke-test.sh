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
	chromium Xvnc openbox; do
	command -v "$cmd" >/dev/null || { echo "缺少工具：$cmd" >&2; exit 1; }
done

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

mkdir repo
cd repo
git init --quiet
printf 'content\n' > file.txt
git add file.txt
git -c user.name=Smoke -c user.email=smoke@example.invalid commit --quiet -m smoke
[[ "$(git branch --show-current)" == main ]]
[[ "$(git log -1 --format=%s)" == smoke ]]
git lfs version
printf 'Git 初始化 / 提交 / LFS：通过\n'
printf 'orb 镜像离线冒烟：通过\n'
