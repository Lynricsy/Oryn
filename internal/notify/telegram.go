package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
	"github.com/Lynricsy/Oryn/internal/threads"
	"github.com/Lynricsy/Oryn/internal/uiprompt"
)

const (
	defaultTelegramAPI = "https://api.telegram.org"
	linkCodeAlphabet   = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	linkCodeTTL        = 10 * time.Minute
	pollWaitSeconds    = 50
	maxButtons         = 8
	maxButtonText      = 40
	maxTelegramText    = 3500
	pollBackoffMin     = 5 * time.Second
	pollBackoffMax     = 60 * time.Second
)

// tgConfig 是 Telegram 渠道的机密配置。
type tgConfig struct {
	BotToken string `json:"botToken"`
	APIBase  string `json:"apiBase"`
}

// tgState 是 Telegram 渠道的非机密运行状态（notification_channels.state）。
type tgState struct {
	BotUsername string `json:"botUsername"`
	ChatID      int64  `json:"chatId,omitempty"`
	ChatTitle   string `json:"chatTitle,omitempty"`
	Offset      int64  `json:"offset,omitempty"`
	LinkCode    string `json:"linkCode,omitempty"`
	LinkExpires int64  `json:"linkExpires,omitempty"`
}

func parseTgState(s string) tgState {
	var st tgState
	_ = json.Unmarshal([]byte(s), &st)
	return st
}

func validTelegram(in *apigen.TelegramChannelConfig) (tgConfig, error) {
	cfg := tgConfig{BotToken: strings.TrimSpace(in.BotToken), APIBase: defaultTelegramAPI}
	if cfg.BotToken == "" || strings.ContainsAny(cfg.BotToken, "/ ?#") {
		return tgConfig{}, errInvalidChannel("telegram.botToken is required")
	}
	if in.APIBase != nil && strings.TrimSpace(*in.APIBase) != "" {
		u, err := httpURL("telegram.apiBase", *in.APIBase)
		if err != nil {
			return tgConfig{}, err
		}
		cfg.APIBase = u
	}
	return cfg, nil
}

// ---- Bot API 调用 ----

type tgError struct {
	Status      int
	Description string
	RetryAfter  int
}

func (e *tgError) Error() string {
	return fmt.Sprintf("telegram: HTTP %d: %s", e.Status, e.Description)
}

// tgCall 调用 `POST <apiBase>/bot<token>/<method>`，把 result 解到 out（可为 nil）。
func tgCall(ctx context.Context, hc *http.Client, cfg tgConfig, method string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.APIBase+"/bot"+cfg.BotToken+"/"+method, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		// 错误信息里的 URL 含 token：只保留方法名与底层原因
		var ue interface{ Unwrap() error }
		if errors.As(err, &ue) && ue.Unwrap() != nil {
			return fmt.Errorf("telegram %s: %w", method, ue.Unwrap())
		}
		return fmt.Errorf("telegram %s: request failed", method)
	}
	defer func() { _ = resp.Body.Close() }()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(rb, &env); err != nil || !env.OK || resp.StatusCode != http.StatusOK {
		desc := env.Description
		if desc == "" {
			desc = truncate(strings.TrimSpace(string(rb)), 200)
		}
		return &tgError{Status: resp.StatusCode, Description: desc, RetryAfter: env.Parameters.RetryAfter}
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// tgResult 把一次 Bot API 调用的错误归类为投递结果。
func tgResult(err error) Result {
	var te *tgError
	if errors.As(err, &te) {
		r := Result{Err: te}
		if te.Status == http.StatusTooManyRequests || te.Status >= 500 {
			r.Retry = true
			r.RetryAfter = time.Duration(te.RetryAfter) * time.Second
		}
		return r
	}
	return netResult(err)
}

// telegramGetMe 校验 token，返回 bot 用户名。
func (s *Service) telegramGetMe(ctx context.Context, cfg tgConfig) (string, error) {
	var me struct {
		Username string `json:"username"`
	}
	if err := tgCall(ctx, s.cfg.HTTP, cfg, "getMe", struct{}{}, &me); err != nil {
		var te *tgError
		if errors.As(err, &te) {
			return "", errInvalidChannel("telegram getMe failed: %s", te.Description)
		}
		return "", errInvalidChannel("telegram getMe failed: %v", err)
	}
	if me.Username == "" {
		return "", errInvalidChannel("telegram getMe returned no username")
	}
	return me.Username, nil
}

type tgChat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type tgMessage struct {
	MessageID int64      `json:"message_id"`
	Chat      tgChat     `json:"chat"`
	Text      string     `json:"text"`
	ReplyTo   *tgMessage `json:"reply_to_message"`
}

type tgCallback struct {
	ID      string     `json:"id"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
	From    struct {
		ID int64 `json:"id"`
	} `json:"from"`
}

type tgUpdate struct {
	UpdateID      int64       `json:"update_id"`
	Message       *tgMessage  `json:"message"`
	CallbackQuery *tgCallback `json:"callback_query"`
}

type tgButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type tgMarkup struct {
	InlineKeyboard [][]tgButton `json:"inline_keyboard"`
}

// tgSend 发一条纯文本消息，返回 message_id。
func (s *Service) tgSend(ctx context.Context, cfg tgConfig, chatID int64, txt string, markup *tgMarkup) (int64, error) {
	body := map[string]any{"chat_id": chatID, "text": txt, "link_preview_options": map[string]bool{"is_disabled": true}}
	if markup != nil {
		body["reply_markup"] = markup
	}
	var m tgMessage
	if err := tgCall(ctx, s.cfg.HTTP, cfg, "sendMessage", body, &m); err != nil {
		return 0, err
	}
	return m.MessageID, nil
}

// tgClearButtons 去掉一条消息的内联按钮（尽力而为）。
func (s *Service) tgClearButtons(ctx context.Context, cfg tgConfig, chatID, messageID int64) {
	err := tgCall(ctx, s.cfg.HTTP, cfg, "editMessageReplyMarkup", map[string]any{
		"chat_id": chatID, "message_id": messageID, "reply_markup": tgMarkup{InlineKeyboard: [][]tgButton{}},
	}, nil)
	if err != nil {
		slog.Debug("notify: 撤回 Telegram 按钮失败", "chat", chatID, "message", messageID, "err", err)
	}
}

// ---- 发送方 ----

type telegramSender struct {
	s     *Service
	cfg   tgConfig
	state tgState
}

func (s *Service) telegramSender(ch db.NotificationChannel, raw []byte) (Sender, error) {
	var cfg tgConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	return telegramSender{s, cfg, parseTgState(ch.State)}, nil
}

var kindEmoji = map[string]string{KindDone: "✅", KindInput: "❓", KindError: "⚠️", KindTest: "🔔"}

func labelsFor(locale string) uiprompt.Labels {
	return uiprompt.Labels{Yes: text(locale, "yes"), No: text(locale, "no")}
}

// telegramText 组装消息正文与（input 且可点选时的）按钮。
func telegramText(m Message) (string, *tgMarkup) {
	head := kindEmoji[m.Kind] + " " + displayTitle(m)
	body := displayBody(m)
	var tail []string
	var markup *tgMarkup
	switch m.Kind {
	case KindInput:
		p := uiprompt.Describe(m.Request, labelsFor(m.Locale))
		if p.Body != "" {
			body = strings.TrimSpace(body + "\n" + p.Body)
		}
		if p.Supported && len(p.Options) > 0 {
			var opts []string
			markup = &tgMarkup{}
			for i, o := range p.Options {
				opts = append(opts, fmt.Sprintf("%d. %s", i+1, o))
				if i < maxButtons {
					markup.InlineKeyboard = append(markup.InlineKeyboard, []tgButton{{Text: truncate(o, maxButtonText), CallbackData: fmt.Sprintf("o:%s:%d", m.DeliveryID, i)}})
				}
			}
			tail = append(tail, "", strings.Join(opts, "\n"))
		}
		if p.Supported {
			tail = append(tail, text(m.Locale, "answerHint"))
		} else {
			tail = append(tail, text(m.Locale, "needWeb"))
		}
	case KindTest:
	default:
		tail = append(tail, "", text(m.Locale, "replyHint"))
	}
	if m.URL != "" {
		tail = append(tail, m.URL)
	}
	rest := strings.Join(tail, "\n")
	budget := maxTelegramText - utf8Len(head) - utf8Len(rest) - 2
	if budget < 0 {
		budget = 0
	}
	if utf8Len(body) > budget {
		body = truncate(body, max(budget, 1))
	}
	parts := []string{head}
	if body != "" {
		parts = append(parts, body)
	}
	if rest != "" {
		parts = append(parts, rest)
	}
	return truncate(strings.Join(parts, "\n"), maxTelegramText), markup
}

// errNotLinked 是未绑定私聊时的投递失败（不可重试）。
var errNotLinked = errors.New("telegram: not linked")

func (t telegramSender) Send(ctx context.Context, m Message) Result {
	if t.state.ChatID == 0 {
		return Result{Err: errNotLinked}
	}
	txt, markup := telegramText(m)
	id, err := t.s.tgSend(ctx, t.cfg, t.state.ChatID, txt, markup)
	if err != nil {
		return tgResult(err)
	}
	return Result{Ref: fmt.Sprintf("%d:%d", t.state.ChatID, id)}
}

// ---- 绑定 ----

// LinkTelegram 为 Telegram 渠道生成一次性绑定码（10 分钟有效）；在 Telegram 里向 bot 发 `/start <码>` 完成绑定。
func (s *Service) LinkTelegram(ctx context.Context, id string) (apigen.TelegramLink, error) {
	ch, err := s.getChannelRow(ctx, id)
	if err != nil {
		return apigen.TelegramLink{}, err
	}
	if ch.Kind != "telegram" {
		return apigen.TelegramLink{}, errInvalidChannel("channel %s is not a Telegram channel", id)
	}
	code, err := newLinkCode()
	if err != nil {
		return apigen.TelegramLink{}, err
	}
	exp := s.cfg.Now().Add(linkCodeTTL).UnixMilli()
	st, err := s.updateState(ctx, id, func(st *tgState) { st.LinkCode, st.LinkExpires = code, exp })
	if err != nil {
		return apigen.TelegramLink{}, err
	}
	return apigen.TelegramLink{Code: code[:4] + "-" + code[4:], BotUsername: st.BotUsername, ExpiresAt: exp}, nil
}

func newLinkCode() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = linkCodeAlphabet[int(b[i])%len(linkCodeAlphabet)]
	}
	return string(b), nil
}

// updateState 串行地读改写渠道的 tgState，返回写入后的值。
func (s *Service) updateState(ctx context.Context, id string, fn func(*tgState)) (tgState, error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	ch, err := s.st.Q.GetNotificationChannel(ctx, id)
	if err != nil {
		return tgState{}, err
	}
	st := parseTgState(ch.State)
	fn(&st)
	b, _ := json.Marshal(st)
	return st, s.st.Q.SetNotificationChannelState(ctx, db.SetNotificationChannelStateParams{State: string(b), ID: id})
}

// ---- 轮询 ----

type poller struct {
	cancel context.CancelFunc
	sig    string
}

// reloadTelegram 让轮询 goroutine 与渠道配置一致：启用的 Telegram 渠道各一个；停用、删除或换了 token 的取消并重建。
// Run 之前调用时只记录，不启动。
func (s *Service) reloadTelegram(ctx context.Context) {
	s.tgMu.Lock()
	defer s.tgMu.Unlock()
	if s.runCtx == nil {
		return
	}
	chs, err := s.st.Q.ListNotificationChannels(ctx)
	if err != nil {
		slog.Warn("notify: 读取渠道失败", "err", err)
		return
	}
	want := map[string]string{}
	for _, ch := range chs {
		if ch.Kind != "telegram" || !ch.Enabled {
			continue
		}
		cfg, err := s.openConfig(ch)
		if err != nil {
			slog.Warn("notify: 解密 Telegram 配置失败", "channel", ch.ID, "err", err)
			continue
		}
		want[ch.ID] = string(cfg)
	}
	for id, p := range s.pollers {
		if want[id] != p.sig {
			p.cancel()
			delete(s.pollers, id)
		}
	}
	for id, sig := range want {
		if s.pollers[id] != nil {
			continue
		}
		pctx, cancel := context.WithCancel(s.runCtx)
		s.pollers[id] = &poller{cancel: cancel, sig: sig}
		go s.pollLoop(pctx, id)
	}
}

func (s *Service) pollLoop(ctx context.Context, id string) {
	wait := pollBackoffMin
	for ctx.Err() == nil {
		err := s.pollOnce(ctx, id)
		if err == nil {
			wait = pollBackoffMin
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			return
		}
		msg := truncate("getUpdates: "+err.Error(), maxErrorLen)
		_ = s.st.Q.SetNotificationChannelError(ctx, db.SetNotificationChannelErrorParams{LastError: &msg, ID: id})
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, pollBackoffMax)
	}
}

// pollOnce 执行一次 getUpdates（长轮询），逐条处理后把 offset 写回渠道状态。
func (s *Service) pollOnce(ctx context.Context, id string) error {
	ch, err := s.st.Q.GetNotificationChannel(ctx, id)
	if err != nil {
		return err
	}
	raw, err := s.openConfig(ch)
	if err != nil {
		return err
	}
	var cfg tgConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	st := parseTgState(ch.State)
	body := map[string]any{"timeout": pollWaitSeconds, "allowed_updates": []string{"message", "callback_query"}}
	if st.Offset > 0 {
		body["offset"] = st.Offset
	}
	var ups []tgUpdate
	if err := tgCall(ctx, s.cfg.PollHTTP, cfg, "getUpdates", body, &ups); err != nil {
		return err
	}
	if len(ups) == 0 {
		return nil
	}
	for _, u := range ups {
		s.handleUpdate(ctx, id, cfg, ch.Locale, u)
	}
	last := ups[len(ups)-1].UpdateID
	_, err = s.updateState(ctx, id, func(st *tgState) { st.Offset = last + 1 })
	return err
}

// ---- 入站 ----

func (s *Service) handleUpdate(ctx context.Context, id string, cfg tgConfig, locale string, u tgUpdate) {
	ch, err := s.st.Q.GetNotificationChannel(ctx, id)
	if err != nil {
		return
	}
	st := parseTgState(ch.State)
	reply := func(chat int64, key string, extra ...string) {
		txt := text(locale, key)
		if len(extra) > 0 {
			txt += ": " + strings.Join(extra, " ")
		}
		if _, err := s.tgSend(ctx, cfg, chat, txt, nil); err != nil {
			slog.Debug("notify: 回复 Telegram 消息失败", "err", err)
		}
	}
	if cb := u.CallbackQuery; cb != nil {
		if cb.Message == nil || st.ChatID == 0 || cb.Message.Chat.ID != st.ChatID {
			return
		}
		s.handleCallback(ctx, id, cfg, locale, cb)
		return
	}
	m := u.Message
	if m == nil {
		return
	}
	txt := strings.TrimSpace(m.Text)
	if cmd, arg, _ := strings.Cut(txt, " "); cmd == "/start" || strings.HasPrefix(cmd, "/start@") {
		s.handleStart(ctx, id, cfg, locale, st, m, strings.TrimSpace(arg))
		return
	}
	if st.ChatID == 0 || m.Chat.ID != st.ChatID {
		return // 只接受已绑定的私聊
	}
	if m.ReplyTo == nil {
		reply(m.Chat.ID, "replyHint")
		return
	}
	d, err := s.st.Q.GetNotificationDeliveryByRef(ctx, db.GetNotificationDeliveryByRefParams{Target: "telegram", Ref: fmt.Sprintf("%d:%d", m.Chat.ID, m.ReplyTo.MessageID)})
	if err != nil {
		reply(m.Chat.ID, "replyHint")
		return
	}
	n, err := s.st.Q.GetNotification(ctx, d.NotificationID)
	if err != nil || n.ThreadID == nil {
		reply(m.Chat.ID, "replyHint")
		return
	}
	threadID := *n.ThreadID
	if n.Kind == KindInput && n.ResolvedAt == nil && n.Request != nil {
		frame, err := uiprompt.AnswerText(json.RawMessage(*n.Request), txt)
		switch {
		case errors.Is(err, uiprompt.ErrNeedOption):
			reply(m.Chat.ID, "answerHint")
			return
		case errors.Is(err, uiprompt.ErrUnsupported):
			reply(m.Chat.ID, "needWeb")
			return
		case err != nil:
			reply(m.Chat.ID, "failed", err.Error())
			return
		}
		switch err := s.th.UIResponse(ctx, threadID, frame); {
		case err == nil:
			reply(m.Chat.ID, "answered")
		case errors.Is(err, threads.ErrUIResolved):
			reply(m.Chat.ID, "answeredElsewhere")
		default:
			reply(m.Chat.ID, "failed", err.Error())
		}
		return
	}
	frame, _ := json.Marshal(map[string]string{"type": "prompt", "message": txt, "streamingBehavior": "steer"})
	chat := m.Chat.ID
	// 应答回调在线程锁内调用：只起 goroutine 回消息，不在回调里做网络请求
	onReply := func(resp json.RawMessage) {
		var r struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(resp, &r)
		go func() {
			rctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
			defer cancel()
			txt := text(locale, "sent")
			if !r.Success {
				txt = text(locale, "failed") + ": " + r.Error
			}
			if _, err := s.tgSend(rctx, cfg, chat, txt, nil); err != nil {
				slog.Debug("notify: 回复 Telegram 消息失败", "err", err)
			}
		}()
	}
	if err := s.th.Command(ctx, threadID, frame, onReply); err != nil {
		reply(chat, "failed", err.Error())
	}
}

// handleStart 处理 `/start <码>`：私聊里码有效（未过期、常数时间比较）即绑定（含换绑）；已绑定的聊天发来回 linked；
// 其余聊天在已绑定时静默丢弃。
func (s *Service) handleStart(ctx context.Context, id string, cfg tgConfig, locale string, st tgState, m *tgMessage, arg string) {
	code := strings.ToUpper(strings.ReplaceAll(arg, "-", ""))
	valid := m.Chat.Type == "private" && st.LinkCode != "" && s.now() < st.LinkExpires &&
		subtle.ConstantTimeCompare([]byte(code), []byte(st.LinkCode)) == 1
	switch {
	case valid:
		title := m.Chat.FirstName
		if title == "" {
			title = m.Chat.Username
		}
		if _, err := s.updateState(ctx, id, func(st *tgState) {
			st.ChatID, st.ChatTitle, st.LinkCode, st.LinkExpires = m.Chat.ID, title, "", 0
		}); err != nil {
			slog.Warn("notify: 写入 Telegram 绑定失败", "channel", id, "err", err)
			return
		}
		if _, err := s.tgSend(ctx, cfg, m.Chat.ID, text(locale, "linked"), nil); err != nil {
			slog.Debug("notify: 回复 Telegram 消息失败", "err", err)
		}
	case st.ChatID != 0 && m.Chat.ID == st.ChatID:
		_, _ = s.tgSend(ctx, cfg, m.Chat.ID, text(locale, "linked"), nil)
	case st.ChatID == 0 && m.Chat.Type == "private":
		_, _ = s.tgSend(ctx, cfg, m.Chat.ID, text(locale, "notLinked"), nil)
	}
}

// handleCallback 处理选项按钮：`o:<deliveryID>:<序号>` → 作答对应的待答请求。
func (s *Service) handleCallback(ctx context.Context, id string, cfg tgConfig, locale string, cb *tgCallback) {
	answer := func(key string) {
		_ = tgCall(ctx, s.cfg.HTTP, cfg, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID, "text": text(locale, key)}, nil)
	}
	parts := strings.Split(cb.Data, ":")
	if len(parts) != 3 || parts[0] != "o" {
		answer("failed")
		return
	}
	idx, err := strconv.Atoi(parts[2])
	if err != nil || idx < 0 {
		answer("failed")
		return
	}
	d, err := s.st.Q.GetNotificationDelivery(ctx, parts[1])
	if err != nil || d.ChannelID == nil || *d.ChannelID != id {
		answer("failed")
		return
	}
	n, err := s.st.Q.GetNotification(ctx, d.NotificationID)
	if err != nil || n.Kind != KindInput || n.Request == nil || n.ThreadID == nil {
		answer("failed")
		return
	}
	dropButtons := func() { s.tgClearButtons(ctx, cfg, cb.Message.Chat.ID, cb.Message.MessageID) }
	if n.ResolvedAt != nil {
		answer("answeredElsewhere")
		dropButtons()
		return
	}
	frame, err := uiprompt.AnswerOption(json.RawMessage(*n.Request), idx)
	if err != nil {
		answer("failed")
		return
	}
	switch err := s.th.UIResponse(ctx, *n.ThreadID, frame); {
	case err == nil:
		answer("answered")
		dropButtons()
	case errors.Is(err, threads.ErrUIResolved):
		answer("answeredElsewhere")
		dropButtons()
	default:
		answer("failed")
	}
}

// revokeButtons 撤回一条通知在 Telegram 里的选项按钮（请求已在别处作答 / 取消）。
func (s *Service) revokeButtons(ctx context.Context, notificationID string) {
	refs, err := s.st.Q.ListDeliveryRefsByNotification(ctx, notificationID)
	if err != nil {
		return
	}
	for _, d := range refs {
		if d.Target != "telegram" || d.ChannelID == nil || d.ExternalRef == nil {
			continue
		}
		chat, msg, ok := parseRef(*d.ExternalRef)
		if !ok {
			continue
		}
		ch, err := s.st.Q.GetNotificationChannel(ctx, *d.ChannelID)
		if err != nil {
			continue
		}
		raw, err := s.openConfig(ch)
		if err != nil {
			continue
		}
		var cfg tgConfig
		if json.Unmarshal(raw, &cfg) != nil {
			continue
		}
		s.tgClearButtons(ctx, cfg, chat, msg)
	}
}

func parseRef(ref string) (chat, msg int64, ok bool) {
	a, b, found := strings.Cut(ref, ":")
	if !found {
		return 0, 0, false
	}
	c, err1 := strconv.ParseInt(a, 10, 64)
	m, err2 := strconv.ParseInt(b, 10, 64)
	return c, m, err1 == nil && err2 == nil
}
