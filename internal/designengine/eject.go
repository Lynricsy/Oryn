package designengine

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// ejectFS 是「导出为工程」的模板（M29 D2，eject/ 目录）：一个独立的普通 Vite 工程的外壳，
// 与版本里的 src/** 一起打包。含点文件（.oryn/box.yaml、.gitignore），所以用 all: 前缀。
// README.md 与 styles.css 里的占位注释由 ProjectFiles 填写。
//
//go:embed all:eject
var ejectFS embed.FS

// EjectFile 是导出工程里的一个文件：Path 相对工程根，以 / 分隔。
type EjectFile struct {
	Path string
	Data []byte
}

// ProjectFrame 是导出工程里的一帧：ID 是 src/frames/<ID>.tsx 的文件名，Name 是画布上画框的名字。
type ProjectFrame struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Project 是生成导出工程外壳所需的信息。
type Project struct {
	Title string
	// Start 是起始帧 id；空 = 第一帧。
	Start string
	// Frames 按画布顺序排列。
	Frames []ProjectFrame
	// Theme 报告版本里有没有 src/theme.css。
	Theme bool
	// Conflicts 是设计级依赖覆盖引擎依赖的各项（见 ProjectConflicts），列进 README。
	Conflicts []Conflict
}

// 模板里的占位（整行替换）。
const (
	readmeTitle     = "<!-- oryn:title -->"
	readmeConflicts = "<!-- oryn:conflicts -->\n"
	stylesTheme     = "/* oryn:theme */\n"
)

// FramesFile 是导出工程里记录帧顺序、名字与起始帧的文件（main.tsx 读它）。
const FramesFile = "oryn-frames.json"

// ProjectFiles 返回导出工程的外壳文件（按路径字节序）：eject/ 下的模板（README 填上标题与依赖冲突，styles.css 在有
// src/theme.css 时引入它）与 oryn-frames.json。不含 package.json / package-lock.json（见 ProjectManifest、ProjectLock）
// 与版本里的 src/**。
func ProjectFiles(p Project) []EjectFile {
	var out []EjectFile
	_ = fs.WalkDir(ejectFS, "eject", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(ejectFS, name)
		if err != nil {
			return err
		}
		rel := name[len("eject/"):]
		switch rel {
		case "README.md":
			data = readme(data, p)
		case "styles.css":
			theme := ""
			if p.Theme {
				theme = "@import \"./src/theme.css\";\n"
			}
			data = bytes.Replace(data, []byte(stylesTheme), []byte(theme), 1)
		}
		out = append(out, EjectFile{Path: rel, Data: data})
		return nil
	})
	frames := p.Frames
	if frames == nil {
		frames = []ProjectFrame{}
	}
	meta := struct {
		Title  string         `json:"title"`
		Start  string         `json:"start"`
		Frames []ProjectFrame `json:"frames"`
	}{oneLine(p.Title), p.Start, frames}
	out = append(out, EjectFile{Path: FramesFile, Data: indentJSON(meta)})
	slices.SortFunc(out, func(a, b EjectFile) int { return strings.Compare(a.Path, b.Path) })
	return out
}

func readme(tpl []byte, p Project) []byte {
	title := oneLine(p.Title)
	if title == "" {
		title = "Oryn 设计稿"
	}
	out := bytes.Replace(tpl, []byte(readmeTitle), []byte(title), 1)
	var c strings.Builder
	if len(p.Conflicts) > 0 {
		c.WriteString("\n## 依赖版本冲突\n\n设计级依赖（`src/package.json`）与画布引擎同名、版本不同，`package.json` 采用了设计级的版本：\n\n")
		c.WriteString("| 包 | 引擎版本 | 采用的版本 |\n|---|---|---|\n")
		for _, x := range p.Conflicts {
			fmt.Fprintf(&c, "| `%s` | `%s` | `%s` |\n", x.Name, x.Engine, x.Design)
		}
	}
	return bytes.Replace(out, []byte(readmeConflicts), []byte(c.String()), 1)
}

// oneLine 把标题压成一行（README 标题与页面标题用）。
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// EnginePackageJSON 返回嵌入的引擎 package.json（engine/package.json）原文；调用方不得修改返回的切片。
func EnginePackageJSON() []byte {
	b, _ := fs.ReadFile(engine, "engine/package.json")
	return b
}

// engineLock 返回嵌入的引擎 package-lock.json 原文。
func engineLock() []byte {
	b, _ := fs.ReadFile(engine, "engine/package-lock.json")
	return b
}
