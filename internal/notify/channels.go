package notify

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/webpush"
)

const (
	pushTTL        = 86400
	maxPushPayload = 3000
	maxPushBody    = 300
)

// VapidKey 返回 VAPID 公钥（applicationServerKey）。
func (s *Service) VapidKey() string { return s.keys.PublicKey() }

// vapidSubject 是 VAPID 的 sub：public_url 为 https 时用它，否则用占位邮箱。
func (s *Service) vapidSubject() string {
	if strings.HasPrefix(s.cfg.PublicURL, "https://") {
		return strings.TrimRight(s.cfg.PublicURL, "/")
	}
	return "mailto:admin@oryn.invalid"
}

type webpushSender struct {
	s   *Service
	sub webpush.Subscription
}

func (s *Service) webpushSender(sub db.PushSubscription) Sender {
	return webpushSender{s, webpush.Subscription{Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: sub.Auth}}
}

// pushPayload 是 service worker 收到的 JSON（≤3000 字节：正文截到 300 字符、标题截到 200 字符，仍超出时去掉正文）。
func pushPayload(m Message) ([]byte, error) {
	tag := m.ThreadID
	if len(tag) > 12 {
		tag = tag[len(tag)-12:]
	}
	v := map[string]any{
		"v": 1, "id": m.NotificationID, "kind": m.Kind, "title": truncate(m.Title, 200), "body": truncate(m.Body, maxPushBody),
		"url": "/t/" + m.ThreadID, "tag": "oryn-" + tag,
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) <= maxPushPayload {
		return b, err
	}
	v["body"] = ""
	return json.Marshal(v)
}

func utf8Len(s string) int { return len([]rune(s)) }

func (w webpushSender) Send(ctx context.Context, m Message) Result {
	payload, err := pushPayload(m)
	if err != nil {
		return Result{Err: err}
	}
	sum := sha256.Sum256([]byte(m.ThreadID))
	urgency := "normal"
	if m.Kind == KindInput {
		urgency = "high"
	}
	status, err := w.s.keys.Send(ctx, w.s.cfg.HTTP, w.sub, payload, webpush.Options{
		TTL: pushTTL, Urgency: urgency, Topic: base64.RawURLEncoding.EncodeToString(sum[:])[:32], Subject: w.s.vapidSubject(),
	})
	switch {
	case err != nil:
		// 订阅密钥已在创建时校验：这里的错误是请求没有得到响应（网络 / 超时）
		return netResult(err)
	case status == http.StatusOK || status == http.StatusCreated || status == http.StatusAccepted:
		return Result{}
	case status == http.StatusNotFound || status == http.StatusGone:
		return Result{Gone: true, Err: fmt.Errorf("subscription gone (HTTP %d)", status)}
	}
	return httpResult(&http.Response{StatusCode: status, Header: http.Header{}}, nil)
}

// Owner 是推送订阅的归属主体（网页会话或设备），二者至多一个非空。
type Owner struct {
	WebSessionHash, DeviceID *string
}

// errPushInvalid 构造 400 push_invalid。
func errPushInvalid(msg string) error {
	return &Error{Status: http.StatusBadRequest, Code: "push_invalid", Message: msg}
}

// CreatePushSubscription 登记推送订阅：`kind: fcm` 见 createFCMSubscription；其余按 endpoint upsert 一条 Web Push 订阅。
// 端点须是 https（回环主机允许 http）；p256dh 须是 65 字节未压缩点，auth 须是 16 字节。
func (s *Service) CreatePushSubscription(ctx context.Context, in apigen.PushSubscriptionInput, owner Owner, userAgent string) (apigen.PushSubscription, error) {
	if in.Kind != nil && *in.Kind == apigen.PushSubscriptionInputKindFcm {
		return s.createFCMSubscription(ctx, in, owner, userAgent)
	}
	if in.Kind != nil && !in.Kind.Valid() {
		return apigen.PushSubscription{}, errPushInvalid("unknown kind")
	}
	if in.Token != nil {
		return apigen.PushSubscription{}, errPushInvalid("token is only valid with kind fcm")
	}
	if in.Endpoint == nil || in.Keys == nil {
		return apigen.PushSubscription{}, errPushInvalid("endpoint and keys are required")
	}
	u, err := url.Parse(*in.Endpoint)
	if err != nil || u.Host == "" {
		return apigen.PushSubscription{}, errPushInvalid("endpoint must be an absolute URL")
	}
	switch u.Scheme {
	case "https":
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return apigen.PushSubscription{}, errPushInvalid("endpoint must use https")
		}
	default:
		return apigen.PushSubscription{}, errPushInvalid("endpoint must use https")
	}
	p, err := webpush.DecodeKey(in.Keys.P256Dh)
	if err != nil || len(p) != 65 || p[0] != 0x04 {
		return apigen.PushSubscription{}, errPushInvalid("keys.p256dh must be a 65-byte uncompressed P-256 point")
	}
	a, err := webpush.DecodeKey(in.Keys.Auth)
	if err != nil || len(a) != 16 {
		return apigen.PushSubscription{}, errPushInvalid("keys.auth must be 16 bytes")
	}
	enc := base64.RawURLEncoding.EncodeToString
	row, err := s.st.Q.UpsertPushSubscription(ctx, db.UpsertPushSubscriptionParams{
		ID: ids.New("psub"), Kind: "webpush", Endpoint: *in.Endpoint, P256dh: enc(p), Auth: enc(a),
		WebSessionHash: owner.WebSessionHash, DeviceID: owner.DeviceID, UserAgent: truncate(userAgent, 300), CreatedAt: s.now(),
	})
	if err != nil {
		return apigen.PushSubscription{}, err
	}
	return subscriptionAPI(row), nil
}

// ListPushSubscriptions 列出全部推送订阅。
func (s *Service) ListPushSubscriptions(ctx context.Context) ([]apigen.PushSubscription, error) {
	rows, err := s.st.Q.ListPushSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.PushSubscription, 0, len(rows))
	for _, r := range rows {
		out = append(out, subscriptionAPI(r))
	}
	return out, nil
}

// DeletePushSubscription 删除推送订阅。
func (s *Service) DeletePushSubscription(ctx context.Context, id string) error {
	n, err := s.st.Q.DeletePushSubscription(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func subscriptionAPI(r db.PushSubscription) apigen.PushSubscription {
	return apigen.PushSubscription{ID: r.ID, Kind: apigen.PushSubscriptionKind(r.Kind), Endpoint: r.Endpoint, UserAgent: r.UserAgent,
		CreatedAt: r.CreatedAt, LastSuccessAt: r.LastSuccessAt, LastError: r.LastError}
}

// ---- 渠道 CRUD ----

var allEvents = []apigen.NotificationKind{apigen.NotificationKindDone, apigen.NotificationKindInput, apigen.NotificationKindError}

func normalizeEvents(in []apigen.NotificationKind) (string, error) {
	if len(in) == 0 {
		in = allEvents
	}
	seen := map[apigen.NotificationKind]bool{}
	out := make([]string, 0, 3)
	for _, e := range allEvents {
		for _, x := range in {
			if !x.Valid() {
				return "", errInvalidChannel("unknown event %q", x)
			}
			if x == e && !seen[e] {
				seen[e] = true
				out = append(out, string(e))
			}
		}
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func normalizeLocale(l *apigen.NotificationLocale, def string) (string, error) {
	if l == nil {
		return def, nil
	}
	if !l.Valid() {
		return "", errInvalidChannel("unknown locale %q", *l)
	}
	return string(*l), nil
}

// configObjects 是请求里与各 kind 同名的配置对象。
type configObjects struct {
	Telegram *apigen.TelegramChannelConfig
	Webhook  *apigen.WebhookChannelConfig
	Ntfy     *apigen.NtfyChannelConfig
	Bark     *apigen.BarkChannelConfig
}

// pick 返回与 kind 对应的配置对象是否给出；给了别的 kind 的对象即报错。
func (c configObjects) pick(kind string) (bool, error) {
	given := map[string]bool{"telegram": c.Telegram != nil, "webhook": c.Webhook != nil, "ntfy": c.Ntfy != nil, "bark": c.Bark != nil}
	for k, ok := range given {
		if ok && k != kind {
			return false, errInvalidChannel("config object %q does not match channel kind %q", k, kind)
		}
	}
	return given[kind], nil
}

// buildConfig 校验并规范化 kind 的配置对象，返回明文配置 JSON；telegram 另返回 getMe 得到的 bot 用户名。
func (s *Service) buildConfig(ctx context.Context, kind string, c configObjects) ([]byte, string, error) {
	var v any
	var bot string
	switch kind {
	case "webhook":
		cfg, err := validWebhook(c.Webhook)
		if err != nil {
			return nil, "", err
		}
		v = cfg
	case "ntfy":
		cfg, err := validNtfy(c.Ntfy)
		if err != nil {
			return nil, "", err
		}
		v = cfg
	case "bark":
		cfg, err := validBark(c.Bark)
		if err != nil {
			return nil, "", err
		}
		v = cfg
	case "telegram":
		cfg, err := validTelegram(c.Telegram)
		if err != nil {
			return nil, "", err
		}
		bot, err = s.telegramGetMe(ctx, cfg)
		if err != nil {
			return nil, "", err
		}
		v = cfg
	default:
		return nil, "", errInvalidChannel("unknown channel kind %q", kind)
	}
	b, err := json.Marshal(v)
	return b, bot, err
}

func configName(id string) string { return "notify/" + id }

func (s *Service) openConfig(ch db.NotificationChannel) ([]byte, error) {
	return s.cfg.Box.Open(configName(ch.ID), ch.ConfigEnc)
}

// CreateChannel 创建通知渠道。
func (s *Service) CreateChannel(ctx context.Context, in apigen.NotificationChannelInput) (apigen.NotificationChannel, error) {
	kind := string(in.Kind)
	if !in.Kind.Valid() {
		return apigen.NotificationChannel{}, errInvalidChannel("unknown channel kind %q", in.Kind)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8Len(name) > 100 {
		return apigen.NotificationChannel{}, errInvalidChannel("name is required (≤100 characters)")
	}
	objs := configObjects{in.Telegram, in.Webhook, in.Ntfy, in.Bark}
	if ok, err := objs.pick(kind); err != nil {
		return apigen.NotificationChannel{}, err
	} else if !ok {
		return apigen.NotificationChannel{}, errInvalidChannel("missing %q config object", kind)
	}
	events, err := normalizeEvents(in.Events)
	if err != nil {
		return apigen.NotificationChannel{}, err
	}
	locale, err := normalizeLocale(in.Locale, defaultLocale)
	if err != nil {
		return apigen.NotificationChannel{}, err
	}
	if kind == "telegram" {
		n, err := s.st.Q.CountNotificationChannelsByKind(ctx, kind)
		if err != nil {
			return apigen.NotificationChannel{}, err
		}
		if n > 0 {
			return apigen.NotificationChannel{}, &Error{Status: http.StatusConflict, Code: "channel_exists", Message: "a Telegram channel already exists"}
		}
	}
	cfg, bot, err := s.buildConfig(ctx, kind, objs)
	if err != nil {
		return apigen.NotificationChannel{}, err
	}
	id := ids.New("nch")
	sealed, err := s.cfg.Box.Seal(configName(id), cfg)
	if err != nil {
		return apigen.NotificationChannel{}, err
	}
	state := "{}"
	if kind == "telegram" {
		b, _ := json.Marshal(tgState{BotUsername: bot})
		state = string(b)
	}
	enabled := in.Enabled == nil || *in.Enabled
	now := s.now()
	if err := s.st.Q.InsertNotificationChannel(ctx, db.InsertNotificationChannelParams{
		ID: id, Kind: kind, Name: name, Enabled: enabled, Events: events, Locale: locale, ConfigEnc: sealed, State: state, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return apigen.NotificationChannel{}, err
	}
	if kind == "telegram" {
		s.reloadTelegram(ctx)
	}
	return s.GetChannel(ctx, id)
}

// UpdateChannel 修改渠道；给了配置对象即整体替换（Telegram 换 token / apiBase 会清掉绑定）。
func (s *Service) UpdateChannel(ctx context.Context, id string, p apigen.NotificationChannelPatch) (apigen.NotificationChannel, error) {
	ch, err := s.getChannelRow(ctx, id)
	if err != nil {
		return apigen.NotificationChannel{}, err
	}
	upd := db.UpdateNotificationChannelParams{Name: ch.Name, Enabled: ch.Enabled, Events: ch.Events, Locale: ch.Locale, ConfigEnc: ch.ConfigEnc, UpdatedAt: s.now(), ID: id}
	if p.Name != nil {
		name := strings.TrimSpace(*p.Name)
		if name == "" || utf8Len(name) > 100 {
			return apigen.NotificationChannel{}, errInvalidChannel("name is required (≤100 characters)")
		}
		upd.Name = name
	}
	if p.Enabled != nil {
		upd.Enabled = *p.Enabled
	}
	if p.Events != nil {
		if upd.Events, err = normalizeEvents(p.Events); err != nil {
			return apigen.NotificationChannel{}, err
		}
	}
	if upd.Locale, err = normalizeLocale(p.Locale, ch.Locale); err != nil {
		return apigen.NotificationChannel{}, err
	}
	objs := configObjects{p.Telegram, p.Webhook, p.Ntfy, p.Bark}
	replace, err := objs.pick(ch.Kind)
	if err != nil {
		return apigen.NotificationChannel{}, err
	}
	var newState *string
	if replace {
		cfg, bot, err := s.buildConfig(ctx, ch.Kind, objs)
		if err != nil {
			return apigen.NotificationChannel{}, err
		}
		if upd.ConfigEnc, err = s.cfg.Box.Seal(configName(id), cfg); err != nil {
			return apigen.NotificationChannel{}, err
		}
		if ch.Kind == "telegram" {
			old, _ := s.openConfig(ch)
			if string(old) != string(cfg) {
				// 换了 bot：旧的绑定、offset 与绑定码都作废
				b, _ := json.Marshal(tgState{BotUsername: bot})
				st := string(b)
				newState = &st
			}
		}
	}
	s.stateMu.Lock()
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.UpdateNotificationChannel(ctx, upd); err != nil {
			return err
		}
		if newState != nil {
			return q.SetNotificationChannelState(ctx, db.SetNotificationChannelStateParams{State: *newState, ID: id})
		}
		return nil
	})
	s.stateMu.Unlock()
	if err != nil {
		return apigen.NotificationChannel{}, err
	}
	if ch.Kind == "telegram" {
		s.reloadTelegram(ctx)
	}
	return s.GetChannel(ctx, id)
}

// DeleteChannel 删除渠道（其投递记录随外键级联删除）。
func (s *Service) DeleteChannel(ctx context.Context, id string) error {
	n, err := s.st.Q.DeleteNotificationChannel(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	s.reloadTelegram(ctx)
	return nil
}

// ListChannels 列出全部渠道（机密字段不输出）。
func (s *Service) ListChannels(ctx context.Context) ([]apigen.NotificationChannel, error) {
	rows, err := s.st.Q.ListNotificationChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.NotificationChannel, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.channelAPI(ctx, r))
	}
	return out, nil
}

// GetChannel 返回一个渠道。
func (s *Service) GetChannel(ctx context.Context, id string) (apigen.NotificationChannel, error) {
	ch, err := s.getChannelRow(ctx, id)
	if err != nil {
		return apigen.NotificationChannel{}, err
	}
	return s.channelAPI(ctx, ch), nil
}

func (s *Service) getChannelRow(ctx context.Context, id string) (db.NotificationChannel, error) {
	ch, err := s.st.Q.GetNotificationChannel(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.NotificationChannel{}, ErrNotFound
	}
	return ch, err
}

// TestChannel 同步发一条测试消息并更新渠道的 last_error；不写 notifications 表。
func (s *Service) TestChannel(ctx context.Context, id string) (apigen.NotificationTestResult, error) {
	ch, err := s.getChannelRow(ctx, id)
	if err != nil {
		return apigen.NotificationTestResult{}, err
	}
	if ch.Kind == "telegram" && parseTgState(ch.State).ChatID == 0 {
		return apigen.NotificationTestResult{}, &Error{Status: http.StatusConflict, Code: "channel_not_linked", Message: "the Telegram channel is not linked to a chat yet"}
	}
	sender, err := s.channelSender(ch)
	if err != nil {
		return apigen.NotificationTestResult{}, err
	}
	r := sender.Send(ctx, Message{Kind: KindTest, Title: text(ch.Locale, "testTitle"), URL: strings.TrimRight(s.cfg.PublicURL, "/"), Locale: ch.Locale, CreatedAt: s.now()})
	if r.Err != nil {
		msg := errorText(r.Err)
		_ = s.st.Q.SetNotificationChannelError(ctx, db.SetNotificationChannelErrorParams{LastError: &msg, ID: id})
		return apigen.NotificationTestResult{Ok: false, Error: &msg}, nil
	}
	_ = s.st.Q.SetNotificationChannelError(ctx, db.SetNotificationChannelErrorParams{ID: id})
	return apigen.NotificationTestResult{Ok: true}, nil
}

// channelAPI 把渠道行转换成 API 形态：只输出脱敏的 summary 与非机密状态。
func (s *Service) channelAPI(ctx context.Context, ch db.NotificationChannel) apigen.NotificationChannel {
	var events []apigen.NotificationKind
	_ = json.Unmarshal([]byte(ch.Events), &events)
	if events == nil {
		events = []apigen.NotificationKind{}
	}
	out := apigen.NotificationChannel{
		ID: ch.ID, Kind: apigen.NotificationChannelKind(ch.Kind), Name: ch.Name, Enabled: ch.Enabled, Events: events,
		Locale: apigen.NotificationLocale(ch.Locale), CreatedAt: ch.CreatedAt, UpdatedAt: ch.UpdatedAt, LastError: ch.LastError,
	}
	cfg, err := s.openConfig(ch)
	switch ch.Kind {
	case "telegram":
		st := parseTgState(ch.State)
		out.Summary = "@" + st.BotUsername
		t := &apigen.NotificationChannelTelegram{BotUsername: st.BotUsername, Linked: st.ChatID != 0}
		if st.ChatTitle != "" {
			t.ChatTitle = new(st.ChatTitle)
		}
		out.Telegram = t
	case "webhook":
		var c webhookConfig
		if err == nil && json.Unmarshal(cfg, &c) == nil {
			out.Summary = maskURL(c.URL)
		}
	case "ntfy":
		var c ntfyConfig
		if err == nil && json.Unmarshal(cfg, &c) == nil {
			out.Summary = hostOf(c.Server) + "/" + maskTail(c.Topic)
		}
	case "bark":
		var c barkConfig
		if err == nil && json.Unmarshal(cfg, &c) == nil {
			out.Summary = hostOf(c.Server)
		}
	}
	if d, err := s.st.Q.GetLastDeliveryByChannel(ctx, ch.ID); err == nil {
		at := d.CreatedAt
		switch {
		case d.FinishedAt != nil:
			at = *d.FinishedAt
		case d.NextAttemptAt != nil:
			at = *d.NextAttemptAt
		}
		out.LastDelivery = &apigen.NotificationChannelLastDelivery{Status: apigen.NotificationDeliveryStatus(d.Status), At: at, Error: d.LastError}
	}
	return out
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// maskTail 只保留末 4 个字符（值不足 9 个字符时全部隐去）。
func maskTail(v string) string {
	r := []rune(v)
	if len(r) < 9 {
		return "…"
	}
	return "…" + string(r[len(r)-4:])
}

// maskURL 输出 `<host>/…<末 4 位>`：路径与查询串里可能带着令牌。
func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	rest := strings.TrimPrefix(u.RequestURI(), "/")
	if rest == "" {
		return u.Host
	}
	return u.Host + "/" + maskTail(rest)
}
