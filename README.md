# Oryn

带高级环境与跨平台客户端的 agent 系统：每个线程一台隔离机器（orb），agent 内核为 [Oryn Core](https://github.com/Lynricsy/oryn-core)（fork 自 [omp](https://github.com/can1357/oh-my-pi)，MIT），客户端覆盖 Web / 桌面 / 移动 / CLI，外围（环境面板、Portal、分享、统计）参照 Amp 最新版。

> 状态：M1（协议骨架）已完成，M2（线程核心）进行中。完整计划见 [`docs/plan-m1-m2.md`](docs/plan-m1-m2.md)。

## 组成

| 组件 | 说明 |
|---|---|
| `orynd` | 控制面：REST + WebSocket、SQLite 存储、Passkey/设备鉴权、orb 生命周期、密钥注入出站代理 |
| `orbd` | orb 内守护进程：经出站长连接（WebSocket + yamux）与控制面通信，监督 Oryn Core 进程、复制会话 JSONL、提供本地出站代理 |
| `oryn` | 命令行客户端，含 `oryn provider run`（在 Docker 主机上执行 orb 任务） |
| `web/` | React + TypeScript 客户端（Web/PWA；桌面与移动端以 Tauri 2 套壳，后续里程碑） |
| `images/orb/` | orb 基础镜像（Debian 13 + Node/bun/Python/uv/Go/Rust/gh + Oryn Core） |

```mermaid
flowchart LR
  C[Web / CLI] -- REST + WS --> D[orynd]
  P[oryn provider run] -- 长轮询拉任务 --> D
  P -- docker run/pause/commit --> O
  subgraph O[orb 容器]
    B[orbd] -- JSONL --> A[Oryn Core<br/>omp --mode rpc-ui]
  end
  B -- WS + yamux 出站长连接 --> D
  D -- 注入真实密钥 --> L[模型 API / git]
```

关键约束：真实 API key 从不进入 orb——下发的 `models.yml` 只含占位令牌，请求经 orbd 的 `127.0.0.1:7811` 回到控制面，在那里按路由绑定替换为真实值，响应中的回显会被换回占位符。

## 开发

依赖：Go 1.27、bun 1.4、Docker、`sqlc`（`go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1`）。

```bash
make gen          # 由 schema/openapi.yaml 与 SQL 生成 Go / TS 代码
make web          # 构建前端并嵌入 orynd
make build        # bin/orynd bin/orbd bin/oryn bin/fakeomp
make test
scripts/fetch-core.sh && make image    # 拉取钉定的 Oryn Core 发行版并构建 oryn-orb:dev
./bin/orynd --config dev/orynd.yaml    # 首次启动打印 Passkey 初始化链接
```

`dev/orynd.yaml` 的第二个 `listen` 是本机 docker0 网桥地址（用 `ip -4 addr show docker0` 查看），供本机 orb 直连。

## 常用命令

```bash
oryn login --url https://oryn.example.com     # 设备配对（在网页上输入设备显示的验证码并批准）
oryn config push ~/.omp/agent                  # 上传 omp 文本配置，明文 key 自动移入加密密钥库
oryn config pull                               # 拉回文本配置（models.yml 默认另存为 models.oryn.yml）
oryn provider create local > provider.token    # 创建 orb provider，令牌只显示一次
oryn provider run --config /etc/oryn/provider.yaml
oryn secrets set git/github.com/token          # git 令牌，经出站代理注入 clone/push
```

## orb 环境自配置

项目的 orb 环境由生命周期脚本配置：仓库内 `.oryn/setup`（新 orb 首次就绪时运行一次，兼容读取 `.agents/setup`）、`.oryn/resume`（每次唤醒，至多等 10 秒），或存在项目设置里、不进仓库的项目脚本。脚本失败不会阻止 orb 就绪，线程里会出现提示条，可查看日志并一键交给 agent 修复。

orb 内的 agent 自带 `oryn-setup` 技能：让它「为这个项目配置 Oryn orb 环境」，它会调查仓库、写出脚本、试跑并推送到基线分支。orb 内可用：

```bash
oryn orb setup run [--resume]                         # 重跑 setup / resume（经 orbd，结果上报控制面）
oryn orb setup status | logs [--resume]               # 最近结果与日志
oryn orb setup project-script get|set setup --file f  # 读写本 orb 所属项目的项目脚本
```

## 文档

- [`docs/deploy.md`](docs/deploy.md)：部署到 1Panel OpenResty 反代之后
- [`docs/protocol.md`](docs/protocol.md)：协议索引与相对计划的偏差
- [`docs/research/`](docs/research/)：Amp / omp / 参考 GUI / Tauri 移动端调研
- agent 内核：[`Lynricsy/oryn-core`](https://github.com/Lynricsy/oryn-core)（钉定版本见 `core/source.json`；二进制与配置目录沿用 omp 的 `omp` / `~/.omp/agent`）
