// Package agentcfg 处理用户 omp 文本配置（omp-home）：导入时抽取密钥，下发到 orb 时改写 models.yml。
package agentcfg

import (
	"bytes"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Lynricsy/Oryn/internal/egress"
	"github.com/Lynricsy/Oryn/internal/secrets"
)

// ModelsFile 是需要做密钥抽取与改写的文件。
const ModelsFile = "models.yml"

// EgressAddr 是 orb 内 orbd 出站代理的监听地址。
const EgressAddr = "127.0.0.1:7811"

// BuiltinBaseURLs 是 models.yml 未写 baseUrl 时可走代理的内置 provider 上游。
var BuiltinBaseURLs = map[string]string{
	"anthropic":  "https://api.anthropic.com",
	"openai":     "https://api.openai.com/v1",
	"google":     "https://generativelanguage.googleapis.com",
	"deepseek":   "https://api.deepseek.com",
	"openrouter": "https://openrouter.ai/api/v1",
}

var (
	refRe      = regexp.MustCompile(`\$\{oryn:secret:([^}]+)\}`)
	nonAlnumRe = regexp.MustCompile(`[^A-Za-z0-9]`)
)

// SecretRef 返回模板中的密钥引用写法。
func SecretRef(name string) string { return "${oryn:secret:" + name + "}" }

// ModelKeySecretName 是 models.yml 中 provider apiKey 对应的密钥名。
func ModelKeySecretName(providerKey string) string { return "omp/models/" + providerKey + "/apiKey" }

// Placeholder 是下发到 orb 的占位令牌，控制面 egress 代理据此替换真实密钥。
func Placeholder(secretID string) string { return "oryn-ph-" + secretID }

// EnvFallbackName 是环境变量降级时使用的变量名。
func EnvFallbackName(providerKey string) string {
	return "ORYN_MODEL_KEY_" + strings.ToUpper(nonAlnumRe.ReplaceAllString(providerKey, "_"))
}

// excludedDirs / excludedGlobs 是 `oryn config push` 不上传的路径。
var (
	// ssh-keys/（私钥）、remote-pairing.json（配对令牌）、*.bak（常为含明文密钥的 models.yml 备份）同样不上传：
	// 它们不是文本配置，却会把凭据带进 orb。
	// memories/（记忆库运行时数据）与 skill-repositories/（按 agent-config-hub.json 克隆的技能仓库缓存，
	// 可由配置重新拉取，体积常达上百 MiB）同样排除。
	excludedDirs  = []string{"sessions", "blobs", "cache", "logs", "natives", "terminal-sessions", "ssh-keys", "memories", "skill-repositories"}
	excludedGlobs = []string{"*.db", "*.db-*", "auth.json", "history*", "remote-pairing.json", "*.bak", "*.bak.*"}
)

// Excluded 判断相对路径（以 / 分隔）是否应排除在导入之外。
func Excluded(rel string) bool {
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	first, _, _ := strings.Cut(rel, "/")
	for _, d := range excludedDirs {
		if first == d {
			return true
		}
	}
	// 版本库元数据与依赖目录不是配置（且常含超大文件）
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".git" || seg == "node_modules" {
			return true
		}
	}
	base := path.Base(rel)
	for _, g := range excludedGlobs {
		if ok, _ := path.Match(g, base); ok {
			return true
		}
	}
	return false
}

var secretLikeRes = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`ghp_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	// 通用长串：≥32 位 base64url 字符；ScanSecretLike 只保留大小写字母与数字混排的命中，
	// 排除 git 哈希、校验和、单词拼接等误报。
	genericSecretRe,
}

var genericSecretRe = regexp.MustCompile(`(?:^|[^A-Za-z0-9+/_-])([A-Za-z0-9+_-]{32,}={0,2})(?:$|[^A-Za-z0-9+/=_-])`)

// mixedClasses 判断串同时含大写、小写与数字。
func mixedClasses(s []byte) bool {
	var up, lo, dg bool
	for _, c := range s {
		switch {
		case c >= 'A' && c <= 'Z':
			up = true
		case c >= 'a' && c <= 'z':
			lo = true
		case c >= '0' && c <= '9':
			dg = true
		}
	}
	return up && lo && dg
}

// ScanSecretLike 返回内容中形似密钥的片段（已脱敏，仅保留前 4 个字符），用于告警。
func ScanSecretLike(content []byte) []string {
	seen := map[string]bool{}
	var out []string
	for _, re := range secretLikeRes {
		for _, m := range re.FindAllSubmatch(content, -1) {
			s := m[0]
			if len(m) > 1 && m[1] != nil {
				s = m[1]
			}
			if re == genericSecretRe && !mixedClasses(s) {
				continue
			}
			masked := string(s[:4]) + "…"
			if !seen[masked] {
				seen[masked] = true
				out = append(out, masked)
			}
		}
	}
	return out
}

// ImportResult 是导入结果。
type ImportResult struct {
	// Files 是落盘的模板文件（models.yml 已把字面密钥替换为引用）。
	Files map[string][]byte
	// Secrets 是抽取出的密钥名 → 明文，调用方写入密钥库（mode=proxy）。
	Secrets map[string]string
	// SecretHosts 是密钥名 → 允许的上游主机（provider 的 baseUrl / 内置上游主机；未知时为 nil）。
	SecretHosts map[string][]string
	// Warnings 是给用户看的提示。
	Warnings []string
}

// Import 处理一次 `oryn config push` 的全部文件。
func Import(files map[string][]byte) (ImportResult, error) {
	res := ImportResult{Files: map[string][]byte{}, Secrets: map[string]string{}, SecretHosts: map[string][]string{}}
	for _, p := range sortedKeys(files) {
		content := files[p]
		if Excluded(p) {
			continue
		}
		if p == ModelsFile {
			out, err := importModels(content, res.Secrets, res.SecretHosts)
			if err != nil {
				return res, fmt.Errorf("%s: %w", p, err)
			}
			res.Files[p] = out
			continue
		}
		if hits := ScanSecretLike(content); len(hits) > 0 && isText(content) {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s 含形似密钥的字符串（%s），该文件原样下发到 orb", p, strings.Join(hits, ", ")))
		}
		res.Files[p] = content
	}
	return res, nil
}

func importModels(content []byte, out map[string]string, hosts map[string][]string) ([]byte, error) {
	doc, providers, err := parseModels(content)
	if err != nil || providers == nil {
		return content, err
	}
	for _, kv := range mapPairs(providers) {
		key, prov := kv[0].Value, kv[1]
		if prov.Kind != yaml.MappingNode {
			continue
		}
		ak := mapGet(prov, "apiKey")
		if ak == nil || ak.Kind != yaml.ScalarNode || !isLiteralKey(ak.Value) {
			continue
		}
		name := ModelKeySecretName(key)
		out[name] = ak.Value
		if u, err := url.Parse(providerBase(key, prov)); err == nil && u.Hostname() != "" {
			hosts[name] = []string{u.Hostname()}
		}
		ak.Value = SecretRef(name)
		ak.Style = yaml.DoubleQuotedStyle
	}
	return encode(doc)
}

// isLiteralKey：不是 `!命令`、不是环境变量名、不是已有引用。
func isLiteralKey(v string) bool {
	return v != "" && !strings.HasPrefix(v, "!") && !secrets.IsEnvName(v) && !refRe.MatchString(v)
}

// Resolver 按名称查询密钥库。
type Resolver interface {
	// Lookup 返回密钥 id 与明文值。
	Lookup(name string) (id, value string, ok bool)
}

// Rendered 是下发到 orb 的配置。
type Rendered struct {
	Files map[string][]byte
	// Routes 是 egress 路由键 → 上游前缀（scheme://host，不含路径）与允许在该路由上使用的密钥 id。
	Routes map[string]egress.Route
	// Env 是环境变量降级时注入 agent 进程的真实密钥。
	Env map[string]string
	// Warnings 是改写过程中的提示。
	Warnings []string
}

// Render 为一台 orb 生成 omp-home 文件。
func Render(files map[string][]byte, r Resolver) (Rendered, error) {
	out := Rendered{Files: map[string][]byte{}, Routes: map[string]egress.Route{}, Env: map[string]string{}}
	for p, content := range files {
		if p != ModelsFile {
			out.Files[p] = content
			continue
		}
		b, err := renderModels(content, r, &out)
		if err != nil {
			return out, fmt.Errorf("%s: %w", p, err)
		}
		out.Files[p] = b
	}
	return out, nil
}

func renderModels(content []byte, r Resolver, out *Rendered) ([]byte, error) {
	doc, providers, err := parseModels(content)
	if err != nil || providers == nil {
		return content, err
	}
	for _, kv := range mapPairs(providers) {
		key, prov := kv[0].Value, kv[1]
		if prov.Kind != yaml.MappingNode {
			continue
		}
		origins := map[string]string{} // 本 provider 产生的路由键 → 上游前缀
		var ids []string               // 本 provider 子树中用到的密钥 id（绑定到它的全部路由）
		ak := mapGet(prov, "apiKey")
		var secretName string
		if ak != nil && ak.Kind == yaml.ScalarNode {
			if m := refRe.FindStringSubmatch(ak.Value); m != nil && m[0] == ak.Value {
				secretName = m[1]
			}
		}
		if secretName != "" {
			id, value, ok := r.Lookup(secretName)
			if !ok {
				return nil, fmt.Errorf("provider %s 引用的密钥 %s 不存在", key, secretName)
			}
			base := providerBase(key, prov)
			bu := mapGet(prov, "baseUrl")
			if base == "" {
				// 环境变量降级：omp 先把 apiKey 当环境变量名解析
				env := EnvFallbackName(key)
				ak.Value = env
				ak.Style = 0
				out.Env[env] = value
				out.Warnings = append(out.Warnings, fmt.Sprintf("provider %s 没有 baseUrl 且不在内置表中，真实密钥以环境变量 %s 进入 orb", key, env))
			} else {
				local, origin, err := rewriteBase(base, key)
				if err != nil {
					return nil, fmt.Errorf("provider %s baseUrl: %w", key, err)
				}
				ak.Value = Placeholder(id)
				ak.Style = yaml.DoubleQuotedStyle
				ids = append(ids, id)
				origins[key] = origin
				if bu == nil {
					appendPair(prov, "baseUrl", local)
				} else {
					bu.Value = local
				}
			}
			// 自定义模型条目自带的 baseUrl
			if models := mapGet(prov, "models"); models != nil && models.Kind == yaml.SequenceNode {
				for i, m := range models.Content {
					if m.Kind != yaml.MappingNode {
						continue
					}
					mb := mapGet(m, "baseUrl")
					if mb == nil || mb.Kind != yaml.ScalarNode || mb.Value == "" {
						continue
					}
					rk := fmt.Sprintf("%s.m%d", key, i)
					local, origin, err := rewriteBase(mb.Value, rk)
					if err != nil {
						return nil, fmt.Errorf("provider %s models[%d].baseUrl: %w", key, i, err)
					}
					mb.Value = local
					origins[rk] = origin
				}
			}
		}
		// provider 子树内其余密钥引用（如自定义请求头）绑定到该 provider 的路由
		more, err := replaceRefs(prov, r)
		if err != nil {
			return nil, err
		}
		ids = append(ids, more...)
		for rk, origin := range origins {
			out.Routes[rk] = egress.Route{Origin: origin, SecretIDs: ids}
		}
	}
	// providers 之外的引用：换成占位符但不绑定任何路由（egress 会拒绝使用）
	if _, err := replaceRefs(doc, r); err != nil {
		return nil, err
	}
	return encode(doc)
}

// replaceRefs 把子树中的 ${oryn:secret:…} 换成占位符，返回用到的密钥 id。
func replaceRefs(n *yaml.Node, r Resolver) ([]string, error) {
	var ids []string
	var walkErr error
	walk(n, func(n *yaml.Node) {
		if n.Kind != yaml.ScalarNode || !refRe.MatchString(n.Value) {
			return
		}
		n.Value = refRe.ReplaceAllStringFunc(n.Value, func(s string) string {
			name := refRe.FindStringSubmatch(s)[1]
			id, _, ok := r.Lookup(name)
			if !ok {
				walkErr = fmt.Errorf("引用的密钥 %s 不存在", name)
				return s
			}
			ids = append(ids, id)
			return Placeholder(id)
		})
	})
	return ids, walkErr
}

// providerBase 返回 provider 的上游 baseUrl（未写时取内置表；都没有返回空串）。
func providerBase(key string, prov *yaml.Node) string {
	if bu := mapGet(prov, "baseUrl"); bu != nil && bu.Kind == yaml.ScalarNode && bu.Value != "" {
		return bu.Value
	}
	return BuiltinBaseURLs[key]
}

// rewriteBase 把上游 baseUrl 改写为经 orbd 出站代理的地址，返回新地址与上游前缀。
func rewriteBase(base, routeKey string) (local, origin string, err error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", "", fmt.Errorf("无效 URL %q", base)
	}
	origin = u.Scheme + "://" + u.Host
	local = "http://" + EgressAddr + "/llm/" + routeKey + strings.TrimSuffix(u.EscapedPath(), "/")
	if u.RawQuery != "" {
		local += "?" + u.RawQuery
	}
	return local, origin, nil
}

func parseModels(content []byte) (*yaml.Node, *yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return &doc, nil, nil
	}
	p := mapGet(doc.Content[0], "providers")
	if p == nil || p.Kind != yaml.MappingNode {
		return &doc, nil, nil
	}
	return &doc, p, nil
}

func encode(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func mapGet(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mapPairs(m *yaml.Node) [][2]*yaml.Node {
	var out [][2]*yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		out = append(out, [2]*yaml.Node{m.Content[i], m.Content[i+1]})
	}
	return out
}

func appendPair(m *yaml.Node, key, value string) {
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

func walk(n *yaml.Node, f func(*yaml.Node)) {
	f(n)
	for _, c := range n.Content {
		walk(c, f)
	}
}

func isText(b []byte) bool { return !bytes.Contains(b, []byte{0}) }

func sortedKeys(m map[string][]byte) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
