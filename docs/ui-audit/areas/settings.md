# 区域：settings（设置）

spec：`web/e2e/ui-audit/settings.e2e.ts`。代码：`web/src/pages/SettingsPage.tsx`、`web/src/pages/settings/**`、`web/src/components/settings/**`，以及设置里用到的 `components/schedule/ScheduleDialog.tsx`、`ScheduleRow.tsx`。

用例约定：
- 页面级用例先等网络空闲、主内容里的转圈消失再截图；长页面另按卡片分段截图，只滚内容滚动区（`scrollIntoView` 会连外壳一起滚，截出页头被推走的假画面）。
- 「桌面应用」分区、桌面通知卡、移动壳推送卡、App 图标、「更换服务器」只在壳里出现：用例在页面加载前注入一个假的 Tauri 宿主（桌面壳 linux / 移动壳 iOS bridge 6，原生页头与底栏命令一律拒绝，页面退回网页版页头与底栏）。桌面壳用例只跑桌面视口，移动壳用例只跑手机视口。
- 日程表达式、单位、时区、语言、认证方式等是原生 `<select>`，`ModelPatternInput` 的补全是原生 `<datalist>`，弹出层由浏览器绘制，不在审计范围内（截图里看不到，也不会被页面裁切或遮挡）。

## 清单

| id | 界面 | 打开路径（用户怎么点到） | 形态 |
|---|---|---|---|
| `settings.devices` | 设备：批准配对、设备列表、扫码连接 | 侧栏「更多」→ 设备（或 Ctrl K「设备」） | 页面 |
| `settings.devices.pair` | 批准配对：输入配对码后的设备信息卡 | 设备 → 输入配对码 → 查看 | 页面内卡片 |
| `settings.providers` | Provider 列表与新建 | 更多 → Provider | 页面 |
| `settings.providers.created` | 新建 provider 后一次性显示令牌 | Provider → 填名称 → 创建 | 页面内提示 |
| `settings.runners` | Runner 列表与新建 | 更多 → Runner | 页面 |
| `settings.runners.created` | 新建 runner 后显示令牌与启动命令 | Runner → 填名字 → 创建 | 页面内提示 |
| `settings.secrets` | 密钥：写入表单与列表 | 更多 → 密钥 | 页面 |
| `settings.browser-profiles` | 浏览器 Profile | 更多 → 浏览器 Profile | 页面 |
| `settings.git-identity` | Git 提交身份 | 更多 → Git 身份 | 页面 |
| `settings.github` | GitHub：OAuth App、账号 | 更多 → GitHub | 页面 |
| `settings.github.automation` | GitHub：自动化、Webhook、审计（下半页） | GitHub 页向下滚到「自动化」 | 页面 |
| `settings.github.connect` | 连接 Agent 账号（设备码） | GitHub → Agent 账号「连接」 | 弹窗 |
| `settings.agent-skills` | 技能 | 更多 → Agent → 技能 | 页面 |
| `settings.agent-skills.new` | 新建技能 | 技能 → 新建 | 弹窗 |
| `settings.agent-skills.edit` | 编辑技能 | 技能 → 行尾铅笔 | 弹窗 |
| `settings.agent-skills.delete` | 删除技能确认 | 技能 → 行尾垃圾桶 | 确认框 |
| `settings.agent-subagents` | 子 agent：内置与自定义 | 更多 → 子 agent | 页面 |
| `settings.agent-subagents.expanded` | 内置 agent 展开：模型覆盖、头像 | 子 agent → task「设置」 | 页面内面板 |
| `settings.agent-subagents.new` | 新建子 agent | 子 agent → 自定义 agent「新建」 | 弹窗 |
| `settings.agent-roles` | 模型角色：轮换顺序 | 更多 → 模型角色 | 页面 |
| `settings.agent-roles.others` | 模型角色：其他角色（下半页） | 模型角色页向下滚 | 页面 |
| `settings.agent-roles.expanded` | 角色展开：模型、显示名、头像 | 模型角色 → 默认「设置」 | 页面内面板 |
| `settings.agent-roles.new` | 新建自定义角色 | 模型角色 → 新建角色 | 弹窗 |
| `settings.agent-prompts` | 提示词与上下文文件 | 更多 → 提示词与上下文 | 页面 |
| `settings.agent-prompts.edit` | 编辑 AGENTS.md | 提示词 → AGENTS.md 铅笔 | 弹窗 |
| `settings.agent-prompts.delete` | 删除 AGENTS.md 确认 | 提示词 → AGENTS.md 垃圾桶 | 确认框 |
| `settings.agent-commands` | 命令、规则、提示片段（空态） | 更多 → 命令与规则 | 页面 |
| `settings.agent-commands.new` | 新建斜杠命令 | 命令与规则 → 斜杠命令「新建」 | 弹窗 |
| `settings.agent-extensions` | 扩展、Hook、自定义工具（空态） | 更多 → 扩展与工具 | 页面 |
| `settings.agent-extensions.new` | 在「扩展模块」中新建文件 | 扩展与工具 → 扩展模块「新建」 | 弹窗 |
| `settings.agent-memory` | 记忆：状态、连接、项目开关 | 更多 → 记忆 | 页面 |
| `settings.agent-memory.models` | 记忆：模型、嵌入与重排 | 记忆页向下滚到「模型」 | 页面 |
| `settings.agent-memory.browse` | 记忆：浏览 | 记忆页向下滚到「浏览」 | 页面 |
| `settings.agent-memory.candidates` | 记忆：全局记忆、待批准、mental models | 记忆页向下滚到「全局记忆」 | 页面 |
| `settings.agent-memory.usage` | 记忆：用量、导出 | 记忆页向下滚到「用量」 | 页面 |
| `settings.agent-memory.owner` | 助手记忆：设置、状态 | 记忆页向下滚到「助手记忆」 | 页面 |
| `settings.agent-memory.owner-facts` | 助手记忆：事实 | 记忆页向下滚到「事实」 | 页面 |
| `settings.agent-memory.owner-sources` | 助手记忆：来源与排除、明确记忆 | 记忆页向下滚到「来源」 | 页面 |
| `settings.agent-memory.env-file` | 重新生成 Hindsight .env | 记忆 → 连接「重新生成 .env」 | 弹窗 |
| `settings.agent-memory.owner-brief` | 助手记忆注入预览 | 记忆 → 助手记忆状态「注入预览」 | 弹窗 |
| `settings.agent-memory.owner-forget` | 按来源遗忘确认 | 记忆 → 来源「按来源遗忘」 | 确认框 |
| `settings.agent-ssh` | SSH 主机：主机、钥匙 | 更多 → SSH 主机 | 页面 |
| `settings.agent-ssh.audit` | SSH 网关审计（下半页） | SSH 主机页向下滚到「网关审计」 | 页面 |
| `settings.agent-ssh.test-result` | 主机测试结果卡 | SSH 主机 → bastion「测试」 | 页面内面板 |
| `settings.agent-ssh.host-new` | 添加主机（含开关、下拉） | SSH 主机 → 添加主机 | 弹窗 |
| `settings.agent-ssh.host-menu` | 主机行「更多」菜单 | SSH 主机 → bastion 行「…」 | 菜单 |
| `settings.agent-ssh.host-edit` | 编辑主机 | 主机行「…」→ 编辑 | 弹窗 |
| `settings.agent-ssh.host-delete` | 删除主机确认 | 主机行「…」→ 删除 | 确认框 |
| `settings.agent-ssh.host-files` | 主机文件浏览 | 主机行「…」→ 文件 | 弹窗 |
| `settings.agent-ssh.key-generate` | 生成钥匙 | 钥匙卡「生成钥匙」 | 弹窗 |
| `settings.agent-ssh.key-import` | 导入私钥 | 钥匙卡「导入私钥」 | 弹窗 |
| `settings.agent-ssh.key-menu` | 钥匙行「更多」菜单 | 钥匙行「…」 | 菜单 |
| `settings.agent-ssh.key-rename` | 重命名钥匙 | 钥匙行「…」→ 重命名 | 弹窗 |
| `settings.agent-ssh.key-delete` | 删除钥匙确认 | 钥匙行「…」→ 删除 | 确认框 |
| `settings.agent-composer` | 输入框：下一条消息预测 | 更多 → 输入框 | 页面 |
| `settings.selection-actions` | 选区快捷动作（空态） | 更多 → 选区快捷动作 | 页面 |
| `settings.selection-actions.new` | 添加选区动作 | 选区快捷动作 → 添加动作 | 弹窗 |
| `settings.agent-config` | 高级：agent 配置文件浏览器 | 更多 → 高级 | 页面 |
| `settings.agent-config.file-view` | 打开 config.yml | 高级 → 点 config.yml | 页面内面板 |
| `settings.agent-config.file-menu` | 文件行「…」菜单 | 高级 → config.yml 行「…」 | 菜单（上下文菜单） |
| `settings.agent-config.new-file` | 新建文件 | 高级 → 新建文件图标 | 弹窗 |
| `settings.identity` | 身份与头像：默认 Agent 身份 | 更多 → 身份与头像 | 页面 |
| `settings.identity.assistant` | 身份与头像：全局助手（下半页） | 身份页向下滚到「全局助手」 | 页面 |
| `settings.identity.crop` | 头像裁剪 | 身份 → 上传图片 → 选图 | 弹窗 |
| `settings.assistant` | 全局助手：联网、MCP、助手的电脑 | 更多 → 全局助手 | 页面 |
| `settings.appearance` | 外观：明暗、主题色、壁纸 | 更多 → 外观 | 页面 |
| `settings.notifications` | 通知：此设备、渠道 | 更多 → 通知 | 页面 |
| `settings.notifications.recent` | 通知：最近通知（下半页） | 通知页向下滚到「最近通知」 | 页面 |
| `settings.notifications.add-menu` | 添加渠道菜单 | 通知 → 渠道「添加渠道」 | 菜单 |
| `settings.notifications.channel-new` | 添加 Webhook 渠道 | 添加渠道 → Webhook | 弹窗 |
| `settings.notifications.channel-edit` | 编辑渠道 | 渠道行铅笔 | 弹窗 |
| `settings.shares` | 公开分享 | 更多 → 公开分享 | 页面 |
| `settings.artifacts` | 已授权的 Artifacts | 更多 → Artifacts 授权 | 页面 |
| `settings.automations` | 自动化：定时任务 | 更多 → 自动化 | 页面 |
| `settings.automations.actions` | 自动化：操作记录（下半页） | 自动化页向下滚到「操作记录」 | 页面 |
| `settings.automations.row-menu` | 定时行「…」菜单 | 自动化 → nightly 行「…」 | 菜单 |
| `settings.automations.new` | 新建定时任务 | 自动化 → 创建 | 弹窗（手机为底部抽屉） |
| `settings.automations.new.role-picker` | 定时对话框里的「对象」选择 | 新建定时任务 → 对象胶囊 | 对话框内下拉（手机为抽屉） |
| `settings.automations.new.model-picker` | 定时对话框里的「指定模型」 | 对象胶囊 → 指定模型… | 对话框内下拉（手机为抽屉） |
| `settings.automations.edit` | 编辑定时（含运行记录） | 自动化 → 点 nightly | 弹窗 |
| `settings.about` | 关于、协调宿主、退出登录 | 更多 → 关于 | 页面 |
| `settings.desktop` | 桌面应用（仅桌面壳；只跑桌面视口） | 桌面 App → 更多 → 桌面应用 | 页面 |
| `settings.desktop.notifications` | 桌面壳的通知页（桌面通知卡；只跑桌面视口） | 桌面 App → 通知 | 页面 |
| `settings.desktop.about` | 桌面壳的关于页（无退出登录；只跑桌面视口） | 桌面 App → 关于 | 页面 |
| `settings.mobile.notifications` | 移动壳的通知页（推送卡；只跑手机视口） | iOS App → 通知 | 页面 |
| `settings.mobile.appearance` | 移动壳的外观页（App 图标；只跑手机视口） | iOS App → 外观 → 滚到壁纸 | 页面 |
| `settings.mobile.about` | 移动壳的关于页（App 版本、更换服务器；只跑手机视口） | iOS App → 关于 | 页面 |
| `settings.nav` | 手机上的分区导航（侧栏抽屉；只跑手机视口） | 手机设置页左上角菜单 | 抽屉 |

## 问题记录

| # | 界面 id | 问题 | 类别 | 截图 | 处理 |
|---|---|---|---|---|---|
| 1 | settings.automations.new.role-picker | **用户报告的那个下拉**：新建定时任务里「对象」弹层是对话框正文里的 `absolute` 玻璃层。1920×1080 下从胶囊向下展开到 y=1140，超出视口；被对话框正文 `div.overflow-y-auto` 裁掉 15/25 个采样点，只露出「角色」与第一项，下面的「上一次还没结束时」输入框、页脚「启用」开关压在可见区边缘；弹层自己就是滚动容器，内容一滚 `::before` 的模糊底跟着滚走，文字直接透出；打开时自身与对话框面板都在做 opacity 动画，模糊被截断（时有时无） | 错乱 | `shots/settings.automations.new.role-picker/d1920-light-before.png` | 1bec702（主 agent：`Popover` 改用顶层 `ui/Floating`，外壳不滚、内层滚，进入只位移）。after 6 视口 0 问题，见 `shots/settings.automations.new.role-picker/d1366-light-after.png` |
| 2 | settings.automations.new.model-picker | 同一胶囊里的「指定模型」：被对话框正文裁掉 5/25 个采样点；玻璃兼滚动容器，滚到底后底色跟着滚走；打开时 opacity 动画 | 错乱 | `shots/settings.automations.new.model-picker/d1920-light-before.png` | 1bec702（同上） |
| 3 | settings.agent-ssh.host-menu、settings.agent-ssh.key-menu、settings.automations.row-menu、settings.notifications.add-menu | `Menu` 是 `absolute z-40` 的玻璃层，打开时自身做 opacity 淡入，淡入期间模糊被截断；离卡片边缘近时会被卡片 / 主滚动区裁切 | 错乱 | `shots/settings.agent-ssh.host-menu/d1920-light-before.png` | 1bec702（`Menu` 改用 `ui/Floating`） |
| 4 | settings.agent-config.file-menu | 文件行的上下文菜单 `fixed z-50` 玻璃层既是滚动容器又在打开时做 opacity 动画 | 错乱 | `shots/settings.agent-config.file-menu/d1920-light-before.png` | 1bec702（`ContextMenu` 外壳不滚、内层滚） |
| 5 | settings.agent-commands、settings.agent-extensions、settings.agent-subagents、settings.selection-actions | 空列表用 48px 内边距的大号 `EmptyState`（圆形图标 + 粗体），命令与规则页三张空卡片各占约 240px，一屏只看到两张半；同页其他卡片（provider、渠道、SSH）的空列表都是一行淡色提示 | 设计 | `shots/settings.agent-commands/d1366-light-before.png` | 91fd86d、047580c（改为一行 `text-faint text-sm` 提示） |
| 6 | settings.agent-skills、settings.agent-commands、settings.agent-extensions、settings.agent-subagents、settings.agent-roles | 卡片标题栏的「新建」按钮带 `-mr-1.5`，比其他卡片（自动化「创建」、SSH「添加主机」、选区「添加动作」）的操作按钮向右多突出 6px，几乎贴到卡片描边 | 设计 | `shots/settings.agent-skills/d1366-light-before.png` | 91fd86d |
| 7 | settings.github | OAuth App 表单里 Client ID 与 Client Secret 各占一整行且只到 `max-w-96`，右侧大块空白；两者是一对凭据，可以并排 | 设计 | `shots/settings.github/d1366-light-before.png` | 91fd86d（宽屏两列并排，手机仍各占一行） |
| 8 | settings.about（及设备配对、记忆状态、桌面应用等用 `KeyValue` 的卡片） | 键值表的标签贴在行顶：值是下拉框（界面语言）或徽章时，标签比值的文字高出约 8px，不在同一基线 | 设计 | `shots/settings.about/d1366-light-before.png` | 91fd86d（`KeyValue` 加 `items-baseline`） |
| 9 | settings.providers | Provider 行的前置位只有一个 8px 在线圆点，标题列与设备、runner、密钥页的「图标方块 + 标题」不对齐；在线状态在圆点和「在线」文字里各说一遍 | 设计 | `shots/settings.providers/d1366-light-before.png` | 91fd86d（前置位换成 box 图标方块，圆点挪到「在线」文字前） |
| 10 | settings.shares | 「Artifacts 自动发布」单独一张卡片，里面只有一行开关，紧跟着「已发布的 Artifacts」卡片：两张卡片说的是同一件事 | 冗余 | `shots/settings.shares/d1366-light-before.png` | 91fd86d（开关并入「已发布的 Artifacts」卡片首行，信息不减） |
| 11 | settings.browser-profiles | Profile 列表的说明在卡片底部，其他设置卡片的说明都在标题下方 | 设计 | `shots/settings.browser-profiles/d1366-light-before.png` | 91fd86d |
| 12 | settings.agent-ssh | 全局主机卡片里的标签筛选是一排 ghost 按钮（infra、prod、web），没有任何前缀，看起来像普通文字，不像能点的筛选 | 设计 | `shots/settings.agent-ssh/d1366-light-before.png` | 91fd86d（前置漏斗图标） |
| 13 | settings.automations.new | 新建定时任务里「项目」下拉单独占一行且只有半宽，名称独占一整行；两者都是目标信息，可以并排 | 设计 | `shots/settings.automations.new/d1366-light-before.png` | 047580c、c9e4c22（名称与项目两列并排，Space 在下一行；同名报错跟在名称列下） |
| 14 | settings.agent-ssh | mock 数据太整齐：主机名都很短、没有停用主机、标签少，看不出截断与换行 | 设计 | — | b06d329（mock 加一台长名称、长地址、长描述、4 个标签、已停用的主机）；after 截图里名称、地址整行显示，描述正常换行，未发现溢出 |
| 15 | settings.agent-memory | 记忆页一页 11 张卡片 + 助手记忆分区 6 张卡片，1366×768 要滚约 9 屏；没有页内目录 | 设计 | `shots/settings.agent-memory/d1366-light-before.png` | 需人工决策：拆页或加页内目录会改变页面主要信息结构（模块位置、导航层级），超出本次排版修复范围 |
| 16 | settings.devices | 深色主题下「用手机扫码连接」的二维码底是浅色纸面，在深色卡片里很亮 | 设计 | `shots/settings.devices/d1366-dark-before.png` | 不修：二维码必须浅底深码才能稳定被扫出，代码里已注明是有意为之 |
| 17 | settings.nav、settings.devices | 打开任一设置分区时，「更多」侧栏（手机为抽屉）不把当前分区滚进可见区：1366×768 与手机上「设置」组在折叠线下，看不出当前在哪个分区 | 设计 | `shots/settings.nav/m390-light-before.png` | 12d3c47（侧栏区域 ThreadPanelsShellAudit：路由变化时把高亮条目滚进本分区滚动区） |
| 18 | 全部设置页（d1366） | 侧栏列表最后一项「选区快捷动作」露出半行，压在悬浮底栏（线程 / 全局助手 / 项目 / 更多）下面 | 设计 | `shots/settings.devices/d1366-light-before.png` | 不修：侧栏区域确认是设计如此，悬浮玻璃底栏下透出列表，滚到底时最后一项停在底栏上方 |
