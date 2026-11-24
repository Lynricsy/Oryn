// Package designengine 嵌入设计画布引擎（M29）：vite 配置、帧插件、babel 打标插件、帧入口与 Link 组件，
// 以及锁定版本的 package.json / package-lock.json。orbd 把它写到 ~/.oryn/design/engine/<Version>/，
// 依赖优先软链到镜像预装的 /usr/local/share/oryn/design-engine/<Version>/node_modules（docs/plan-m29.md C3）。
//
// 设计源码的导入写法：import { Link } from "oryn:design"。
package designengine

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Version 是引擎版本：嵌入文件的内容指纹，取下列清单的 sha256 前 12 位 hex——
// 按文件名字节序，每个文件一行 `<文件 sha256 hex>  <文件名>\n`（即在引擎目录里
// `LC_ALL=C sha256sum $(LC_ALL=C ls)` 的输出）。
//
// 手写常量而不在运行时计算：镜像构建脚本从本文件精确匹配取值作构建参数，Dockerfile 再按同一算法复算核对；
// TestVersionMatchesContent 保证它与嵌入内容一致，改了引擎文件而没更新这里时测试失败并给出新值。
const Version = "efee1556f53a"

// files 是引擎源的全部文件（引擎目录下，无子目录）。
var files = []string{
	"babel-loc.mjs",
	"entry.tsx",
	"link.tsx",
	"package-lock.json",
	"package.json",
	"plugin-frames.mjs",
	"styles.css",
	"vite.config.mjs",
}

//go:embed engine/babel-loc.mjs engine/entry.tsx engine/link.tsx engine/package-lock.json engine/package.json
//go:embed engine/plugin-frames.mjs engine/styles.css engine/vite.config.mjs
var engine embed.FS

// WriteTo 把引擎源写到 dir（不存在则创建）；已有同名文件被覆盖，dir 里的其它文件（如 node_modules）不动。
func WriteTo(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("designengine: %w", err)
	}
	for _, name := range files {
		data, err := fs.ReadFile(engine, "engine/"+name)
		if err != nil {
			return fmt.Errorf("designengine: %w", err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return fmt.Errorf("designengine: %w", err)
		}
	}
	return nil
}
