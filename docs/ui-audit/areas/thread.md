# 区域：thread（线程页核心）

spec：`web/e2e/ui-audit/thread.e2e.ts`

范围：线程页 `/t/:id` 的转录、输入坞及其弹层、页头菜单、线程内对话框、消息菜单与上下文菜单；外加首页输入卡上未被 `home.*` 覆盖的「目录被占用」对话框（首页输入卡没有斜杠菜单，附件托盘与线程页同一组件）。右侧面板归 panels 区域；CoordinatorHeader 只出现在全局助手与 Space 页，由 `assistant.*`、`space.page` 覆盖。

mock 线程：主线程 `MAIN_THREAD`（长回复、表格、时序图、公式、PR、公开链接）、工具样例 `TOOL_ZOO_THREAD`（每种工具一次调用）、四个环境故障线程 `ENV_THREADS`、runner 线程与离线 runner 线程（M11b）、定时线程（M9）。

## 清单

| id | 界面 | 打开路径（用户怎么点到） | 形态 |
|---|---|---|---|
| `thread.top` | 主线程转录顶部（首条用户消息、回合头、工具卡收起态） | 侧栏点线程，往上翻到顶 | 页面 |
| `thread.mid` | 主线程转录中段（表格、时序图、公式、用量胶囊） | 同上，翻到中间 | 页面 |
| `thread.table-narrow` | 900 宽窄桌面窗口下的 Markdown 表格（中栏放不下，横向滚动；仅桌面，手机表格列按词换行能放下） | 把窗口拉窄到 900，翻到回复里的表格 | 页面 |
| `thread.bottom` | 主线程转录底部 + 输入坞 | 打开线程（默认贴底） | 页面 |
| `thread.tools` | 工具样例线程顶部（各工具卡收起） | 侧栏「工具样例」 | 页面 |
| `thread.tools.mid` | 工具样例中段 | 同上，翻到中间 | 页面 |
| `thread.tools.expanded` | 工具卡全部展开（diff、patch、代码） | 点开每张工具卡 | 页面 |
| `thread.tools.expanded-end` | 工具卡全部展开，翻到底（artifact、bash 输出） | 同上，翻到底 | 页面 |
| `thread.env.setupFailed` | 环境 setup 失败横幅 | 打开 setup 失败的线程 | 页面（托盘内横幅） |
| `thread.env.slowSetup` | setup 进行中横幅 | 打开 setup 慢的线程 | 页面 |
| `thread.env.missingConfig` | 缺少环境配置横幅 | 打开缺配置的线程 | 页面 |
| `thread.env.orbFailed` | orb 故障横幅 | 打开 orb 故障的线程 | 页面 |
| `thread.runner` | runner 线程（页头 runner · cwd 胶囊） | 打开 runner 上的线程 | 页面 |
| `thread.runner-offline` | runner 离线提示 | 打开离线 runner 上的线程 | 页面 |
| `thread.scheduled` | 定时线程（页头定时胶囊） | 打开带定时的线程 | 页面 |
| `thread.ask` | agent 提问卡（UiRequestCard，托盘内） | agent 发起 ask | 面板（托盘内） |
| `thread.plan` | 计划提案（PlanProposalDialog） | agent 提交计划 | 弹窗 / 手机底部抽屉 |
| `thread.jump-end` | 「回到底部」小圆钮（仅桌面；手机往上翻时输入坞收成胶囊，见下一项） | 往上翻转录 | 浮钮 |
| `thread.dock-pill` | 输入坞收起成信息胶囊（仅手机） | 手机往上翻转录 | 浮动胶囊 |
| `thread.quote` | 选区引用胶囊（桌面贴选区）/ 引用条（触屏贴托盘） | 选中助手正文 | 浮动胶囊 |
| `thread.side-ask` | 解释浮卡（SideAskCard；手机是对话框） | 选区胶囊 →「解释」 | 浮卡 / 弹窗 |
| `thread.message-menu` | 消息操作菜单（复制 / 引用 / 分叉 / 加标签） | 悬停用户消息 →「⋯」 | 菜单 |
| `thread.context-menu` | 转录右键菜单（手机长按动作面板） | 右键 / 长按助手正文 | 菜单 / 动作面板 |
| `thread.tool-context-menu` | 工具卡右键菜单（仅桌面；手机同一套动作在长按面板里，见上一项） | 右键工具卡 | 菜单 |
| `thread.slash` | 斜杠命令菜单（CommandMenu） | 输入框键入 `/` | 下拉（坞上方） |
| `thread.slash.usage` | 命令用法卡（`/goal ` 之后） | 键入 `/goal ` | 下拉（坞上方） |
| `thread.ref-menu` | GitHub issue / PR 引用菜单（RefMenu） | 键入 `#log` | 下拉（坞上方） |
| `thread.thinking` | 思考级别菜单（ThinkingDial） | 输入坞模型胶囊右侧圆圈 | 菜单 |
| `thread.speed` | 速度档位菜单 | 输入坞页脚速度读数 | 菜单 |
| `thread.modes-more` | 「更多模式」菜单（仅手机：窄托盘放不下六个模式开关） | 输入坞「⋯」 | 菜单 |
| `thread.role-track` | 角色模型轨道（RoleTrack，仅桌面：Ctrl+P 快捷键） | Ctrl+P 切换角色 | 浮动胶囊 |
| `thread.attachments-tray` | 附件托盘（长文件名、多文件） | 输入坞回形针 / 拖入文件 | 面板（托盘内） |
| `thread.actions` | 线程操作菜单（宽页头「更多操作」/ 手机标题菜单） | 页头「⋯」/ 手机点标题胶囊 | 菜单 |
| `thread.actions.snooze` | 子菜单「稍后提醒」 | 操作菜单 →「稍后提醒」 | 菜单（换页） |
| `thread.actions.color` | 子菜单「颜色」 | 操作菜单 →「颜色」 | 菜单（换页） |
| `thread.pr-menu` | PR 胶囊菜单（仅桌面；手机 PR 在标题菜单里） | 页头「#128」 | 菜单 |
| `thread.view-menu` | 视图菜单（仅手机：宽屏是标签条） | 页头右上角视图钮 | 菜单 |
| `thread.dlg.tree` | 会话树 | 页头树图标 / `/tree` | 弹窗 |
| `thread.dlg.fork` | 分叉线程 | 操作菜单「分叉」 | 弹窗 |
| `thread.dlg.model` | 模型（角色 + 全部模型） | 输入坞模型胶囊 / `/model` | 弹窗 |
| `thread.dlg.goal` | 目标 | 模式开关「目标」/ `/goal` | 弹窗 |
| `thread.dlg.btw` | 旁路提问（空） | `/btw` | 弹窗 |
| `thread.dlg.btw-topic` | 旁路提问（带话题、问答详情） | `/btw <问题>` | 弹窗 |
| `thread.dlg.rename` | 改名 | 操作菜单「改名」 | 弹窗 |
| `thread.dlg.labels` | 线程标签 | 操作菜单「标签」 | 弹窗 |
| `thread.dlg.label` | 消息标签 | 消息菜单「加标签」 | 弹窗 |
| `thread.dlg.debug` | 调试面板 | 操作菜单「调试面板」 | 弹窗 |
| `thread.dlg.attachments` | 附件 | 操作菜单「附件」 | 弹窗 |
| `thread.dlg.share` | 分享线程 | 操作菜单「分享…」 | 弹窗 |
| `thread.dlg.share-findings` | 分享：扫描发现疑似密钥 | 分享 →「创建链接」 | 弹窗 |
| `thread.dlg.schedule` | 定时（从线程打开） | 操作菜单「定时…」 | 弹窗 |
| `thread.dlg.archive` | 归档（runner worktree 线程） | 操作菜单「归档」 | 弹窗 |
| `thread.dlg.context` | 上下文报告（ContextBreakdown） | 输入坞页脚上下文圆环 | 弹窗 |
| `thread.dlg.subagent` | 子 agent 转录 | 工具卡里的子 agent 链接 | 弹窗 |
| `thread.dlg.setup-log` | setup 日志 | setup 失败横幅「查看日志」 | 弹窗 |
| `thread.home.dir-in-use` | 目录被占用（DirInUseDialog） | 首页选 runner 已占用目录后发送 | 弹窗 |

## 问题记录

| # | 界面 id | 问题 | 类别 | 截图 | 处理 |
|---|---|---|---|---|---|
| 1 | `thread.role-track` | Ctrl+P 浮出的角色轨道是玻璃胶囊，收起时整块 `opacity` 过渡到 0；过渡期间它自己成了 Backdrop Root，模糊截断，读起来是「先变透明再消失」，而且审计记到打开瞬间就有 opacity 过渡 | 错乱 | `shots/thread.role-track/d1366-light-before.json` | 6163e4a（收起改为缩小下沉，不动透明度） |
| 2 | `thread.actions`（m390） | 手机标题菜单 `.title-menu` 本身带 backdrop-filter，开合做 `opacity` 过渡，打开的 150ms 里模糊被截断 | 错乱 | `shots/thread.actions/m390-light-before.png` | 6163e4a（开合只动位移与缩放，收起时 invisible） |
| 3 | `thread.quote`（m390） | 触屏引用条「引用回复」「解释」各被折成两行（胶囊 `absolute left-1/2` 按收缩适配只拿到容器一半宽度） | 错乱 | `shots/thread.quote/m390-light-before.png` | 1a6019f（胶囊 `w-max` + `nowrap`） |
| 4 | `thread.message-menu` | 「⋯」外包元素做 `opacity` 显隐过渡，菜单是它的 DOM 后代：悬停后马上点开时菜单玻璃的模糊被截断 | 错乱 | `shots/thread.message-menu/d1920-light-before.json` | 2156227（透明度只加在按钮上） |
| 5 | `thread.message-menu`、`thread.dlg.label` | 菜单打开后再对菜单 `scrollIntoView`：菜单已进顶层，滚动的是转录，锚点连同菜单一起被带出视口（1920 上菜单跑到 y=-148，「加标签」点不到） | 错乱 | `shots/thread.message-menu/d1920-light-before.json` | 2156227（去掉 scrollIntoView，展开方向仍按页头 / 输入坞之间的可见区选） |
| 6 | `thread.dlg.goal` | 没有目标时上来先是「当前目标 / 当前没有目标。」一整节，紧接着又是「设定目标」表单，空态说了两遍 | 冗余 | `shots/thread.dlg.goal/d1366-light-before.png` | 2391c21（没有目标时只留设定表单） |
| 7 | `thread.dlg.labels` | 只有一个输入框，「保存」却另起一行右对齐；改名、消息标签两个同类对话框都是输入框与保存同一行 | 设计 | `shots/thread.dlg.labels/d1366-light-before.png` | 2391c21（与输入框同一行，底边对齐） |
| 8 | `thread.plan`（m390） | 计划提案五个按钮在 390 宽里折成两行，第二行只剩一个「提修改意见」孤零零靠右 | 设计 | `shots/thread.plan/m390-light-before.png` | 2391c21（手机上「执行」独占一行，其余两两成列） |
| 9 | `thread.dlg.model` | 「全部模型」标题与搜索框随列表滚走 | 设计 | `shots/thread.dlg.model/d1366-light-before-scrolled.png` | 63c74d5（主 agent 在 ModelPickerBody 吸顶，本区复核通过） |
| 10 | `thread.dlg.debug` | 调试面板是宽对话框，没有未识别帧时只有一行「暂无。」，大片留白 | 设计 | `shots/thread.dlg.debug/d1366-light-after.png` | 不修：宽度是给 JSON 帧准备的，帧随时可能在对话框开着时到来，按内容切换宽度会让对话框跳动 |
| 11 | `thread.message-menu` 等全部 `Menu` 触发钮 | 共享 `Menu` 的触发钮只给了 `aria-expanded`，没有 `aria-haspopup="menu"`，读屏不知道它会弹菜单（审计脚本也因此无法把它和折叠钮区分开） | 设计 | — | 需人工决策：属共享核心文件 ui/primitives.tsx，已报主 agent |
| 12 | `thread.ref-menu` | Esc 收起 `#` 引用菜单后，删掉再敲出同样的 `#log`，菜单不再弹出（`useRefMenu` 只记「收起时的整段文本」，文本回到同一值就仍算已收起） | 设计 | — | 需人工决策：属交互逻辑，不在排版范围；审计用例改为清空输入框来关菜单 |
| 13 | `thread.table-narrow`（另见 host 区域 `host.chrome-mac.narrow`） | 900 宽时转录里的 Markdown 表格右侧被硬切（「退避 + 补拉」「live.rese…」截在边缘）；`.md-table` 能横向滚动但边缘没有任何提示，看着像排版坏了 | 设计 | `shots/host.chrome-mac.narrow/d1366-light-after.png` | b53486a（`.md-table` 纯 CSS 滚动阴影：两层 local 纸面色遮罩 + 两层 scroll 径向阴影，只在还能滚的一侧出现；深色主题加深阴影色。表头行底色不透明，阴影只露在表体） |
| 14 | `thread.dlg.attachments`（m390） | 手机上附件行很散：文件名、「2 KB」徽标、日期各占一行，日期本身折行；「撤回 / 下载 / 删除」挤在右侧，下面又单独一行「已公开」与复制图标 | 设计 | `shots/thread.dlg.attachments/m390-light-before.png` | d92f941（第一行文件名截断；第二行「2 KB · 11月21日 00:56 · 已公开」合成一行，日期用 Intl 短格式、完整时间在悬停提示；操作在窄屏另起一行与文字左缘对齐，桌面仍在右侧） |
| 15 | `thread.dlg.attachments` | mock 只有一个附件、名字很短，长文件名截断和多行排版暴露不出来 | 设计 | — | d92f941（mock 补三个附件：超长英文文件名的图片、JSON、超长中文名的 12 MB PDF，均未发布） |
| 16 | `thread.tools.mid`（及 assistant 区 `assistant.thread`，m390 最明显） | #4 把显隐透明度挪到按钮后，回合中间消息右上角的浮动槽仍常显底色与描边，按钮透明时留下一个空描边小方块压在正文上（SectionsAudit 发现） | 错乱 | `shots/assistant.thread/m390-light-after.png` | 9681c55（底色描边画在按钮上，随按钮显隐；槽位只定位） |
