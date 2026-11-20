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

## 界面截图

| 命令（`web/` 目录） | 输出 |
|---|---|
| `bun run shots:all` | 全界面矩阵（明暗 × 宽度 × 界面清单），写到 `/tmp/oryn-shots`，参数见 `scripts/screenshots.ts` 文件头 |
| `bun run shots:readme` | README 与本目录文档用的配图，写到 `docs/assets/` |

两者都起 `VITE_MOCK=1` 的 vite 开发服务器并用本机 Chromium 截图，不需要后端。

## 跨架构构建 ARM64 镜像

在 x64 宿主上执行 `scripts/build-orb-image.sh arm64` 构建 ARM64 镜像时，跨架构离线冒烟需要宿主的 QEMU `binfmt_misc` 注册开启 `C`（凭据）标志，否则容器内 `sudo` 会报告 effective uid 不为 0。

在装有 `qemu-user-static` 的 Arch Linux 宿主上，把 `/usr/lib/binfmt.d/qemu-aarch64-static.conf` 复制到 `/etc/binfmt.d/qemu-aarch64-static.conf`，只把规则末尾的 `FP` 改为 `FPC`，然后重载 ARM64 注册并运行验收（无需重启 Docker）：

```bash
sudo sh -c 'printf "%s\n" -1 > /proc/sys/fs/binfmt_misc/qemu-aarch64'
sudo /usr/lib/systemd/systemd-binfmt /etc/binfmt.d/qemu-aarch64-static.conf
docker run --rm --platform linux/arm64 --network none --entrypoint bash oryn-orb:arch-arm64 /usr/local/lib/oryn/smoke-test.sh
```

验收通过时输出 `orb 镜像离线冒烟：通过`。
