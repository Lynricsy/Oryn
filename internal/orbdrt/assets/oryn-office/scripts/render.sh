#!/usr/bin/env bash
# 把 Word / Excel / PowerPoint 文件或 PDF 渲染成逐页 PNG，交付前逐页查看用。
# 用法：bash render.sh <文件> [输出目录]
# 输出目录缺省为文件旁边的 <文件名>-pages/；先清掉其中旧的 page-*.png，再按页写 page-<页号>.png，最后逐行打印页图路径。
# Office 文件先经 LibreOffice 无头转成 PDF（用一次性的用户配置目录，不与正在运行的 LibreOffice 抢锁），再用 pdftoppm 按 80 DPI 出图。
set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
	echo "用法：bash render.sh <文件> [输出目录]" >&2
	exit 2
fi
src=$1
if [[ ! -f $src ]]; then
	echo "找不到文件：$src" >&2
	exit 2
fi
name=$(basename -- "$src")
stem=${name%.*}
out=${2:-$(dirname -- "$src")/$stem-pages}

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT

case ${name,,} in
*.pdf)
	pdf=$src
	;;
*)
	soffice -env:UserInstallation="file://$work/profile" --headless --convert-to pdf --outdir "$work" "$src" >"$work/soffice.log" 2>&1 || true
	pdf=$work/$stem.pdf
	if [[ ! -s $pdf ]]; then
		echo "LibreOffice 没能把 $src 转成 PDF：" >&2
		cat "$work/soffice.log" >&2
		exit 1
	fi
	;;
esac

mkdir -p -- "$out"
rm -f -- "$out"/page-*.png
pdftoppm -png -r 80 "$pdf" "$out/page"
ls -1 -- "$out"/page-*.png
