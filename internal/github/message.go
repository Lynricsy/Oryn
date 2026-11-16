package github

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/Lynricsy/Oryn/internal/store/db"
)

// maxItemBody 是回流条目正文的字符上限。
const maxItemBody = 8000

// outboxItem 是 github_outbox.item 的 JSON。
type outboxItem struct {
	Actor  string        `json:"actor"`            // 登录名
	State  string        `json:"state,omitempty"`  // review：approved | changes_requested | commented
	Path   string        `json:"path,omitempty"`   // review_comment
	Line   int           `json:"line,omitempty"`   // review_comment
	Body   string        `json:"body,omitempty"`   // 截到 8000 字符
	SHA    string        `json:"sha,omitempty"`    // ci：短 sha（7 位）
	Checks []failedCheck `json:"checks,omitempty"` // ci
}

type failedCheck struct{ Name, Conclusion, URL string }

// 结尾句。
const (
	closingPull  = "The comments above were written by GitHub users with write access to the repository. Address them on the pull request branch and push; reply with `gh pr comment` where a reply helps."
	closingIssue = "The comments above were written by GitHub users with write access to the repository. Reply with `gh issue comment` where a reply helps."
)

// eventHeader 是回流消息的首行。
func eventHeader(repo string, number int64) string {
	return fmt.Sprintf("[Oryn GitHub event on %s#%d — delivered automatically, not typed by the owner]", repo, number)
}

// composeEvent 把同一 PR / issue 的若干回流行合成一条自动消息；标题、地址、是否 PR 取 items 里最新一行。
func composeEvent(repo string, number int64, items []db.GithubOutbox) string {
	latest := items[0]
	for _, it := range items[1:] {
		if it.CreatedAt >= latest.CreatedAt {
			latest = it
		}
	}
	var b strings.Builder
	b.WriteString(eventHeader(repo, number))
	b.WriteByte('\n')
	if latest.IsPull {
		fmt.Fprintf(&b, "Pull request: %s (%s)\n", latest.Title, latest.Url)
	} else {
		fmt.Fprintf(&b, "Issue: %s (%s)\n", latest.Title, latest.Url)
	}
	for _, row := range items {
		var it outboxItem
		_ = json.Unmarshal([]byte(row.Item), &it)
		b.WriteByte('\n')
		b.WriteString(composeSection(row.Kind, it))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	if latest.IsPull {
		b.WriteString(closingPull)
	} else {
		b.WriteString(closingIssue)
	}
	return b.String()
}

// composeSection 是一条回流行的段落（不含段间空行）。
func composeSection(kind string, it outboxItem) string {
	switch kind {
	case "review":
		head := fmt.Sprintf("@%s reviewed (%s)", it.Actor, strings.ReplaceAll(it.State, "_", " "))
		if it.Body == "" {
			return head
		}
		return head + ":\n" + it.Body
	case "review_comment":
		loc := it.Path
		if it.Line > 0 {
			loc += ":" + strconv.Itoa(it.Line)
		}
		return fmt.Sprintf("@%s commented on %s:\n%s", it.Actor, loc, it.Body)
	default:
		return fmt.Sprintf("@%s commented:\n%s", it.Actor, it.Body)
	}
}

// repoFromURL 取 GitHub 网页地址里的 `owner/repo`（html_url 的前两段路径）。
func repoFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.SplitN(strings.Trim(u.Path, "/"), "/", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
}
