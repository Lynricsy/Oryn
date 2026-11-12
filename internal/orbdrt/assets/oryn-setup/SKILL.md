---
name: oryn-setup
description: "配置或修复 Oryn orb 环境：编写 .oryn/setup、.oryn/resume 或项目 setup 脚本；用户要求配置 orb 环境、setup 失败 / 过慢 / 缺依赖时使用。"
---

# Oryn orb 环境配置

目标：让这个项目的每一台新 orb 自动装好开发所需的一切，agent 在新线程里可以直接构建、运行测试。

## orb 是什么样的

- Debian 13，用户 `user`，`sudo` 免密；`$HOME=/home/user`。
- 有仓库的项目检出在 `~/workspace/repo`（setup / resume 的工作目录就是仓库根）；没有仓库时为 `~/workspace`。
- 基础镜像已带：git、curl、build-essential、python3 + uv、Node.js LTS、bun、Go、rustup（minimal）、gh、ripgrep、jq、tmux。没有 Docker daemon。
- 访问 git 主机与模型 API 都经 orb 内的代理注入凭据，脚本里**不要**写令牌，也不需要 `gh auth login`。

## 生命周期文件

| 文件 | 何时运行 | 放什么 |
|---|---|---|
| 项目 setup 脚本（Oryn 项目设置） | 每次 setup，先于仓库内脚本 | 不能或不想提交进仓库的准备步骤 |
| `.oryn/setup`（回退 `.agents/setup`） | 新 orb 首次就绪时一次（成功后写 `~/.oryn/setup.done`，之后跳过） | 系统包、工具链、依赖、数据库、初始数据 |
| 项目 resume 脚本 / `.oryn/resume`（回退 `.agents/resume`） | 首次激活与每次唤醒；最多等 10 秒，之后转入后台 | 快速、幂等的重连、自检、刷新短期凭据 |

硬约束：

- setup **非交互**（不读 stdin、`apt-get -y`、`DEBIAN_FRONTEND=noninteractive`）、**可重复执行**（第二次运行应快且无副作用）、20 分钟内结束，目标 2 分钟内。
- setup 结束时不要留下常驻进程：脚本退出后残留进程不保证继续存在。需要的数据库等服务在 resume 里确保已启动（快速、幂等）。
- setup 里不做任何用户 / 线程级认证：setup 的产物以后可能被其它线程复用。
- resume 必须在 10 秒内返回且保持前台运行；不要在 resume 里装依赖，也不要把慢步骤丢到后台（会掩盖失败）。
- 仓库内脚本要带可执行位提交（`chmod +x`），否则不会被运行。
- setup / resume 失败不会阻止 orb 就绪，但用户会在线程里看到失败提示；日志在 `~/.cache/oryn/logs/setup.log` 与 `resume.log`。

工具：

- `oryn orb setup run`：经 orbd 用本 orb 的参数重跑 setup（忽略 `setup.done`，不重新 clone），实时输出，退出码 0 = 成功。
- `oryn orb setup run --resume`：重跑 resume 并等到结束。
- `oryn orb setup status [--json]`：最近一次 setup / resume 的结果与耗时。
- `oryn orb setup logs [--resume] [-n N]`：查看日志末尾。
- `oryn orb setup project-script get [setup|resume]`、`oryn orb setup project-script set setup|resume --file <路径>`：读写本 orb 所属项目的项目脚本（空文件 = 清除）。

## 第 0 步：先决定放在哪

- **仓库路径（默认）**：脚本写进 `.oryn/setup`、`.oryn/resume` 并提交。已有 `.agents/setup` / `.agents/resume` 时原地更新它们，不要再新建一份 `.oryn/` 版本。
- **项目脚本路径**：用户要求「不要提交 / 不要改仓库」时走这条。全程不改仓库工作区：
  1. 把脚本写到仓库外的临时文件（如 `/tmp/oryn-setup.sh`），`bash -n` 检查语法。
  2. 在仓库根用 `bash /tmp/oryn-setup.sh` 连续运行两次，确认第二次快且成功。
  3. `oryn orb setup project-script set setup --file /tmp/oryn-setup.sh`（resume 同理），再 `oryn orb setup project-script get` 确认已保存。
  4. `oryn orb setup run` 再验证一次（此时用的就是刚保存的项目脚本）。
  5. 告诉用户：已保存到项目设置，对以后的新 orb 生效，不需要提交或推送；`git status` 应无改动。

## 第 1 步：调查仓库

按可靠程度依次看：

1. CI 配置（`.github/workflows/*`、`.gitlab-ci.yml` 等）里的安装与测试步骤——这是最可信的依赖清单。
2. 包管理器与锁文件：`package-lock.json` / `pnpm-lock.yaml` / `yarn.lock` / `bun.lock`、`uv.lock` / `poetry.lock` / `requirements*.txt`、`go.sum`、`Cargo.lock`、`Gemfile.lock` 等。
3. 版本钉：`.nvmrc`、`.node-version`、`.python-version`、`.tool-versions`、`mise.toml`、`rust-toolchain.toml`、`package.json` 的 `packageManager` / `engines`、`go.mod` 的 `go` 行。
4. 环境变量模板（`.env.example`、`.env.sample`）、数据库与其它后端服务（docker-compose 文件里列的服务要改成在 orb 里直接安装运行）。
5. Nix / devenv / direnv 之类的开发 shell，README、CONTRIBUTING、AGENTS.md 里的开发说明。
6. 已有的 setup / resume 与项目脚本（`oryn orb setup project-script get`）：在其基础上改，保留你无法验证的行为并说明。

## 第 2 步：写 setup

- 开头 `#!/usr/bin/env bash` 与 `set -euo pipefail`；每个主要步骤先打印一行说明，并记录耗时（如 `SECONDS`）。
- 系统包：先检查再装，只在确实缺包时才 `sudo apt-get update && sudo apt-get install -y …`，避免每次重跑都刷新索引。
- 工具链：按版本钉安装；刚装好的工具若要在本脚本后面用，先 `source` 它的环境脚本或把目录加进 `PATH`。
- 依赖：按锁文件安装（`npm ci`、`pnpm install --frozen-lockfile`、`uv sync --frozen`、`go mod download` 等）。
- 数据库：安装、初始化、灌种子数据都要幂等（已初始化则跳过）；开发环境优先速度（例如关掉 fsync）。
- 环境文件：`[ -f .env ] || cp .env.example .env`，不覆盖已有文件。
- 不要启动需要一直运行的服务；不要写任何令牌。

## 第 3 步：让开发环境在新 shell 里可用

setup 进程里 `export` 的变量随脚本结束就没了。Oryn 的规则：

- agent 进程在**启动时**取一次登录 shell（`bash -lc`，工作目录为仓库根）的环境；setup / resume 的每个脚本也在登录 shell 环境下运行。
- agent 的每条 bash 命令 = agent 进程环境 + `~/.bashrc` 的快照（函数、别名、PATH）。

所以需要持久的 `PATH`、`NODE_PATH`、虚拟环境等写进 **`~/.profile`**（Debian 默认的登录配置；不要新建 `~/.bash_profile`，它存在时 bash 会跳过 `~/.profile`）。写法要幂等（重复运行不重复追加）、不递归：

```bash
hook='# >>> oryn project env >>>'
if ! grep -qF "$hook" ~/.profile 2>/dev/null; then
  cat >> ~/.profile <<'SH'
# >>> oryn project env >>>
export PATH="$HOME/workspace/repo/node_modules/.bin:$HOME/.local/bin:$PATH"
# <<< oryn project env <<<
SH
fi
```

改动对**之后启动的** agent（新 orb、新线程）生效，当前对话里的 agent 环境不会变：验证时用干净的登录 shell（见第 5 步），不要靠 `source` 改当前 shell 来冒充。Nix 项目用 `nix print-dev-env` 生成环境，不要在 profile 里跑 `nix develop`。

## 第 4 步：写 resume（按需）

只有确实需要时才写：启动 setup 装好的数据库、刷新短期凭据、修复唤醒后失效的状态。同样的开头，几秒内结束，慢的部分写日志而不是放后台。

## 第 5 步：验证

1. `chmod +x .oryn/setup .oryn/resume`。
2. `time oryn orb setup run` 运行两次：两次都退出 0；第二次明显更快，没有下载与重复写配置。失败时看输出与 `oryn orb setup logs`，先加计时找慢步骤，再考虑缓存。
3. `oryn orb setup run --resume`：几秒内返回 0。
4. 用干净的登录 shell 检查环境，只看预期的变量与命令，**不要**打印整个环境（可能含密钥）：
   `env -i HOME="$HOME" USER="$USER" PATH=/usr/local/bin:/usr/bin:/bin bash -lc 'cd ~/workspace/repo && command -v node pnpm uv && node --version'`
5. 运行项目自己的构建与测试，确认能直接通过。
6. 不要为了验证 profile 重启 agent 进程（会中断当前线程）；要端到端确认，请用户新开一个线程。

## 第 6 步：收尾

- 仓库路径：带可执行位提交脚本（以及需要的 `.gitignore` 改动）。**只有合入项目基线分支后，新 orb 才会用上**：用户已要求推送就直接推送到基线分支（或按仓库流程开 PR）；用户没提时先询问再推送。确认远端已经有这个提交之后才能说「完成」；未推送时如实说明「脚本已提交在本地 / 当前分支，合入基线分支后生效」。
- 项目脚本路径：确认 `git status` 干净，报告已保存到项目设置。
- 总结：装了什么、两次 setup 的耗时、resume 做了什么、项目测试结果，以及任何无法在 orb 里验证的部分。
