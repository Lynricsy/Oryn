#!/bin/sh
# 审阅用拼图：把一个界面在 6 个 project 下的 before / after 截图拼成一张（每行一个 project，左 before 右 after）。
#   sh e2e/ui-audit/sheet.sh <界面 id> [输出文件，默认 /tmp/ui-audit-sheet-<id>.png] [project 前缀过滤，如 d1366]
set -eu
id="$1"
out="${2:-/tmp/ui-audit-sheet-$id.png}"
filter="${3:-}"
dir="$(dirname "$0")/../../../docs/ui-audit/shots/$id"
files=""
for p in d1920-light d1920-dark d1366-light d1366-dark m390-light m390-dark; do
	case "$p" in "$filter"*) ;; *) continue ;; esac
	for phase in before after; do
		f="$dir/$p-$phase.png"
		[ -f "$f" ] && files="$files $f" || files="$files null:"
	done
done
# shellcheck disable=SC2086
montage -label '%t' $files -tile 2x -geometry 720x720+6+6 -pointsize 14 "$out"
echo "$out"
