# 第三方来源说明

`oryn-design` 技能由 Oryn 编写。下列来源只取思路、流程或数据，正文用 Oryn 自己的话重写，没有整段照搬。

## oil-ui（流程改写）

第 2 节「流程」（产品事实 → 每帧一个主角与主动作 → 灰度结构 → 默认一个推荐方向，用户明说才比较多个方向）
改写自 oil-ui 的主流程；删去了版本检查、截图脚本、任务工作区、生图与评审派发等与 Oryn 画布无关的部分。

- 仓库：https://github.com/oil-oil/oil-ui
- 许可证：MIT，Copyright (c) 2026 oil-oil

## HyperSkills frontend-design（规则阈值）

第 2 节末尾的硬规则与第 5 节规则表的阈值（文本对比度 4.5:1 / 大字 3:1、可点目标 24×24 CSS px、可见文案不用长短
破折号、小号大写标签不超过 `ceil(区块数 / 3)`、一个强调色、一套圆角刻度、不连续三段图文分栏、组件里用语义 token
而不是颜色字面量）参考 HyperSkills 的 `frontend-design` 技能，并下沉为 Oryn 设计探针的规则。

- 仓库：HyperSkills，`skills/frontend-design`（其上游出处见该技能的 NOTICE.md）
- 许可证：MIT，Copyright (c) 2026 Lynricsy

## taste-skill（方向库数据）

`design_directions` 发牌所用的方向库（色板、字体对、落地页版式）裁剪、改写自 taste-skill：

- 仓库：https://github.com/Leonxlnx/taste-skill
- 上游 commit：`6c0b5c78f5d31776e2210305abaa210cdc0030bf`（2026-11-22）
- 文件：`skills/taste-skill/library/palettes.json`（64 → 40）、`skills/taste-skill/library/type.json`（39 → 20）、
  `skills/taste-skill/assets/layout/heroes.json`（11 → 9）
- 改动：字段改名、按底色亮度补 `mode`、去掉不参与发牌的字段；字体家族、色值、标签、行业与说明文字保持上游原样。
  行业、调性的别名表与 `app` 面的 4 种版式由 Oryn 编写。数据所在位置与完整说明见 `internal/artifacts/design/NOTICE.md`。
- 许可证：MIT，Copyright (c) 2026 Leonxlnx

## Open Vetta layout-probe（规则思路）

第 5 节规则表前半（`text-clipped`、`unintended-wrap`、`icon-missing`、`edge-misaligned`、`token-undefined`）的
判据思路参考 Open Vetta 的 layout-probe。只取判据、由 Oryn 独立实现，没有查看或复制其源码。

## MIT 许可证全文

上面三个 MIT 来源的许可证文本相同（版权行见各节）：

```
MIT License

Copyright (c) <year> <copyright holders>

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
