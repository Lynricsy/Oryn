package artifacts

import (
	"strings"
)

// 教程主题（read_artifact_guide 的 topic）。
const (
	GuideFiles    = "files"
	GuideCanvas   = "canvas"
	GuideRuntime  = "runtime"
	GuideDocument = "document"
	GuideSlides   = "slides"
)

// guideTopics 是全部合法的主题（按说明顺序）。
var guideTopics = []string{GuideFiles, GuideCanvas, GuideRuntime, GuideDocument, GuideSlides}

// guides 是已写好的教程；合法但不在这里的主题在当前版本不可用。
var guides = map[string]string{
	GuideFiles: guideFiles,
}

// guideFiles 是 files 主题：artifact 的文件、上限、沙箱与改动流程。
var guideFiles = `# Artifact 的文件

artifact 是 Oryn 替用户保存的版本化交付物：报告、原型、设计稿这类做好给人看的成品。它是一组文件，每次写入产生一个新版本，用户能看到全部历史并恢复任何一版。

## 入口与相对路径

- 普通页面的入口是 index.html；用户打开 artifact 时显示它。
- 其它文件（样式、脚本、图片、数据）和 index.html 放在同一个 artifact 里，页面里用相对路径引用，例如 <link rel="stylesheet" href="style.css">、<img src="images/chart.png">。不要写以 / 开头的绝对路径。
- 路径规则：相对路径，用 / 分隔；不能有空段、. 或 ..；不能含 \ 和 : * ? " < > | 与控制字符；每段不以点或空格结尾；不能以 _oryn 开头；同一版本里不能有两个只差大小写的路径。
- .html / .htm 文件必须是 UTF-8。

## content 与 source

create_artifact 与 update_artifact 的 files 里，每个文件给 content 或 source，恰好一个：

- content：直接写文件的文本内容，不超过 5 MiB。只想建一个单页时，也可以只给 html（等于 files 里的 {path: "index.html", content}）。
- source：你所在环境里一个已有文件的路径（例如生成好的图片、打包后的脚本、大数据文件），Oryn 会把它的字节取过来。单文件不超过 20 MiB。

其它上限：每个版本最多 256 个文件、合计不超过 64 MiB；标题不超过 120 字；note（版本备注）不超过 200 字。每个线程每分钟最多写 60 次。

## 页面在隔离的沙箱里显示

页面运行在一个与 Oryn 和任何网站都不同源的沙箱里：

- 不能用 localStorage、sessionStorage、IndexedDB 和 cookie。
- Worker 只能用 blob: 脚本创建。
- 访问不了你所在环境里的服务（localhost 上的开发服务器、数据库等都连不上），也访问不了 Oryn 的接口。数据要写进 artifact 的文件里。
- 不能用摄像头、麦克风、定位，不能弹窗，不能跳转到别的网站；外部链接会被拦下，由用户决定是否在浏览器里打开。
- 除了 artifact 自己的文件，只能从下面这些公共 CDN 加载脚本、样式和字体：
` + "  " + strings.Join(CDNs, "\n  ") + `
- 图标请直接内联成 <svg>，不要依赖图标字体或其它网站的图片。

## 修改流程

1. 改之前先 read_artifact，记下返回的 version。
2. update_artifact 时把它作为 baseVersion 传回。小改动用 edits（oldText 必须在文件里恰好出现一次，可以连续给多条，按顺序应用）；整文件替换或增删文件用 files（删除写 {path, remove: true}）。
3. 只改标题时只给 title，不产生新版本。
4. 如果收到 conflict：说明你要改的文件在 baseVersion 之后被别人（用户或其它线程）改过。重新 read_artifact，在最新版本上重新做你的改动，不要直接覆盖。
5. 改错了可以用 list_artifact_versions 查看历史，再用 restore_artifact_version 把旧版本恢复成新的最新版本。
`

// guideText 返回主题的教程文本。
func guideText(topic string) (string, error) {
	if text, ok := guides[topic]; ok {
		return text, nil
	}
	for _, t := range guideTopics {
		if t == topic {
			return "", opErr(CodeInvalid, "这个主题在当前版本不可用。可用的主题：%s。", strings.Join(availableGuides(), "、"))
		}
	}
	return "", opErr(CodeInvalid, "没有主题 %q。topic 只能是 %s 之一。", topic, strings.Join(guideTopics, "、"))
}

func availableGuides() []string {
	var out []string
	for _, t := range guideTopics {
		if _, ok := guides[t]; ok {
			out = append(out, t)
		}
	}
	return out
}
