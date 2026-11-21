# 区域：home（首页 / 新建线程）

spec：`web/e2e/ui-audit/home.e2e.ts`

## 清单

| id | 界面 | 打开路径（用户怎么点到） | 形态 |
|---|---|---|---|
| home | 首页：品牌标、标题、输入托盘与四个胶囊 | 启动后默认页 `/`；侧栏「新建线程」 | 页面 |
| home.role-picker | 对话对象选择（全局助手 / 各 role / 默认 / 指定模型…） | 首页托盘左下「对象」胶囊 | 下拉（桌面）/ 底部抽屉（手机） |
| home.model-picker | 指定模型：模型目录 + 筛选框 | 对象胶囊 → 「指定模型…」 | 下拉（桌面）/ 底部抽屉（手机） |
| home.runner-picker | 执行位置（新 orb 规格、Runners） | 托盘「local · medium」胶囊 | 下拉 |
| home.profile-picker | 浏览器 Profile | 托盘「浏览器 Profile」胶囊 | 下拉 |
| home.project-picker | 项目 | 托盘「无项目」胶囊 | 下拉 |

## 问题记录

| # | 界面 id | 问题 | 类别 | 截图 | 处理 |
|---|---|---|---|---|---|
| 1 | home.model-picker | 列表滚动后玻璃底（`.glass::before`）跟着内容一起滚走，标题「要做点什么?」和输入框占位文字直接透过列表露出来。PickerPopover 的玻璃外壳本身就是 `overflow-y-auto` 的滚动容器 | 错乱 | `shots/home.model-picker/d1366-light-before-scrolled.png` | 1bec702（ui/Floating：外壳不滚动，内容在内层滚动） |
| 2 | home.role-picker、home.runner-picker、home.profile-picker、home.project-picker | 同 #1 的结构问题：所有 PickerPopover 的外壳都是滚动容器，列表一长就会透字 | 错乱 | `shots/home.runner-picker/d1366-light-before.png` | 1bec702 |
| 3 | 全部下拉 | 打开时 `.pop-in` 会淡入，外壳 opacity<1 期间它本身成了 Backdrop Root，模糊失效。表现为「透明度时有时无」：先是透明的，180ms 后才突然变成磨砂 | 错乱 | `shots/home.role-picker/d1366-light-before.json` | 1bec702（`.glass.pop-in` 只做位移，不淡入） |
| 4 | home | 首页托盘的进入动画淡入 420ms（含 120ms 延迟），托盘的玻璃（glass-dock）在这段时间里没有模糊，第一次打开系统时尤其明显 | 错乱 | `shots/home/d1366-light-before.json` | 1bec702（托盘的 rise 只做位移） |
| 5 | home.runner-picker（m390） | 手机上执行位置下拉被托盘所在的 `overflow` 容器裁掉一角（25 个采样点里有 5 个落在外面） | 错乱 | `shots/home.runner-picker/m390-light-before.png` | 1bec702（顶层 + 按视口夹位） |
| 6 | 全部下拉 | 弹层是 `absolute z-30/40`，放在对话框或带层叠上下文的容器里时，会被裁切或被后面的控件压住 | 错乱 | 见 settings 区域的对话框截图 | 1bec702（Popover API 顶层） |
| 7 | home.model-picker | 「全部模型」标题和筛选框会随列表滚走，模型多时想改筛选词得先滚回顶部 | 设计 | `shots/home.model-picker/d1366-light-before-scrolled.png` | 63c74d5（标题和筛选框吸顶，不透明底） |
| 8 | home.model-picker | mock 目录只有 10 个对话模型、名称都很短，滚动和长名称截断的问题暴露不出来 | 设计（测试数据） | — | 63c74d5（mock 目录补到 24 个，含超长名称） |
| 9 | home.runner-picker vs home.role-picker（m390） | 手机上「对象」是底部抽屉，「执行位置 / Profile / 项目」却是锚定下拉，形态不统一 | 设计 | `shots/home.runner-picker/m390-light-after.png` | 不修：三个下拉都很短（≤8 行），锚定在胶囊上方也不会挡住输入；对象列表长且带二级页，用抽屉合理。改成抽屉会改变交互形态，不属于排版范围 |
