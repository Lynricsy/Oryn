# 部署 Oryn

单节点部署：`orynd`（控制面）只监听本机，放在现有反代（1Panel OpenResty）之后；同一台机器上的 `oryn provider run` 用 Docker 跑 orb。

## 1. 安装二进制与镜像

```bash
make web                        # 构建 Web 客户端，复制到 internal/webui/dist 以便嵌入
make build                      # 产出 bin/orynd bin/orbd bin/oryn（嵌入上一步的前端）
sudo install -m 0755 bin/orynd bin/oryn /usr/local/bin/
make image                      # 构建 orb 镜像 oryn-orb:dev（需要 build/core/x64/omp，见 scripts/fetch-core.sh）
```

创建服务用户与目录：

```bash
sudo useradd --system --home /var/lib/oryn --shell /usr/sbin/nologin oryn
sudo install -d -o oryn -g oryn -m 0700 /var/lib/oryn
sudo install -d -m 0755 /etc/oryn
```

## 2. 控制面配置 `/etc/oryn/orynd.yaml`

```yaml
listen: ["127.0.0.1:7600", "172.17.0.1:7600"]   # 第二项 = docker0 网桥地址，供本机 orb 直连
public_url: "https://oryn.example.com"          # WebAuthn RP ID 取其主机名，必须与浏览器访问地址一致
portal_domain: ""                               # M4 起使用，如 orb.example.com
data_dir: "/var/lib/oryn"
log_level: "info"
idle: { agent_idle: "5m", user_idle: "20m", stop_after_frozen: "24h", wake_on_focus: true }
orb_defaults: { provider: "", type: "" }
```

docker0 地址因机器而异，用 `ip -4 addr show docker0` 查看（例如有的机器是 `172.18.0.1`）。orb 不经过反代，直接连这个地址。

## 3. 反代（1Panel OpenResty）

1. 在 1Panel「网站」中创建两个反向代理站点，或直接把 `deploy/openresty/oryn.conf` 放进 OpenResty 的 `conf.d`，把示例域名替换为实际域名：
   - 主域名 `oryn.example.com` → `http://127.0.0.1:7600`
   - 泛域名 `*.orb.example.com` → `http://127.0.0.1:7600`（M4 Portal 启用后才用到，需透传 `Host`）
2. 证书：主域名普通证书；泛域名在 1Panel「证书」里用 DNS 账号申请 `*.orb.example.com` 通配证书（ACME DNS-01）。
3. 配置要点（示例文件已包含）：`proxy_http_version 1.1`、`Upgrade`/`Connection` 头（WebSocket 与 orb link）、`proxy_read_timeout 3600s`、`proxy_buffering off`、`client_max_body_size 600m`。

## 4. 启动控制面并初始化

```bash
sudo install -m 0644 deploy/systemd/orynd.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now orynd
journalctl -u orynd | grep setup?token=
```

首次启动没有所有者时，日志里打印 `https://oryn.example.com/setup?token=ost_…`。浏览器打开它注册 Passkey，并妥善保存显示一次的 10 个恢复码。链接丢失时运行 `sudo -u oryn orynd --config /etc/oryn/orynd.yaml setup-token` 重新生成。

## 5. 本机 Docker provider

```bash
oryn login --url https://oryn.example.com       # 在网页上批准设备配对
oryn provider create local | sudo tee /etc/oryn/provider.token >/dev/null
sudo chmod 0600 /etc/oryn/provider.token
```

`/etc/oryn/provider.yaml`：

```yaml
url: "http://127.0.0.1:7600"         # provider 与控制面同机，直连本地端口
token_file: "/etc/oryn/provider.token"
orb_url: "http://172.17.0.1:7600"    # 注入 orb 的 ORYN_URL = docker0 地址 + orynd 的第二个 listen
driver: docker
docker:
  image: "oryn-orb:dev"
  types: { small: {cpus: 2, memory: "4g"}, medium: {cpus: 4, memory: "8g"}, large: {cpus: 8, memory: "16g"} }
  default_type: medium
```

```bash
sudo install -m 0644 deploy/systemd/oryn-provider.service /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl enable --now oryn-provider
```

provider 放在另一台主机时，`url` 与 `orb_url` 都写主域名（`https://oryn.example.com`），orb 经反代建立 link。

## 6. omp 配置

```bash
oryn config push ~/.omp/agent
```

`models.yml` 里的字面 API key 会被移入加密密钥库（`data_dir/master.key`，首次启动生成，务必备份），下发到 orb 的只有占位令牌。
