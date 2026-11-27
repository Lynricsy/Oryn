# App Store 提交

iOS App（`com.lynricsy.oryn`）提交 App Store 审核时要填的内容：审核备注、「App 隐私」问卷、出口合规、隐私政策与支持地址，以及提交前检查。构建、签名与上传 TestFlight 见 [mobile.md 附录 A](mobile.md#附录-aios)；隐私清单见其中「隐私清单」一节。

## 背景：审核员需要知道的模型

- Oryn 是自托管软件，每台服务器只有一个所有者。所有者账号在自己的服务器上创建：浏览器打开服务器日志里的 setup 链接，注册 Passkey 并保存 10 个恢复码（见 [deploy.md](deploy.md)）。App 里不能注册账号。
- App 是 Tauri 壳：自带的连接页与用户自己的 orynd 配对，配对后加载该服务器的 Web UI。配对码要在已登录的浏览器里批准，App 里不能批准配对。
- 对话、文件、记忆与设置都在用户自己的服务器上，开发者收不到。开发者运营的服务只有官方推送网关 `https://push.oryn.best`（见 [push-gateway.md](push-gateway.md) 与 [protocol.md](protocol.md)「官方推送网关」）。
- 模型调用由服务器发出，设备从不直连 AI 服务商。服务器在所有者同意之前不把数据发给任何第三方 AI 服务：模型请求（含后台任务）、记忆的嵌入 / 重排 / 判断模型与助手联网都在发出前现查同意记录，没有同意的接收方一律拒绝（`ai_consent_required`），请求不发出。App 在第一次使用 AI 功能前列出接收数据的服务商并征得同意；服务器新增接收方（新的服务商或新的上游主机）时，发往它的请求在再次同意前被拒，App 随即再次征求。「设置 → 隐私与数据」可查看接收方并撤回同意，撤回后服务器经手的下一个请求即被拒，App 里不能使用 AI 功能（Guideline 5.1.2(i)）。本机与内网上的模型不算第三方；以环境变量降级下发到 orb 的模型密钥只在同意后下发，撤回要到 orb 下一次 setup 才收回（见 [deploy.md「AI 数据同意」](deploy.md#ai-数据同意app-store-512i)），演示服务器不要用这种配置。

## 审核用的演示服务器

审核员没有自己的服务器，提交前准备一台专供审核的 orynd，不要用日常实例：

1. 公网可达的 HTTPS 地址，证书由公共 CA 签发（App 只信任 Mozilla 根证书，私有 CA 不受支持）。
2. 服务器版本与提交的 App 版本一致，含 AI 同意闸门与服务端的同意执行。
3. 至少配置一个模型 Provider，并放几条演示线程，让审核员能走通一次对话。
4. `push.gateway.url` 保持缺省（官方网关），推送可用。
5. 准备 3 个未用过的恢复码写进审核备注。恢复码每个只能用一次；用恢复码登录后要为审核设备注册一个新的 Passkey，之后才是完整的网页会话、能批准配对（恢复会话本身只能重新注册 Passkey，15 分钟有效）。
6. 审核期间不要重新生成恢复码，不要吊销审核设备。

审核结束后，在「设置 → 账户与设备」删除审核员注册的 Passkey、重新生成恢复码，并吊销审核设备。

## 审核备注模板

App Store Connect → App 版本 →「App 审核信息」：「需要登录」勾选，用户名填演示服务器地址，密码填第一个恢复码；「备注」填下面的英文模板，把 `<…>` 换成实际值。

```text
Oryn is a self-hosted agent platform. Each Oryn server belongs to a single
owner, who installs it on their own machine. This app does not create or host
accounts: the owner account is created once in a web browser on the user's own
server, using a setup link printed by that server. There is no sign-up in the
app.

The app is a client for the user's own server. It pairs with one server,
then loads that server's web interface. Conversations, files, memory and
settings are stored on the user's server, not by us.

Demo server for review: <演示服务器地址，如 https://review.example.com>
Recovery codes (each works once): <恢复码 1>, <恢复码 2>, <恢复码 3>

Steps:
1. Open the app, enter the demo server address and tap "Connect".
   The app shows an 8-character pairing code.
2. Tap "Copy code and open browser". In Safari, choose "Use a recovery code",
   enter one recovery code and tap "Recover".
3. When asked, register a new passkey for this device (iCloud Keychain is
   fine). You are now signed in.
4. Paste the pairing code, check the device details and tap "Approve",
   then tap "Back to the Oryn app". The app opens the server's interface.
5. Open a demo thread and send a message. Before the first AI request the app
   lists the AI providers configured on the server and asks for consent.
   Consent can be reviewed and withdrawn under Settings > Privacy & data.

Data handling:
- AI requests are sent by the user's server to the model providers its owner
  configured, never from the device. The server itself refuses to send any
  data to a third-party AI provider until consent is given in the app, and
  stops relaying requests to it as soon as consent is withdrawn
  (Guideline 5.1.2(i)).
- The only service we operate is the push gateway (push.oryn.best). It stores
  an encrypted APNs device token and public keys, never notification content:
  notifications are end-to-end encrypted and decrypted on device by a
  Notification Service Extension.
- Account deletion (Guideline 5.1.1(v)): accounts are not created in the app.
  All account data lives on the user's own server, and the user can delete it
  there at any time. Unpairing in the app (Settings > About > Change server…)
  or revoking the device under Settings > Account & devices removes the device and
  revokes its push grant on our gateway.

Privacy policy: https://docs.oryn.best/privacy/en/
Contact: <联系人姓名>, <联系邮箱或电话>
```

「联系信息」填所有者的姓名、电话与邮箱；审核员遇到演示服务器不可用时会联系这里。

## App 隐私（App Privacy）问卷

问卷答案与隐私清单（`web/src-tauri/ios/PrivacyInfo.xcprivacy`）保持一致。

| 问题 | 答案 |
|---|---|
| 是否从此 App 收集数据 | 是 |
| 数据类型 | 标识符 → 设备 ID（Device ID） |
| 用途 | App 功能（App Functionality） |
| 是否与用户身份关联 | 否 |
| 是否用于追踪 | 否 |

依据：

- 开发者收集的只有官方推送网关上的推送授权：APNs 设备令牌（AES-256-GCM 加密存储）、设备公钥、服务器的主机公钥、通道、App 标识、环境与时间戳。网关不存服务器地址与通知内容，没有账号，不能关联到具体的人；授权 90 天未用或吊销 7 天后删除。来源 IP 只在内存里限流、不落盘，不算收集。
- 对话、附件、照片、文件与记忆发往用户自己的服务器，开发者无法访问，不算开发者收集，问卷里不勾「用户内容」。服务器把内容转给所有者配置的 AI 服务商，由 App 内的同意提示（Guideline 5.1.2(i)）与隐私政策披露。
- 没有分析、崩溃上报、广告或追踪 SDK，不追踪。

以后 App 开始向开发者运营的服务发送新的数据时，问卷、隐私清单与隐私政策一起更新。

## 出口合规

与 [mobile.md 附录 A](mobile.md#附录-aios) 一致：`ITSAppUsesNonExemptEncryption` 没有写进 Info.plist，每个构建上传后在 App Store Connect 回答（构建显示「缺少合规证明」时）。

作答依据：App 使用加密；所用的都是国际标准算法，没有自有或非标准算法；除了 Apple 系统提供的加密，Rust 网络栈用 rustls 自带的 TLS 实现，推送的端到端加密按 RFC 8291 实现。按这些事实，算法类型选「标准加密算法，而不是或除了使用 Apple 操作系统内的加密」，其后关于上架地区（如法国）的问题按问卷提示作答。这里只整理工程事实，不是法律意见，最终答案由发布者决定。

## 地址

| App Store Connect 字段 | 值 |
|---|---|
| 隐私政策网址 | `https://docs.oryn.best/privacy/`（中文）；英文店面用 `https://docs.oryn.best/privacy/en/` |
| 支持网址 | `https://github.com/Lynricsy/Oryn/issues` |

隐私政策页的源文件在文档站仓库 OrynDocSite 的 `src/content/docs/privacy/`，联系邮箱 `privacy@ling.plus`。App 的数据处理方式变化时同步更新两种语言并改生效日期。

## 提交前检查

- [ ] IPA 里有 `Oryn.app/PrivacyInfo.xcprivacy` 与 `Oryn.app/PlugIns/NotificationService.appex/PrivacyInfo.xcprivacy`（见 mobile.md「隐私清单」）。
- [ ] 演示服务器的版本含 AI 同意闸门：第一次使用 AI 前弹出列出服务商的同意提示，「设置 → 隐私与数据」能看到接收方并撤回；撤回后发消息，回复是 `ai_consent_required` 的拒绝，重新同意后恢复。
- [ ] 演示服务器的模型 Provider 写了 `baseUrl` 或在内置表里（anthropic、openai、google、deepseek、openrouter），没有以环境变量降级的 provider（它的撤回要到 orb 下一次 setup 才生效）。
- [ ] 演示服务器公网可达、证书有效，配好模型 Provider，审核备注里的恢复码未用过；按「审核备注模板」的步骤用一台没配对过的设备走一遍（走完后重新准备恢复码）。
- [ ] `https://docs.oryn.best/privacy/` 与 `https://docs.oryn.best/privacy/en/` 能打开，内容与当前版本一致。
- [ ] 「App 隐私」问卷按上表填好，与隐私清单一致。
- [ ] 当前构建的出口合规已作答。
- [ ] 审核备注、联系信息、隐私政策网址与支持网址已填。
