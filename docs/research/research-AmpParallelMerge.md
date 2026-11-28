# Amp 如何合并多个并行线程的改动（2026-11-28 实测 + 文档）

调研对象：Amp CLI `0.0.1795867242-g5fd9f7`（2026-11-28 发布）、ampcode.com 文档与 news、社区讨论，以及在一次性托管项目 `lingx/oryn-ship-lab` 上的三组实验。Amp 的提示词与工具描述原文只存在 `.dev/research/amp-merge/`（已 gitignore），本文只做转述。

## 结论

Amp **没有服务端的合并机制**：没有 merge queue、没有锁、没有集成服务。并行线程的合并全部由 agent 在各自 orb 里用 git 完成，服务端只负责三件事：

1. 把「怎么交付」写成可编辑的保存提示词（文档称 Ship 按钮只是让 agent 收到一条消息）；
2. 跨线程消息自动附带来源元数据，并约束「不得未经主人同意并入别人的改动」；
3. 追踪交付结果（提交是否进了默认分支、PR 状态），在 Space 里汇总。

冲突处理的产品理念是 trunk-based、小步提交、减少冲突概率，而不是排队（官方 notes「Pave the Road」、播客 S2E1/S2E6）。

## 1. Ship：交付提示词

内置保存提示词共四个：`ship`、`update-base`、`refactor-commits`、`review`，可在 Prompts 页修改；`load_prompt` 可读取原文，`send_thread_message` 的 `prompt` 参数可按名发送，文本前置在消息正文之前。下面的规则取自 `load_prompt` 返回的原文。

**Changes 面板的按钮本次没有实测**（无头浏览器没有 Amp 登录态）。文档说按钮发出的提示词「as if you had typed it」，但按钮路径并不等于手动输入：插件钩子 `changes.prompt` 可以往 Ship / Push to Branch 提示词后追加内容（对话框里可见可改），`agent.start` 事件带 `changesWorkflow: 'ship' | 'push_branch'` 标记，所以插件和 Amp 能区分这是一次按钮触发的交付。

`ship` 的规则（转述）：

- 目标是 `origin/HEAD`，已提交与未提交的改动都交付，未提交的先提交。
- 推送前先 fetch 并 rebase 到最新 `origin/HEAD`，**然后跑完整测试套件**再推送。
- 推送因基线前进被拒：再 rebase、解决冲突后重推；测试**只在**冲突解决或其他本地编辑改了文件后才重跑，单纯重新 fetch / rebase / 被拒不重跑。
- **遇到实质性冲突先停下来问主人**，不自行裁决。
- rebase 冒出大量不相关文件的 add/add 冲突（浅克隆导致）：abort、`fetch --unshallow`、再 rebase。
- 测试失败只有在确认 `origin/HEAD` 去掉本线程改动后同样失败时才能忽略。

其余三个：`update-base` = fetch + rebase 到 `origin/HEAD`，保留未提交改动（同样带 unshallow 兜底）；`refactor-commits` = 把 merge base 以来的改动重组成易评审的提交，最终树不变、不推送（Restack 按钮背后就是它）；`review` = 让一个 Task 子 agent 评审当前改动。

Ship Behavior 三选一：Ship（上面这段）、Push to Branch（推到当前分支；在基线上时 agent 自起分支名；不自动开 PR）、Custom Ship（≤10,000 字符纯文本）。

## 2. 托管仓库的强推：主人凭据实测允许，线程凭据未验证

用主人本机凭据对 `ampcode.com/@lingx/oryn-ship-lab`：普通非快进推送被 git 客户端拒绝；`git push --force origin main` **成功**；`git push origin --delete main` **成功**。这只说明**主人凭据**不受限制；它区分不了「只限制线程、不限制主人」这类规则是否存在，orb 里线程凭据能否强推或删除基线**没有验证**。

文档里托管仓库的设置只有 Base Branch、只读凭据、归档只读、post-receive webhook，没找到保护规则或被覆盖 ref 留存的说明（没找到不等于没有）。GitHub 仓库沿用 GitHub 自己的保护规则。

Oryn 托管仓库目前对线程推送同样没有限制，另有 `refs/oryn/saved/` 30 天留存。

## 3. 并发 Ship 实测：两个线程改同一函数

两个 orb 线程从 `47533fb` 出发，各自本地提交互相矛盾的 `add()` 改动（C：接受 float；D：只接受 int、拒绝 bool/str），然后用 CLI `amp threads continue -ox -x` 以主人身份几乎同时发出一条消息：`load_prompt` 取到的 `ship` 原文，后面追加一行推送授权（因为两个线程开工时被要求不要推送）。这模拟的是 Ship 提示词的内容，不是 Changes 按钮本身：

| 线程 | 过程 | 结果 |
|---|---|---|
| C | fetch 时基线已多 2 个 greet 提交（另一组实验）→ rebase 无冲突 → 测试通过 → 推送 | `83677bc` 进 main |
| D | 第一次 rebase 无冲突 → 测试通过 → 推送**被拒**（C 刚落地）→ 再 rebase，`calc.py`、`test_calc.py` 冲突 → 判定为实质性冲突 → `git rebase --abort` 恢复原状 → 列出 A/B/C 三个方案问主人 | 未推送，等待决策 |

要点：

- 推送被拒只说明基线前进；冲突在下一次 rebase 才暴露（D 第一次 rebase 并无冲突）。
- 实质性冲突时 agent 回滚 rebase 并把裁决交还给人，不会自己挑一边覆盖。
- 同一实验的第一轮：由另一个线程用 `send_thread_message(prompt: "ship")` 代发，两个线程**都拒绝执行**——消息带有「这条消息来自线程 X，不是用户本人」的元数据，而它们的主人先前说过不要推送。跨线程消息不能替代主人授权。

## 4. Space：协调者怎么安排合并

Space 协调者（agentMode `project-puck`）没有 git 工具，只能建线程、发消息、读线程状态、发帖。实测两种做法：

**有交付授权时（2026-11-28，本次实验）**：主人说「两个都完成后合并到 main 并推送，不用再问」。协调者给两个 worker 的首条提示词里都写明：
- 另一个线程正在并行改同一个函数、它要做什么；
- 主人已授权推送；推送前 fetch + rebase，若对方已落地，**按指定语义合并冲突**（两项行为都保留，并给出合并后的具体行为）；
- 被拒则重新 fetch、rebase、测试、推送；自己干活，不再开线程；完成后汇报落地内容和冲突如何解决。

结果：后推送的线程第一次推送被拒，rebase 后只在 `calc.py` 冲突，按协调者事先给出的语义合并、测试通过后推送。协调者用 `list_space_threads` 核对，每个线程都带 `produced: {commits, pullRequest, checks, landedSinceLatestRequest}`，确认后归档已完成线程。**语义冲突由协调者在派工时预先裁决**，worker 只负责执行。

**没有交付授权时（2026-11-07，OhMyAmpCode 首批插件）**：协调者指定一个「集成线程」负责骨架与合并，其余 worker 各自只改自己的目录、改动留在自己的 orb 不推送；集成线程用 `download_thread_changes` 拉取各 worker 的文件、合并并修正跨插件问题、跑完整测试，主人批准后协调者用 `send_thread_message(prompt: "ship")` 让集成线程交付。

工具层面的约束（转述工具描述）：
- `create_thread`：只有确需独立线程时才建；**除非用户说明了交付方式，否则不得在派工提示词里写推送、开 PR、合并或暂缓合并**。
- `send_thread_message` / `create_thread`：线程之间工作区和 checkout 不同，**消息不传文件也不传提交**；要区分源线程本地 `main` 与 `origin/main`，未推送的工作用文件传输工具。
- `download_thread_changes`：从服务端保存的 Changes 快照（相对默认分支 merge base）取改动文件的最终内容写到目标目录；删除的文件只报告不写；默认不覆盖。拷的是文件，不是 diff 或提交，不做合并；源 orb 暂停、归档、丢失也能取。
- 跨线程消息自动追加元数据：来源线程、「不是用户本人」、除非是你委派的工作否则不得未经同意并入其改动、要求回报时才回报。

官方文档对 Space 的说法只有「每个线程各自 Ship」；集成线程是协调者自己的选择。官方唯一明确的分工原则是：需要频繁协调或改同一批文件的任务留在一个线程里做（`docs/orbs/agent-to-agent`）。

## 5. Runner worktree

New Worktree 在仓库旁执行 `git worktree add`（`<repo>-<name>`，同名分支）；「Archive and Remove Worktree」先归档线程再 `git worktree remove` 并删分支，有未提交或未合并内容时不强制。合并留给用户。Oryn 的 `orbdrt/worktree.go` 与之一致。

## 6. Shipped 判定

提交进了默认分支、GitHub PR 已合并，或线程被 Ship 归档。提交只靠 `Amp-Thread-ID` trailer 关联线程（关掉 trailer 就不计为已 Ship）；PR 靠描述里的 `Amp-Thread-ID` 行。只覆盖 Amp 托管仓库、装了 Amp GitHub App 的仓库和 GitLab Push Updates。

## 7. 对照 Oryn

| 维度 | Amp | Oryn 现状 |
|---|---|---|
| 交付提示词可编辑 | 四个保存提示词，可改、可设成按钮 | `ship-prompt.ts` 写死 |
| rebase 后跑测试 | 必须，且规定何时可以不重跑 | 托管提示词没有这一步 |
| 实质性冲突 | 先问人 | 「resolving any conflicts」，agent 自行处理 |
| 浅克隆 add/add 兜底 | 有 | 无 |
| 托管仓库强推保护 | 主人凭据可强推、可删 main；线程凭据未验证；文档未提及 | 无（但有 30 天 saved ref） |
| 跨线程消息来源标注与授权约束 | 标注来源，并要求未经主人同意不得并入对方改动 | `coord/tools.go` 的 `sourceLine` 标注「sent by an agent, not typed by the owner」，没有并入改动的约束 |
| 派工时预先裁决语义冲突 | 协调者自发 | `internal/coord` 里没有分支、推送、合并相关指引 |
| 集成线程 + 拉取别人改动的工具 | `download_thread_changes`：任一线程一次取回另一线程整份改动（取自服务端快照，源 orb 不在也行） | 只有全局助手能中转：`get_thread_changes` 列出改动 → 逐个 `fetch_thread_file` 存进文件库 → `put_thread_file` 或 `create_thread` 的 `files` 放进集成线程（`coord/filetools.go:101-117`，读文件时会唤醒源 orb）；工作线程和 Space 协调者没有这些工具 |
| 汇总交付状态 | `list_space_threads.produced.landedSinceLatestRequest` | 线程有「已交付」标记；Space 侧是否汇总未核对 |
