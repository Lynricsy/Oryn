# 区域：fallback（无 Popover API 的退化路径）

spec：`web/e2e/ui-audit/fallback.e2e.ts`。`tauri.conf.json` 支持 iOS 15 / macOS 12，`showPopover` 到 Safari 17 才有；用例在页面加载前删掉 `HTMLElement.prototype.showPopover`，让 `ui/Floating` 走 `fixed + z-60` 的退化路径。

## 清单

| id | 界面 | 打开路径（用户怎么点到） | 形态 |
|---|---|---|---|
| fallback.schedule-role-picker | 定时对话框里的「对象」选择 | 设置 → 自动化 → 创建 → 对话框里的「对象」胶囊 | 对话框内下拉（手机为底部抽屉） |
| fallback.home-model-picker | 首页指定模型（隔离的托盘层叠上下文里） | 首页「对象」胶囊 → 指定模型…（仅桌面：手机走 Dialog，不经 Floating） | 下拉 |
| fallback.thread-actions | 线程「更多操作」菜单（桌面）/ 标题胶囊菜单（手机） | 线程页页头「更多操作」；手机点标题胶囊 | 菜单 |
| fallback.sink-runner-picker | 下沉卡片平移时的执行位置下拉 | 手机首页「local」胶囊；用例把 `.sink-card` 固定推开 30% 抽屉宽（仅手机） | 下拉 |

## 问题记录

| # | 界面 id | 问题 | 类别 | 截图 | 处理 |
|---|---|---|---|---|---|
| 1 | fallback.sink-runner-picker | 退化路径下 fixed 弹层的包含块是带独立 `translate` 的 `.sink-card`，`fixedContainingBlock` 只看 `transform`，没把 `translate` 算进去，弹层整体偏移约 100px、跑到屏幕外（left -53px、top -192px）。另外只靠 scroll/resize/ResizeObserver 重排，卡片平移时弹层不跟 | 错乱 | `shots/fallback.sink-runner-picker/m390-light-before.png` | 186fa23、ab23f49、bf9cd2d（退化路径按实际包含块换算坐标；包含块检测补上 `translate`/`rotate`/`scale`；打开期间逐帧比对锚点与包含块的位置，变化即重排） |
| 2 | fallback.schedule-role-picker | 对话框面板进入时淡入 180ms，如果在这段时间内打开对话框里的下拉，退化路径下弹层是面板的后代，模糊会被面板的 opacity 截断 | 错乱 | — | 不修：只有对话框出现后 180ms 内就点胶囊才会触发，人手点不到这么快；有 Popover API 时弹层在顶层，不受影响。用例改为等对话框落定再点 |
| 3 | fallback.thread-actions（m390） | 手机标题菜单 `div.title-menu` 打开时自身在做 opacity 动画，里面的玻璃模糊被截断 | 错乱 | `shots/fallback.thread-actions/m390-light-before.json` | 6163e4a（ThreadCoreAudit：标题菜单开合不再动透明度）；本用例恢复手机视口，before 两个时机报此问题，after 0 问题 |
