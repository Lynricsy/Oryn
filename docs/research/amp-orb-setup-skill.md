# Amp `orb-setup` 技能要点（2026-11-12 调研）

来源：在 Amp orb 线程 `T-01a1d028-5226-776c-bbc1-461e955ce3e7` 中让 Amp agent 加载其官方技能 `orb-setup`（`~/.cache/amp/global-skills/ampcode.com/official/orb-setup@e79d76ef4840f052/SKILL.md`，13 KB，无附加文件），并说明它的实际做法。本文只记要点；技能原文属 Amp，不收录进本仓库。

## 触发与定位

- 技能描述：创建或更新 `.agents/setup`、`.agents/resume`、项目 pre-setup 脚本时使用，或排查 orb 环境 setup 问题时使用。
- 系统提示词只有一两句：缺系统工具时参考 orb 文档；项目 setup 提交到 `.agents/setup`，无法提交时用项目 pre-setup 脚本（`amp projects get|update --pre-setup-script-file`）。细节全部放在技能里，按需加载。

## 生命周期文件

| 文件 | 运行时机 | 用途 |
|---|---|---|
| 项目 pre-clone 脚本（存项目设置） | clone 之前，在 `/home/user/workspace` | 只放 clone 所需的前置条件 |
| 项目 pre-setup 脚本（存项目设置） | 每次 setup 运行前 | 不能提交进仓库的准备步骤 |
| `.agents/setup` | 新 orb 一次（精确快照命中则跳过） | 工具链、依赖、数据库 |
| `.agents/resume` | 首次激活与每次唤醒 | 快速、幂等的认证 / 重连 / 自检 |

## 关键约束

- 快照：setup 成功后保存项目快照，最长复用 72 小时；快照过期时恢复旧快照、更新仓库、**重跑 setup** → setup 必须非交互、幂等、在热文件系统上快。
- 修改 pre-setup / pre-clone 脚本会改变快照身份，下一个新 orb 自动重跑，不需要清快照；只有快照本身坏了才删。
- 时限：setup 20 分钟强制停止并继续启动；5 分钟出现连接警告；目标 2 分钟内。失败或超时不发布新快照。
- setup 的产物会跨线程复用：**setup 里不做任何用户 / 线程认证**，只装客户端，认证放到 resume。
- resume 最多等 10 秒，超时则继续并让它在后台跑；10 秒内非零退出会向用户展示失败；不要在 resume 里装依赖，也不要把慢步骤放后台（会掩盖失败）。
- 两个脚本要带可执行位提交。
- 脚本退出时残留的进程（含后台命令与自动拉起的守护进程）都会被杀，不会进入快照；常驻服务写进 `.amp/services.yaml`。

## 工作流

0. 先决定放在哪：用户要求不提交文件 → 走「项目设置路径」：脚本写到仓库外的临时文件，`bash -n` 检查，在仓库根跑两次验证热跑，存为项目 pre-setup 脚本，用 `amp projects get` 确认，报告「对以后的 orb 生效，不需要推送」，全程不改仓库；需要的 resume / services 文件由脚本在运行时生成并加入 `.git/info/exclude`；令牌与配置值从项目环境变量 / 密钥读取，不写进脚本。
1. 调查仓库：包管理器与锁文件、工具链版本钉（`.nvmrc`、`mise.toml`、`rust-toolchain.toml`、`packageManager`）、env 模板、数据库等后端服务、Nix / devenv / direnv 这类开发 shell、README / CONTRIBUTING / AGENTS.md、**CI workflow 的安装步骤**（最可靠的依赖清单）、已有的 setup / resume（原地更新，保留无法验证的行为）。
2. 写 setup：`#!/usr/bin/env bash` + `set -euo pipefail`；apt 包先检查再装（快照过期重跑时别重复 `apt-get update`）；装钉定版本的工具链；刚装的工具要 `source` 其 profile 才能在本脚本后续使用；按锁文件装依赖；数据库启动并灌数据、调成速度优先；复制 env 模板不覆盖（`[ -f x ] || cp`，不用 `cp -n`）；可选写 orb 专用的 agent 说明；每个主要步骤打印并计时。
3. 持久化开发环境：agent 与受管服务都在新的非交互登录 shell 中启动，开发 shell 的完整环境（如 `NODE_PATH`）必须在那里可用；用幂等、限定仓库目录、防递归的 `~/.bash_profile` 钩子（Nix 用 `nix print-dev-env` 而非 `nix develop`）。
4. 写 resume：同样的开头；只做 10 秒内能完成的认证、重连、自检，保持前台运行，慢的写日志。
5. 验证：`chmod +x`；在 orb 里直接运行 setup 两次并计时（热跑应无下载）、运行 resume；排查时看 `~/.cache/amp/logs/setup.log`，先加计时再加缓存；用干净的最小环境 `bash -lc` 只检查预期变量与 `command -v`（不要 dump 可能含密钥的环境）；受管服务用 `services ensure` 并检查进程 environ 中的预期项；不要为测试 profile 重启 agent 进程（会终止当前线程）。
6. 收尾：`.gitignore` 覆盖 `.amp/portals/*`；带可执行位提交；**只有进入项目默认分支才对新 orb 生效**，未推送时必须如实说明，推送前征得用户同意，确认远端已有该提交才给链接。

## Amp agent 补充的判断标准

- 两次 setup 都返回 0（用 `tee` 时要 `set -o pipefail`，否则失败也会显示 0）；热跑明显更快、无下载、无重复写配置；resume 几秒内返回 0；干净登录 shell 中路径与标记变量正确；最后跑一遍项目自己的构建 / 测试。
- 要端到端确认，开一个新的 orb 线程检查环境。

## 对 Oryn 的取舍

- 采用：按需加载的技能 + 极短的系统提示词段落；仓库路径与项目设置路径两种方案；幂等 / 不留进程 / setup 不认证 / resume ≤10 秒的约束；跑两次计时验证；只有合入基线分支才算完成。
- 改动：文件名 `.oryn/setup`、`.oryn/resume`（兼容读取 `.agents/*`）；日志 `~/.cache/oryn/logs/`；试跑用 `oryn orb setup run`（经 orbd 执行，结果会上报控制面并出现在线程界面），而不是直接执行脚本；项目脚本用 `oryn orb setup project-script set`，只能改本 orb 所属项目；不做 pre-clone（Oryn 的 clone 经 egress 注入凭据，没有 clone 前置条件）；orb 基础镜像为 Debian 13。
- 暂缓：快照（M11）；`.oryn/services.yaml`（M4）。
