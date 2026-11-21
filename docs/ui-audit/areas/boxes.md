# UI 审计：Boxes（boxes）

范围：Boxes 分区（M23）——总览（介绍页与 box 列表）、新建 Box 对话框、单台 box 页（app 行菜单、运行时段、密钥、悬停预览）、首屏执行位置弹层的 Boxes 分组。spec 在 `web/e2e/ui-audit/boxes.e2e.ts`。

说明：
- M23 新增，无更早基线，before 取自首版（`UI_AUDIT_PHASE=before UI_AUDIT_WEB=$PWD` 对着当前工作区跑这一个 spec）。
- mock 种子：缺省两台 box（`workbench` 运行中、三个 app；`lab` 主人停机）；`boxes.empty` 用页面脚本写 `localStorage["oryn.mock.boxes"] = "empty"` 得到空态。
- `boxes.detail.preview` 的预览帧是跨源 iframe，mock 的 fetch 替身管不到它的导航：spec 在打开步骤里用 `page.route` 给 `https://bug-tracker.apps.oryn.test/**` 回一页固定 HTML，并断言帧里的文字可见。触屏没有悬停、预览不启用，所以仅桌面。
- 「仅桌面 / 仅手机」的界面在 spec 里用 `only` 显式跳过另一类视口。

## 清单

| id | 界面 | 打开路径（用户怎么点到） | 形态 |
|---|---|---|---|
| `boxes.empty` | Boxes 介绍页（还没有 box） | 更多 → Boxes | 页面 |
| `boxes.list` | Boxes 总览（box 列表与最近活动） | 更多 → Boxes | 页面 |
| `boxes.new-dialog` | 新建 Box 对话框 | 总览页头「新建 Box」 | 弹窗（手机为抽屉） |
| `boxes.detail` | 单台 box 页（应用、活动、21 天用量、关于） | 总览点某台 box | 页面 |
| `boxes.detail.app-menu` | app 行菜单（公开性、重启、回滚、密钥、日志、下线） | app 行「…」 | 菜单 |
| `boxes.detail.schedule` | 运行时段对话框 | 页头「…」→ 运行时段… | 弹窗（手机为抽屉） |
| `boxes.detail.secrets` | app 的密钥对话框 | app 行「…」→ 密钥… | 弹窗（手机为抽屉） |
| `boxes.detail.preview` | app 悬停预览 | 指针停在 app 徽标上 | 提示（仅桌面：触屏没有悬停） |
| `boxes.home-executor` | 首屏执行位置弹层的 Boxes 分组 | 首屏执行位置胶囊 → 滚到底 | 下拉 |

## 问题记录

| # | 界面 id | 问题（具体到视口/主题/元素） | 类别 | 截图 | 处理 |
|---|---|---|---|---|---|
| 1 | `boxes.detail` | m390 与 360 宽：app 行第一行里名字与地址同为 `truncate`、按各自宽度等比收缩，名字先被截成「bug-trac…」「team-…」「tiny-cr…」，而地址还留着大半截；名字才是这一行的主体 | 设计 | `shots/boxes.detail/m390-light-after.png`（修前；before 取自修后的首版，看不到这个问题） | AppRow：名字 `shrink-0 max-w-full`（只在比整行还长时截断），窄屏只压地址 |
