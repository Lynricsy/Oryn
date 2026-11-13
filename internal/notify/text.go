package notify

// 渠道消息的固定文案（M5 计划 3.2）：键 → 语言 → 文本。Web Push 与应用内通知的固定文案由前端按界面语言渲染。
var texts = map[string]map[string]string{
	"untitled":          {"zh-CN": "未命名线程", "en": "Untitled thread"},
	"done":              {"zh-CN": "回合已完成", "en": "Turn finished"},
	"input":             {"zh-CN": "等待你的回答", "en": "Waiting for your answer"},
	"plan":              {"zh-CN": "计划等待批准", "en": "Plan awaiting approval"},
	"error":             {"zh-CN": "线程出错", "en": "Thread failed"},
	"replyHint":         {"zh-CN": "回复这条消息即可把文字发给该线程。", "en": "Reply to this message to send text to the thread."},
	"answerHint":        {"zh-CN": "点按钮，或回复选项编号 / 文字作答。", "en": "Tap a button, or reply with an option number or text."},
	"needWeb":           {"zh-CN": "这个请求需要在 Oryn 网页里作答。", "en": "Answer this request in the Oryn web app."},
	"linked":            {"zh-CN": "已绑定 Oryn 通知。", "en": "Linked to Oryn notifications."},
	"notLinked":         {"zh-CN": "绑定码无效或已过期，请在 Oryn 设置页重新生成。", "en": "Invalid or expired link code. Generate a new one in Oryn settings."},
	"sent":              {"zh-CN": "已发送。", "en": "Sent."},
	"answered":          {"zh-CN": "已作答。", "en": "Answered."},
	"answeredElsewhere": {"zh-CN": "已在别处作答。", "en": "Already answered elsewhere."},
	"failed":            {"zh-CN": "失败", "en": "Failed"},
	"testTitle":         {"zh-CN": "Oryn 测试通知", "en": "Oryn test notification"},
	"yes":               {"zh-CN": "是", "en": "Yes"},
	"no":                {"zh-CN": "否", "en": "No"},
}

// text 返回 key 在 locale 下的文案；未知语言回落到 zh-CN。
func text(locale, key string) string {
	m := texts[key]
	if v, ok := m[locale]; ok {
		return v
	}
	return m[defaultLocale]
}

const defaultLocale = "zh-CN"
