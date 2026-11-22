#!/usr/bin/env bash
# 构建时运行：为桌面应用清单里的每一项找到已安装应用自带的图标，复制进镜像并回写清单的 icon 字段。
#
# 用法：desktop-icons.sh <源清单> <输出目录>
# 产出：<输出目录>/desktop-apps.json（每项带 icon）与 <输出目录>/desktop-icons/<id>.<svg|png>。
#
# 图标取自 /usr/share/applications/<desktopFile> 的 [Desktop Entry] 段里的 Icon=：
# 绝对路径直接用；否则依次找 hicolor 的 scalable SVG、256/128/64/48 的 PNG，
# 再找 Adwaita / AdwaitaLegacy 主题（GTK 默认主题，pcmanfm 的 system-file-manager 只在 AdwaitaLegacy 里有），
# 最后找 /usr/share/pixmaps/<名>.png。任何一项找不到图标都以非零退出，让构建失败。
set -euo pipefail

src="$1"
out="$2"
icons="$out/desktop-icons"
apps=/usr/share/applications

# 按主题与尺寸的优先顺序找名为 $1 的图标，打印第一个存在的文件路径。
find_icon() {
	local name="$1" theme size f
	for theme in hicolor Adwaita AdwaitaLegacy; do
		f="/usr/share/icons/$theme/scalable/apps/$name.svg"
		[[ -f "$f" ]] && { echo "$f"; return; }
		for size in 256x256 128x128 64x64 48x48; do
			# hicolor 只看 apps；Adwaita 系列按上下文分目录（AdwaitaLegacy 放在 legacy/），都接受。
			for f in "/usr/share/icons/$theme/$size/"*"/$name.png"; do
				[[ -f "$f" ]] && { echo "$f"; return; }
			done
		done
	done
	f="/usr/share/pixmaps/$name.png"
	[[ -f "$f" ]] && { echo "$f"; return; }
	return 1
}

install -d "$icons"
manifest="$(jq -c . "$src")"
count="$(jq -r 'length' <<<"$manifest")"
for ((i = 0; i < count; i++)); do
	id="$(jq -r --argjson i "$i" '.[$i].id' <<<"$manifest")"
	desktop="$apps/$(jq -r --argjson i "$i" '.[$i].desktopFile' <<<"$manifest")"
	[[ -f "$desktop" ]] || { echo "$id：缺少桌面项 $desktop" >&2; exit 1; }
	name="$(awk -F= '/^\[/ { main = ($0 == "[Desktop Entry]") } main && $1 == "Icon" { print substr($0, 6); exit }' "$desktop")"
	[[ -n "$name" ]] || { echo "$id：$desktop 没有 Icon=" >&2; exit 1; }
	if [[ "$name" == /* ]]; then
		[[ -f "$name" ]] || { echo "$id：图标 $name 不存在" >&2; exit 1; }
		file="$name"
	else
		file="$(find_icon "$name")" || { echo "$id：找不到图标 $name" >&2; exit 1; }
	fi
	case "$file" in
	*.svg) ext=svg ;;
	*.png) ext=png ;;
	*) echo "$id：不支持的图标格式 $file" >&2; exit 1 ;;
	esac
	install -m 0644 "$file" "$icons/$id.$ext"
	manifest="$(jq -c --argjson i "$i" --arg icon "$id.$ext" '.[$i].icon = $icon' <<<"$manifest")"
	printf '%s\t%s\n' "$id" "$file"
done
jq . <<<"$manifest" > "$out/desktop-apps.json"
chmod 0644 "$out/desktop-apps.json"
