# 代部署手册（给 AI agent）

本文写给替用户部署 Oryn 的 AI agent。读完后你能在一台 Linux 服务器上装好控制面 `orynd` 与本机 Docker provider，把一个能用的 Oryn 交给用户。全程只问用户必要的信息，其余自己探测。用户只需要亲手做一件事：在浏览器里注册管理员 Passkey。

本文只覆盖单机最小部署：一个域名、HTTPS、本机 Docker 跑 orb。网络隔离、Sysbox、Firecracker、Office 引擎、Hindsight、GitHub 集成等可选组件见 [deploy.md](deploy.md)，用户明确要求时再做。

## 0. 硬性规则

- **不要向用户要模型 API Key、GitHub 令牌等密钥。** 用户登录后在网页里配置（第 9 节的交付话术里告诉他在哪）。不要把 setup 链接、设备令牌、provider 令牌写进日志、提交或 issue。
- **控制面端口 7600 不得对公网开放。** `orynd` 只监听回环与 docker0 网桥地址；公网入口只有反代的 80 / 443。
- **不覆盖已有数据。** `/var/lib/oryn` 或 `/etc/oryn` 已存在时停下，按第 1 节的规则问用户。
- **不碰与 Oryn 无关的东西。** 不改 Docker 全局配置（`/etc/docker/daemon.json`），不执行 `docker system prune`，不停用户已有的容器与站点，不改已有反代里别的站点。
- **不给 orb 加 `--privileged`，也不把宿主 `docker.sock` 挂进 orb**（等于把宿主 root 交给 orb 里的 agent）。
- 每一步都按「验证」一栏确认通过后才进行下一步。验证失败先看第 10 节，找不到原因再告诉用户，并附上命令与原始输出。

## 1. 先自行探测（不问用户）

在目标机上执行，记下结果。下文用 `$ARCH`（`x64` 或 `arm64`）、`$DOCKER_IP`、`$VERSION`（tag 去掉 `v`）指代探测值。

| 项 | 命令 | 要求 / 处理 |
|---|---|---|
| 系统与架构 | `uname -m`；`cat /etc/os-release` | `x86_64` → `ARCH=x64`，`aarch64` → `ARCH=arm64`；其它架构停下告诉用户不支持。需要 systemd。 |
| 权限 | `id -u`；`sudo -n true` | 需要 root 或免密 sudo。没有就请用户提供。 |
| 已有安装 | `ls /etc/oryn /var/lib/oryn`；`systemctl status orynd` | 都不存在才继续。存在时问用户：升级（按 deploy.md 操作，不在本文范围）还是放弃。**不要删除。** |
| Docker | `docker info --format '{{.ServerVersion}} {{.DockerRootDir}}'` | 没装就用发行版的包或 Docker 官方源装 Docker Engine，再 `systemctl enable --now docker`。 |
| 磁盘 | `df -h <DockerRootDir> /var/lib` | Docker 数据盘至少 40 GB 可用（镜像约 10 GB，每台 orb 还会增长）。不足时停下告诉用户。 |
| 内存 | `free -g` | ≥ 16 GB 时默认 orb 类型用 `medium`（4 核 8 GB），否则用 `small`（2 核 4 GB）。 |
| git | `git --version` | 需要 ≥ 2.31（项目仓库镜像用）；没有就装。 |
| 80 / 443 | `ss -ltnp '( sport = :80 or sport = :443 )'` | 决定第 6 节的反代做法：空闲 → 装 Caddy；已被 nginx / OpenResty / Caddy 占用 → 并入现有反代；被认不出的程序占用 → 停下问用户。 |
| 公网 IP | `curl -fsS https://api.ipify.org`（取不到时用 `ip -4 addr`） | 用于第 2 节核对 DNS。 |
| 版本 | `curl -fsSL 'https://api.github.com/repos/Lynricsy/Oryn/releases?per_page=1' \| jq -r '.[0].tag_name'` | 得到 `v<版本>`，`VERSION` 是去掉 `v` 的部分。用户指定了版本就用用户的。 |

## 2. 问用户（一次问完）

只问下面两项，在一条消息里问。能给默认值的都给默认值。

1. **访问 Oryn 的域名**，例如 `oryn.example.com`。必填，因为 Passkey 登录和桌面 / 移动 App 都要求 HTTPS。同时告诉用户：这个域名要有一条 A（或 AAAA）记录指向本机公网 IP（填上你探测到的 IP）。
   - 用户没有域名时，提供一个选项：「先在本机试用」。这种模式不装反代，用户经 SSH 隧道在自己电脑上打开 `http://localhost:7600`；桌面与移动 App 不可用，以后可以换成正式域名。
2. **是否需要 Portal**（从浏览器访问 orb 里跑的网页服务，以及 Boxes 应用的外部地址）。默认：**暂不需要**。需要时还得加一条泛域名 DNS 记录和一张通配证书（DNS-01），按 [deploy.md「Portal」](deploy.md#portal受管服务的浏览器入口)做。

拿到域名后自己核对：`dig +short <域名>`（或 `getent hosts <域名>`）的结果包含本机公网 IP。不一致就告诉用户该加什么记录，等他确认后再继续；不要在 DNS 没生效时申请证书，否则会撞上 ACME 的频率限制。

只有遇到下面几种情况才额外提问：第 1 节标注了「停下」的项；80 / 443 被认不出的程序占用；现有反代由面板（1Panel、宝塔等）管理，而且你没法在面板之外申请证书。

## 3. 下载并安装二进制与镜像

```bash
VERSION=<版本>; ARCH=<x64|arm64>
cd "$(mktemp -d)"
base="https://github.com/Lynricsy/Oryn/releases/download/v${VERSION}"
curl -fLO "${base}/oryn-server_${VERSION}_linux-${ARCH}.tar.gz"
curl -fLO "${base}/oryn-server_${VERSION}_SHA256SUMS"
sha256sum -c --ignore-missing "oryn-server_${VERSION}_SHA256SUMS"
tar -xzf "oryn-server_${VERSION}_linux-${ARCH}.tar.gz"
pkg="oryn-server_${VERSION}_linux-${ARCH}"
sudo install -m 0755 "$pkg/orynd" "$pkg/oryn" /usr/local/bin/
sudo install -D -m 0755 "$pkg/omp" /usr/local/lib/oryn/omp

# orb 镜像约 10 GB，下载慢：放到后台，第 7 节之前确认完成
sudo docker pull "ghcr.io/lynricsy/oryn-orb:${VERSION}" > /tmp/oryn-image-pull.log 2>&1 &
```

部署文件（systemd 单元、反代示例）不在包里，从同一个 tag 取：

```bash
raw="https://raw.githubusercontent.com/Lynricsy/Oryn/v${VERSION}"
curl -fLO "${raw}/deploy/systemd/orynd.service"
curl -fLO "${raw}/deploy/systemd/oryn-provider.service"
curl -fLO "${raw}/deploy/caddy/Caddyfile"
curl -fLO "${raw}/deploy/openresty/oryn.conf"
```

**验证**：`sha256sum -c` 输出 `OK`；`orynd --version` 输出 `v<版本>`；包里有 `omp`。

包里没有 `omp`，或 `docker manifest inspect ghcr.io/lynricsy/oryn-orb:${VERSION}` 失败，说明这个版本早于本文档引入的发布方式。换成最新版本；用户坚持要这个版本时，按 [deploy.md 第 1 节](deploy.md#1-安装二进制与镜像)的「从源码构建」做。

## 4. 控制面配置

```bash
sudo useradd --system --home /var/lib/oryn --shell /usr/sbin/nologin oryn
sudo install -d -o oryn -g oryn -m 0700 /var/lib/oryn
sudo install -d -m 0755 /etc/oryn
DOCKER_IP=$(ip -4 -o addr show docker0 | awk '{print $4}' | cut -d/ -f1)
```

写 `/etc/oryn/orynd.yaml`（`<域名>` 换成用户给的域名）：

```yaml
listen: ["127.0.0.1:7600", "<DOCKER_IP>:7600"]   # 第二项让本机 orb 经 docker0 直连控制面
public_url: "https://<域名>"
data_dir: "/var/lib/oryn"
```

- 「先在本机试用」模式：`public_url` 写 `"http://localhost:7600"`，另加一行 `portal_domain: "localhost"`。
- 用户要 Portal：另加 `portal_domain: "<泛域名的父域>"`，规则见 deploy.md「Portal」。

其余配置项都有缺省值，不要写。

## 5. 启动控制面

```bash
sudo install -m 0644 orynd.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now orynd
```

**验证**：`curl -fsS http://127.0.0.1:7600/healthz` 返回 `{"ok":true,"version":"v<版本>"}`。失败时看 `journalctl -u orynd -n 50`。

## 6. 反代与 HTTPS

「先在本机试用」模式跳过本节。

- **80 / 443 空闲**：用发行版的包装 Caddy（Debian / Ubuntu 用 Caddy 官方 apt 源），把下载的 `Caddyfile` 里的 `oryn.example.com` 换成用户的域名，放到 `/etc/caddy/Caddyfile`，然后 `systemctl reload caddy`。Caddy 会自己申请证书。
- **已有 nginx / OpenResty**：把 `oryn.conf` 的主域名 server 块并进去，换掉域名与证书路径，删掉泛域名的 server 块（不做 Portal 时）。证书用机器上已有的 ACME 工具签发（certbot、acme.sh 或面板）。改完先 `nginx -t` 再 reload。
- **已有 Caddy**：把 `Caddyfile` 里的站点块追加到现有配置里。
- **其它反代**：按 [deploy.md 第 3 节](deploy.md#3-反代1panel-openresty)「配置要点」逐条翻译：WebSocket 升级、关闭缓冲、长超时、`/git/` 不限请求体、传 `X-Real-IP`、`/wopi/` 不记访问日志。

防火墙只需放行 80 与 443。

**验证**：`curl -fsS https://<域名>/healthz` 返回与第 5 节相同的 JSON，且证书有效（不加 `-k`）。

## 7. 本机 Docker provider

先给自己拿一个 CLI 设备令牌。用户还没注册 Passkey 也能做：

```bash
TOKEN=$(sudo -u oryn orynd --config /etc/oryn/orynd.yaml admin create-device --name deploy-agent --kind cli)
oryn login --url https://<域名> --token "$TOKEN"      # 本机试用模式用 http://127.0.0.1:7600
oryn provider create local | sudo install -m 0600 /dev/stdin /etc/oryn/provider.token
```

写 `/etc/oryn/provider.yaml`。`default_type` 按第 1 节的内存规则取 `medium` 或 `small`，并删掉超过本机内存的类型：

```yaml
url: "http://127.0.0.1:7600"
token_file: "/etc/oryn/provider.token"
orb_url: "http://<DOCKER_IP>:7600"
driver: docker
docker:
  image: "ghcr.io/lynricsy/oryn-orb:<版本>"
  types: { small: {cpus: 2, memory: "4g"}, medium: {cpus: 4, memory: "8g"}, large: {cpus: 8, memory: "16g"} }
  default_type: medium
```

确认第 3 节的镜像下载已完成（`docker image inspect ghcr.io/lynricsy/oryn-orb:<版本>` 成功），然后：

```bash
sudo install -m 0644 oryn-provider.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now oryn-provider
```

**验证**：`oryn provider ls` 里 `local` 的 STATUS 为 `online`，DRIVER 为 `docker`。

## 8. 端到端自检

开一个线程，看 orb 能不能起来。模型还没配置也没关系，这一步只看 orb 的状态：

```bash
oryn thread new --no-project --title 部署自检 --json "ping" | jq -r .id     # 记下线程 id
oryn orb ls                                                                 # 等 STATE 变成 ready，冷启动通常不到 1 分钟
oryn thread archive <线程 id>
oryn orb rm <orb id>
```

再查协调宿主（Space 与全局助手用）：

```bash
curl -fsS -H "Authorization: Bearer $TOKEN" https://<域名>/api/v1/coordinator
```

返回 `"available":true` 即可。为 `false` 时把 `reason` 原文记下来，在交付时告诉用户；它不影响普通线程，内核条件见 [deploy.md「启动条件」](deploy.md#启动条件)。

**验证**：orb 到过 `ready`，删除后 `oryn orb ls` 为空。orb 停在 `failed` 时看第 10 节。

最后删掉自己的 CLI 凭据：

```bash
rm -f ~/.config/oryn/credentials.json
```

## 9. 交付给用户

生成一次性的初始化链接：

```bash
sudo -u oryn orynd --config /etc/oryn/orynd.yaml setup-token
```

把下面这段发给用户（尖括号换成实际值）：

> Oryn 已经部署好了：`https://<域名>`
>
> 1. 用电脑或手机的浏览器打开这个一次性链接，注册管理员 Passkey：`<setup 链接>`。页面会显示 10 个恢复码，只显示这一次，请存到密码管理器里。
> 2. 配置模型：「设置 → 助手与 Agent → 配置文件」编辑 `models.yml`；API Key 等密钥在「设置 → 工作区 → 密钥」。
> 3. 在「设置 → 账户与设备」里吊销名为 `deploy-agent` 的设备，那是部署时用的临时凭据。
>
> 需要备份的是 `/var/lib/oryn`（包括数据库、加密密钥库的主密钥 `master.key` 与托管仓库）以及 `/etc/oryn`。

「先在本机试用」模式下，第 1 条改成：先在自己电脑上执行 `ssh -N -L 7600:127.0.0.1:7600 <用户>@<服务器>`，再在本机浏览器打开链接（链接的主机部分是 `localhost:7600`）。

协调宿主不可用、用户要的 Portal 还没配置、改用过的默认值（如 orb 类型），都在这条消息里一并说明。

## 10. 常见故障

| 现象 | 原因与处理 |
|---|---|
| orynd 起不来，日志里有 `bind: cannot assign requested address` | docker0 地址写错或 Docker 还没起来。重新探测 `DOCKER_IP`，确认 `docker info` 正常后 `systemctl restart orynd`。 |
| orynd 拒绝启动，报配置项错误 | `orynd.yaml` 按严格模式解析，多余或拼错的键都会报错。只保留第 4 节列出的键。 |
| `https://<域名>/healthz` 证书错误或连不上 | DNS 未生效、80 端口被拦（Caddy 申请证书要用），或云厂商安全组没放行 80 / 443。`journalctl -u caddy` 里能看到 ACME 的具体错误。 |
| provider 一直 `offline` | 看 `journalctl -u oryn-provider -n 50`：令牌文件权限或内容错误，或 `url` 连不上 `127.0.0.1:7600`。 |
| orb 停在 `failed`，原因提到镜像 | 镜像没下载完或标签写错。`docker image inspect` 确认后重新开线程。 |
| orb 停在 `connecting` | orb 连不上 `orb_url`。确认 `orynd.yaml` 的 `listen` 包含 docker0 地址，主机防火墙没有拦 docker0 → 7600。 |
| `setup-token` 报已初始化 | 用户已经注册过 Passkey，不需要新链接；忘记登录方式时让用户用恢复码。 |
