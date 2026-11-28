# 开发与验证

本页面向参与 Oryn 开发的人：代码生成、构建目标、测试与验收脚本。第一次在本机跑起 Oryn 见 [README 的快速开始](../README.md#快速开始)。

## 构建与校验目标

```bash
make gen            # 由 schema/openapi.yaml 与 SQL 生成 Go / TypeScript 代码
make check-gen      # 校验生成物与源码完全同步（CI 门禁）
make lint-spec      # 使用 Redocly 校验 OpenAPI 规格
make test           # 运行 Go 全量单元与集成测试（前端测试在 web/ 目录执行 bun run test）
make desktop        # 构建当前平台 Tauri 2 桌面安装包
make android        # 构建 Android APK（DIST=foss 或 DIST=gms）
make fc-artifacts   # 构建锁定摘要的 Firecracker 二进制、vmlinux 内核与 ext4 根盘
```

## Web 校验

CI 入口：

```bash
cd web && bun run check && bun run test && bun run build
```

`check` 同时运行 Biome 与 TypeScript。预算文字的对比度回归覆盖 6 个主题色的明暗两套主题，要求至少达到 4.5:1。

## 测试约定

- 推送前运行完整校验入口，不能用单文件复验代替全量测试。
- 组件卸载保存、测试宿主启动等后台工作必须在清理时完成，之后才能恢复 mock、定时器或删除临时目录，避免跨用例污染。
- ext4 镜像测试需要 `mke2fs`。设备节点用例另需创建普通设备的权限，无权限时只跳过设备节点断言，其余镜像读取与校验照常运行。Linux 允许无特权创建 `0:0` whiteout，因此不能仅用 whiteout 判断设备创建权限。

## Artifacts 端到端验收

```bash
# 需要 root、git、curl、sqlite3、/usr/bin/chromium；先 make web && make build（或用 M14A_BIN 指向含四个二进制的目录）
cd web && bun run accept:m14-artifacts                    # mock 与真机两部分，逐条打印 K1–K43 的 PASS / FAIL / SKIP
bun run accept:m14-artifacts -- --part real --only K24,K25 # 只跑指定部分与条目
```

真机部分自建一套隔离的 orynd、vite 与 fakeomp。K37（`sample` 能力）要真实模型回答，fakeomp 下打印 SKIP；控制面渲染器不可用时，K22、K23、K26 的 PDF 与 K31 的附图同样 SKIP。截图与结果默认写到 `.dev/m14-artifacts/`。

## Boxes 端到端验收

```bash
# Go 端到端（自建进程内控制面、process 驱动 orbd 与 fakeapp，无需外部 Docker）
go test ./internal/server/ -run 'TestE2EM23' -count=1 -v

# 浏览器真机验收（需要真实开发栈，前置条件同 accept:m4：运行中的 orynd + Docker provider、
# portal_domain: localhost、已登录的 .dev/credentials.json 或 ORYN_TOKEN，以及本机 Chromium）
cd web && bun run accept:m23
```

## 视图端到端验收

```bash
# 需要 root、docker、sqlite3、/usr/bin/chromium、钉住的 Core（build/core/x64/omp）与含真实模型的 ~/.omp/agent/models.yml；
# 先 make web && make build（视图帧的运行时与 chart.js / tailwind 是嵌进 orynd 的），orb 基础镜像默认 oryn-orb:dev（make image）
cd web && bun run accept:m33                 # 逐条打印 T1–T11 的 PASS / FAIL / SKIP 与证据
bun run accept:m33 -- --only T1,T8          # 只跑指定条目
```

脚本自建一套隔离环境，不碰开发栈：全新 data_dir 的 orynd（缺省 `127.0.0.1:7733`，`portal_domain` 为空，视图帧走 `/_af/`）、钉住 Core 的协调宿主（全局助手）、在基础镜像上只替换 orbd / oryn / omp 的薄层镜像与 docker provider；临时目录在 `/var/tmp/oryn-m33-<run>/`，结束时删掉进程、orb、镜像与目录（`M33_KEEP=1` 保留目录与日志）。工作线程与全局助手都用真实模型，提示词点名调用参数，断言只看转录条目、REST 状态与页面 DOM。截图默认写到 `.dev/m33/`。

环境变量：`M33_BIN`、`M33_PORT`、`M33_COORD_EGRESS`（缺省 7983）、`M33_COORD_DIR`（协调宿主目录的父目录，缺省 `/var/tmp`）、`M33_BASE_IMAGE`、`M33_MODELS`、`M33_MODEL`（缺省 `claude-pro/claude-sonnet-5-5`）、`M33_SHOTS`、`CHROMIUM`、`M33_KEEP`、`M33_HOLD=1`（条目跑完后保持环境运行并打印设备令牌与会话 Cookie，Ctrl-C 再收尾，排查用）。线程停在 error（例如上游模型拒绝）时等待立即结束并带出错误原文。

可能的 SKIP：本机访问不了公网时 T5 的 https 图片加载未验证（fetch 被拒、帧读不到父页面照常断言）；T11 依次要模型给出三种需要 Core 修复的参数（`blocks` 二次编码、`blocks` 写成单个对象、`progress.value` 写成字符串），模型都给了合法参数时 Core 的参数修复没有被触发。T7 依赖 T1、T4，T10 依赖 T1，前置没过时判 FAIL。T6 与 T9 的主会话工具表取自 omp `get_active_tools`（确定性）；子 agent 与 `/tan` 克隆的 `session_init.tools` 是 Core 在扩展 `session_start` 之前写的、`session_init.systemPrompt` 是 `before_agent_start` 的输入，都不反映扩展的调整，所以发给模型的工具表与系统提示词看子 agent 的自述（`TOOLS=… GUIDE=… ORB=…`，先用 `session_init` 核对自述里的工具名都真实存在；`/tan` 克隆的 orb 指引应只有一段）。上游网关拒答（`policy_blocked`）时该条立即 FAIL 并带出原文，可用 `M33_MODEL` 换一个提供方重跑。

## 界面截图

| 命令（`web/` 目录） | 输出 |
|---|---|
| `bun run shots:all` | 全界面矩阵（明暗 × 宽度 × 界面清单），写到 `/tmp/oryn-shots`，参数见 `scripts/screenshots.ts` 文件头 |
| `bun run shots:readme` | README 与本目录文档用的配图，写到 `docs/assets/` |

两者都起 `VITE_MOCK=1` 的 vite 开发服务器并用本机 Chromium 截图，不需要后端。

README 的手机三联（`mobile-views`）与 iPhone Duo 双拼（`duo-views`）用 iOS 模拟器里真实 App 的截图：原生液态玻璃底栏与页头由 UIKit 绘制，浏览器里没有；Duo 在同一会话里先截合上时的外屏、再展开截内屏（横放），折叠由 duoctl 驱动（见 `web/src-tauri/ci/ios-sim.sh` 的 `duo_setup`）。原图由 GitHub Actions 的 [`readme-ios-shots`](../.github/workflows/readme-ios-shots.yml) 工作流产出（推送 `readme-shots/**` 分支或手动运行；脚本是 `web/src-tauri/ci/ios-readme-shots.sh`，演示数据与 `shots:readme` 同一份），下载产物后在本机合成：

```bash
gh run download <运行 id> -n readme-ios-shots -D /tmp/readme-ios-shots
cd web && ORYN_IOS_SHOTS=/tmp/readme-ios-shots bun run shots:readme --only mobile-views,duo-views
```

没设 `ORYN_IOS_SHOTS` 时 `shots:readme` 跳过这两组，保留现有配图。

## UI 排版审计

`web/e2e/ui-audit/` 覆盖全部可达界面及其浮层（下拉、弹窗、抽屉、菜单），清单见 [`docs/ui-audit/inventory.md`](ui-audit/inventory.md)。每个界面都在 6 个视口下跑：1920×1080、1366×768、390×844 触屏，各分明暗两套主题。

```bash
cd web
npx playwright test e2e/ui-audit                  # 断言 + after 截图；自动起 VITE_MOCK=1 的 vite（端口 5293）
UI_AUDIT_PHASE=before UI_AUDIT_WEB=<基线 worktree>/web npx playwright test e2e/ui-audit   # 基线截图（端口 5294）
bun e2e/ui-audit/inventory.ts                     # 由 docs/ui-audit/areas/*.md 生成总清单，并与 spec 对账
sh e2e/ui-audit/sheet.sh <界面 id>                # 拼出 before / after 对照图
```

root 下 `e2e/ui-audit/global-setup.ts` 每次运行自动把 Chromium 放进本次独占的、只有回环的网络命名空间（`e2e/ui-audit/netns.ts`），运行结束删掉：本机 Docker orb 的 veth 不停增删，Chromium 收到网卡变化会中止在途请求（`net::ERR_NETWORK_CHANGED`），vite 的模块图加载到一半被掐断就白屏、等待超时。非 root 时直接起 Chromium，跑 Docker 的机器上可能出现这种假失败。

浮层会在三个时机各断言一次：首次打开、内部滚到底、关掉后重开。检查四项：

- 网格采样 `elementFromPoint`，确认没有被遮挡或被祖先 `overflow` 裁切；
- 没有超出视口；
- 玻璃外壳本身不是滚动容器；
- 玻璃及其祖先都不是 Backdrop Root（opacity、filter、mask、clip-path、backdrop-filter），打开那一刻也没有动画动到这些属性。祖先链走到顶层元素（打开的 popover、模态 dialog）为止：顶层元素不受 DOM 祖先的透明度和裁切影响。

此外三个时机各记一份玻璃材质（底色、`::before` 的 backdrop-filter、累乘透明度），任何一项在三次之间变了都算问题。页面级只断言没有横向溢出。

截图和每个界面的结果 JSON 写到 `docs/ui-audit/shots/<id>/<project>-<phase>.*`，不入库。结果分两段终审：用例体跑完写 `recorded`，`result-reporter.ts` 在 Playwright 给出最终状态（含 fixture 清理）后改成 `done` 或 `failed`；只按 `only` 显式跳过的写 `skipped`。`inventory.ts` 只认 `done` 和 `skipped`，任何不一致（spec 与清单 id 对不上、某个组合不是这两种状态）都以非零退出。

注意事项：

- 不要在命令行加 `--reporter`：它会替换配置里的 reporter，`result-reporter.ts` 不跑，结果全部停在 `recorded`。
- 并行跑多批时，先用常驻进程把 5293 / 5294 的 vite 起好（`reuseExistingServer`），否则先结束的那批会关掉它自己起的 vite，掐断别的批次；同一个 phase 的同一个用例不要两批同时跑，它们写同一个结果文件。
- 宿主壳界面（Tauri 窗口栏、连接页、手机壳）用 `web/scripts/fake-host.ts` 注入假的 `window.__TAURI__`，与截图脚本共用。
- 基线 worktree 要单独执行 `bun install`：直接软链主仓库的 `node_modules` 会共用 `.vite` 预构建缓存，或者出现两份 React。
- 截图必须用 SwiftShader（配置里已加 `--use-angle=swiftshader`）；默认软件合成器在 DPR 2 下 backdrop-filter 只模糊半个盒子。

新写浮层一律用 `web/src/components/ui/Floating.tsx`，或基于它的 `Menu`、`Popover`。它通过 Popover API 进入顶层，并按锚点定位；玻璃画在不滚动的外壳上，内容在内层滚动。

## 跨架构构建 ARM64 镜像

在 x64 宿主上执行 `scripts/build-orb-image.sh arm64` 构建 ARM64 镜像时，跨架构离线冒烟需要宿主的 QEMU `binfmt_misc` 注册开启 `C`（凭据）标志，否则容器内 `sudo` 会报告 effective uid 不为 0。

在装有 `qemu-user-static` 的 Arch Linux 宿主上，把 `/usr/lib/binfmt.d/qemu-aarch64-static.conf` 复制到 `/etc/binfmt.d/qemu-aarch64-static.conf`，只把规则末尾的 `FP` 改为 `FPC`，然后重载 ARM64 注册并运行验收（无需重启 Docker）：

```bash
sudo sh -c 'printf "%s\n" -1 > /proc/sys/fs/binfmt_misc/qemu-aarch64'
sudo /usr/lib/systemd/systemd-binfmt /etc/binfmt.d/qemu-aarch64-static.conf
docker run --rm --platform linux/arm64 --network none --entrypoint bash oryn-orb:arch-arm64 /usr/local/lib/oryn/smoke-test.sh
```

验收通过时输出 `orb 镜像离线冒烟：通过`。
