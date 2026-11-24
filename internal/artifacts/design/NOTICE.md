# 第三方来源说明

## taste-skill（方向库数据）

`library.json` 中的色板（`palettes`，40 个）、字体对（`types`，20 个）与 `surface: "landing"` 的版式（`layouts`，9 个）
裁剪、改写自 taste-skill：

- 仓库：https://github.com/Leonxlnx/taste-skill
- 上游 commit：`6c0b5c78f5d31776e2210305abaa210cdc0030bf`（2026-11-22）
- 文件：`skills/taste-skill/library/palettes.json`（64 → 40）、`skills/taste-skill/library/type.json`（39 → 20）、
  `skills/taste-skill/assets/layout/heroes.json`（11 → 9，去掉不参与发牌的 `place` 与已退役的 `editorial`）
- 改动：字段改名（`ink` → `text` 角色、`accentInk` → `on-accent` 角色、`text` → `body`、`w` → `weights`、`job` → `note`），
  按底色亮度补 `mode`，去掉 `fields`、`seenIn`、`place`、版式的尺寸与素材字段。字体家族、色值、标签、行业与说明文字保持上游原样。

`industries`、`tones` 的别名表与 `surface: "app"` 的 4 种版式由 Oryn 编写。

许可证全文：

```
MIT License

Copyright (c) 2026 Leonxlnx

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

## Open Vetta（探针规则）

探针规则思路参考 Open Vetta（只取判据、独立实现，未复制其代码）。
