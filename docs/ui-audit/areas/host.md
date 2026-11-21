# 区域：host（宿主壳：桌面窗口栏、连接页、手机壳）

spec：`web/e2e/ui-audit/host.e2e.ts`。假宿主（`window.__TAURI__` 与 `__ORYN_HOST__`）来自 `web/scripts/fake-host.ts`，和截图脚本 `screenshots.ts` 共用。窗口栏和连接页里都没有下拉或弹层，按页面级审计：检查横向溢出并截图。手机壳的贴边对话框按浮层审计。

## 清单

| id | 界面 | 打开路径（用户怎么点到） | 形态 |
|---|---|---|---|
| host.chrome-mac.thread | macOS 自绘窗口栏：线程页，侧栏展开、面板关 | 桌面 App；打开任一线程 | 页面（仅桌面） |
| host.chrome-mac.thread-panel | macOS 自绘窗口栏：线程页，侧栏展开、面板开 | 桌面 App；线程页头「信息面板」 | 页面（仅桌面） |
| host.chrome-mac.thread-collapsed | macOS 自绘窗口栏：线程页，侧栏收起、面板关 | 桌面 App；侧栏顶部「收起侧边栏」 | 页面（仅桌面） |
| host.chrome-mac.thread-collapsed-panel | macOS 自绘窗口栏：线程页，侧栏收起、面板开 | 桌面 App；收起侧栏 + 打开信息面板 | 页面（仅桌面） |
| host.chrome-mac.home | macOS 自绘窗口栏：首页 | 桌面 App；启动后默认页 | 页面（仅桌面） |
| host.chrome-mac.settings | macOS 自绘窗口栏：设置 · 外观 | 桌面 App；侧栏「更多」→ 设置 | 页面（仅桌面） |
| host.chrome-mac.launcher | macOS 自绘窗口栏：窗口栏里的连接页 | 桌面 App；首次启动 / 断开服务器后 | 页面（仅桌面） |
| host.chrome-mac.narrow | macOS 自绘窗口栏：900 宽三栏（侧栏 256px）+ 面板 | 桌面 App；把窗口拖窄到 840–1023 | 页面（仅桌面） |
| host.chrome-win.thread | Windows 自绘窗口栏：线程页，侧栏展开、面板关 | 桌面 App；打开任一线程 | 页面（仅桌面） |
| host.chrome-win.thread-panel | Windows 自绘窗口栏：线程页，侧栏展开、面板开 | 桌面 App；线程页头「信息面板」 | 页面（仅桌面） |
| host.chrome-win.thread-collapsed | Windows 自绘窗口栏：线程页，侧栏收起、面板关 | 桌面 App；侧栏顶部「收起侧边栏」 | 页面（仅桌面） |
| host.chrome-win.thread-collapsed-panel | Windows 自绘窗口栏：线程页，侧栏收起、面板开 | 桌面 App；收起侧栏 + 打开信息面板 | 页面（仅桌面） |
| host.chrome-win.home | Windows 自绘窗口栏：首页 | 桌面 App；启动后默认页 | 页面（仅桌面） |
| host.chrome-win.settings | Windows 自绘窗口栏：设置 · 外观 | 桌面 App；侧栏「更多」→ 设置 | 页面（仅桌面） |
| host.chrome-win.launcher | Windows 自绘窗口栏：窗口栏里的连接页 | 桌面 App；首次启动 / 断开服务器后 | 页面（仅桌面） |
| host.chrome-win.narrow | Windows 自绘窗口栏：900 宽三栏（侧栏 256px）+ 面板 | 桌面 App；把窗口拖窄到 840–1023 | 页面（仅桌面） |
| host.chrome-linux.thread | Linux（按钮在左） 自绘窗口栏：线程页，侧栏展开、面板关 | 桌面 App；打开任一线程 | 页面（仅桌面） |
| host.chrome-linux.thread-panel | Linux（按钮在左） 自绘窗口栏：线程页，侧栏展开、面板开 | 桌面 App；线程页头「信息面板」 | 页面（仅桌面） |
| host.chrome-linux.thread-collapsed | Linux（按钮在左） 自绘窗口栏：线程页，侧栏收起、面板关 | 桌面 App；侧栏顶部「收起侧边栏」 | 页面（仅桌面） |
| host.chrome-linux.thread-collapsed-panel | Linux（按钮在左） 自绘窗口栏：线程页，侧栏收起、面板开 | 桌面 App；收起侧栏 + 打开信息面板 | 页面（仅桌面） |
| host.chrome-linux.home | Linux（按钮在左） 自绘窗口栏：首页 | 桌面 App；启动后默认页 | 页面（仅桌面） |
| host.chrome-linux.settings | Linux（按钮在左） 自绘窗口栏：设置 · 外观 | 桌面 App；侧栏「更多」→ 设置 | 页面（仅桌面） |
| host.chrome-linux.launcher | Linux（按钮在左） 自绘窗口栏：窗口栏里的连接页 | 桌面 App；首次启动 / 断开服务器后 | 页面（仅桌面） |
| host.chrome-linux.narrow | Linux（按钮在左） 自绘窗口栏：900 宽三栏（侧栏 256px）+ 面板 | 桌面 App；把窗口拖窄到 840–1023 | 页面（仅桌面） |
| host.chrome-mac.fullscreen | macOS 全屏：系统收起红绿灯，窗口栏留白收回 | 桌面 App 进入全屏 | 页面（仅桌面） |
| host.chrome-win.maximized-blur | Windows 最大化且窗口失焦：还原图标、按钮变淡 | 最大化后切到别的窗口 | 页面（仅桌面） |
| host.launcher.connect | 桌面连接页 · 连接：服务器地址输入 | 桌面 App 首次启动 / 断开后 | 页面（仅桌面） |
| host.launcher-mobile.connect | 手机连接页 · 连接：服务器地址输入 | 手机 App 首次启动 / 断开后 | 页面（仅手机） |
| host.launcher.confirm-connect | 桌面连接页 · 连接前确认：大字显示目标主机 | 桌面 App 首次启动 / 断开后 | 页面（仅桌面） |
| host.launcher-mobile.confirm-connect | 手机连接页 · 连接前确认：大字显示目标主机 | 手机 App 首次启动 / 断开后 | 页面（仅手机） |
| host.launcher.pairing | 桌面连接页 · 配对：配对码与倒计时 | 桌面 App 首次启动 / 断开后 | 页面（仅桌面） |
| host.launcher-mobile.pairing | 手机连接页 · 配对：配对码与倒计时 | 手机 App 首次启动 / 断开后 | 页面（仅手机） |
| host.launcher.error | 桌面连接页 · 错误：无法连接 + 自动重连 | 桌面 App 首次启动 / 断开后 | 页面（仅桌面） |
| host.launcher-mobile.error | 手机连接页 · 错误：无法连接 + 自动重连 | 手机 App 首次启动 / 断开后 | 页面（仅手机） |
| host.launcher.confirm-change | 桌面连接页 · 更换服务器确认（会移除本机凭据） | 桌面 App 首次启动 / 断开后 | 页面（仅桌面） |
| host.launcher-mobile.confirm-change | 手机连接页 · 更换服务器确认（会移除本机凭据） | 手机 App 首次启动 / 断开后 | 页面（仅手机） |
| host.mobile.sink-closed | 手机壳（屏幕圆角 47）线程页，侧栏收起 | 手机 App 打开线程 | 页面（仅手机） |
| host.mobile.sink-open | 手机壳下沉侧栏打开：卡片与屏幕圆角同心 | 线程页头「侧边栏」 | 页面（仅手机） |
| host.mobile.corners-dialog | 手机壳贴边对话框（附件） | 标题胶囊菜单 → 附件 | 弹窗（仅手机） |
| host.mobile.push-card | 设置 · 通知里的手机推送卡 | 侧栏「更多」→ 设置 → 通知 | 页面（仅手机） |

## 问题记录

| # | 界面 id | 问题 | 类别 | 截图 | 处理 |
|---|---|---|---|---|---|
| 1 | host.chrome-mac.narrow | 900 宽三栏（侧栏 256px）、右面板开：信息面板「服务」行的红色故障标签左侧被裁掉，只剩「败 · 重启 5 次」 | 错乱 | `shots/host.chrome-mac.narrow/d1366-light-after.png` | 1cfb2dc（ThreadPanelsShellAudit：标签可收缩、从右截断）；panels 新增 `panels.info.narrow` 回归，见 panels #11 |
| 2 | host.chrome-mac.narrow | 900 宽时转录里的 Markdown 表格在中栏右缘被硬切，可以横向滚动，但没有任何提示，读起来像排版坏了 | 设计 | `shots/host.chrome-mac.narrow/d1366-light-after.png` | b53486a（ThreadCoreAudit：`.md-table` 左右滚动阴影），见 thread #13 |
| 3 | host.mobile.corners-dialog、host.launcher-*.pairing 等 | 本区登记前，宿主壳界面完全不在审计范围（shell.e2e.ts 写的是「纯浏览器跑不到，不登记」） | 设计（覆盖） | — | c72eaff（用 scripts/fake-host.ts 登记 40 个界面） |
| 4 | host.mobile.corners-dialog | 手机附件对话框：文件名、大小、日期被拆成三行，日期本身也折行；操作按钮挤在右侧，「已公开」又另起一行，读起来很散 | 设计 | `shots/host.mobile.corners-dialog/m390-light-after.png` | d92f941（ThreadCoreAudit：元信息合成一行，窄屏下操作按钮另起一行），见 thread #14 |
