---
name: oryn-boxes
description: "把应用部署到 Oryn box（常驻机器）：用户想让一个应用一直在线、有自己的地址，要求部署到 box，新建 / 启停 box，设置应用密钥，或在 box 上执行命令时使用。"
---

# Oryn Boxes

## box 是什么

- box 是一台常驻机器：不空闲冻结、不随线程回收、掉线自动拉起。一台 box 可以跑多个应用（app），每个 app 有自己的地址 `https://<app>.<portal_domain>/`、自己的密钥和数据目录。
- 分工：**在线程的 orb 里构建，box 只运行**。`oryn boxes deploy` 在当前 orb 里构建并打包 `HEAD` 提交，送上 box；box 切换 release、重启服务、等端口监听，不监听就回滚。box 保留 live 加前两个 release。
- app **默认公开**（任何拿到地址的人都能打开）；`--private` 只让主人登录后访问。应用可读请求头 `X-Oryn-Authenticated: owner=yes|no` 区分访问者。
- 用户说「部署到 box」而还没有 box 时，直接 `--create` 新建，不用先问。

## `.oryn/box.yaml`

放在 app 目录（单 app 仓库就是仓库根）里，**必须提交**：部署的是 `HEAD` 提交，未提交的改动（包括 box.yaml 本身）不会带上。

| 字段 | 含义 |
|---|---|
| `build` | 构建命令，在 app 目录里用 `bash -lc` 跑；能看到 `ORYN_DEPLOYMENT_URL` / `NAME` / `BOX`，看不到密钥与数据目录 |
| `source` | 缺省 `true`：release 带 app 的源码、Git 历史与已装依赖，box 上的线程能读改线上代码；`false`：只带 `artifacts` |
| `artifacts` | `source: false` 时必填：打进 release 的文件 / 目录（相对 app 目录） |
| `include` | 单仓多 app 时 app 还需要的其它目录（相对 app 目录，如 `../packages/ui`） |
| `services` | 1–4 个服务：`<名字>: {command, cwd}`；名字是小写字母数字和 `-`，至多 10 个字符；`cwd` 相对 app 目录 |

服务**必须监听 `$PORT`**（同一 app 的服务共用这个端口，通常只有一个对外监听）。进程还拿到：

- `ORYN_DEPLOYMENT_URL`：app 地址；`ORYN_DEPLOYMENT_NAME`：app 名；`ORYN_DEPLOYMENT_BOX`：box 名
- `ORYN_DEPLOYMENT_DATA_DIRECTORY`：数据库、上传文件等要跨部署保留的数据**只写这里**（release 目录每次部署都会换）

只带构建产物（可执行文件放进声明为 artifact 的目录，服务 `cwd` 指到那里）：

```yaml
build: go build -o dist/server ./cmd/server
source: false
artifacts: [dist]
services:
  web:
    command: ./server
    cwd: dist
```

单仓多 app（每个 app 目录一份 box.yaml，在各自目录里首次部署）：

```yaml
build: pnpm install --frozen-lockfile && pnpm --filter crm build
include:
  - ../packages/ui
services:
  web:
    command: node build
```

## 命令速查

```sh
oryn boxes deploy --on <box> --name <app> --create   # 首次部署（没有 box 就新建）；记下 target 到 .oryn/box-deploy.json
oryn boxes deploy                                     # 之后在 app 目录里直接部署
oryn boxes deploy --private | --public                # 切换访问方式（之后的部署保持）
oryn boxes deploy --move                              # 把已有 app 改绑到当前仓库 / 目录
oryn boxes list | show <box> | deployments <box>
oryn boxes create <name> | start <box> | stop <box> | rename <box> <new>
oryn boxes schedule <box> --on 08:00 --off 22:00 --tz Asia/Shanghai   # 每日运行时段；--clear 清除
oryn boxes secrets set NAME --box <box> --app <app> --value V   # 或 --data-file F；--plain = 明文环境变量
oryn boxes restart <box> --app <app>                  # 密钥改动要重启（或再部署）才生效
oryn boxes logs <box> --app <app> [--service web]
oryn boxes exec <box> [--app <app>] -- <命令>          # 在 orb 里至多 30 分钟（--timeout），不能中途取消
oryn boxes cp ./file <box>:path --app <app>           # box 路径相对 app 数据目录（不给 --app 则相对 ~）；目标目录要先存在
oryn boxes rollback <box> --app <app> [--to <releaseId>]
oryn boxes undeploy <box> --app <app>                 # 数据目录保留
```

## 出错时

- 部署失败时先读 CLI 打印的错误码和日志尾：`not_listening` = 服务没在 `$PORT` 上监听（看日志、确认命令与 `cwd`）；失败的部署已经回滚，线上仍是上一个 release。
- `box_changes`：box 上的线程改过线上代码（未提交或提交了却不在要部署的分支里）。在 box 上的线程里提交并推送到部署的分支，或确认不需要后加 `--discard-box-changes`。`--move` 到另一个仓库时两边历史不相干，必须同时加 `--discard-box-changes`。
- `app_repo_mismatch`：app 绑定在别的仓库 / 目录，确认要改绑再加 `--move`。
- `deploy_in_progress` 一直不消失：`oryn boxes cancel-deploy <box> --app <app>`。
- `box_stopped`：box 被主人或运行时段停了，请主人在 Boxes 页启动，不要自己反复重试。
- `release_too_large`：release 超过 512 MiB，改用 `source: false` 加 `artifacts` 只带构建产物。

## 不能做的

- 删除 box：请主人在 Boxes 页或自己的机器上删。
- 自动化线程（定时、GitHub 触发）不能使用 box。
