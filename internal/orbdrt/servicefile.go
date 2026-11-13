package orbdrt

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/Lynricsy/Oryn/internal/apigen"
)

// 受管服务的定义（M4 计划 2.1）：仓库内 `.oryn/services.yaml`（回退 `.amp/services.yaml`）的解析与校验，
// 以及动态服务（`oryn orb service start --command`）共用的定义校验。

const (
	// maxServices 是每台 orb 的服务数上限（文件服务 + 动态服务）。
	maxServices = 32
	// maxLinkTitle / maxLinkPath 是 portal 链接标题（含分组名）与路径的上限。
	maxLinkTitle = 80
	maxLinkPath  = 512
)

var (
	serviceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	envKeyRe      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// publicURLRefRe 是 env 值里对其它服务 PUBLIC_URL 的引用。
	publicURLRefRe = regexp.MustCompile(`\$\{services\.([a-z0-9][a-z0-9-]{0,31})\.publicURL\}`)
)

// reservedServiceEnv 是由 orbd 设置、不得出现在服务 env 里的变量。
var reservedServiceEnv = map[string]bool{"PORT": true, "PUBLIC_URL": true, "ORYN_THREAD_ID": true}

// serviceFileNames 是服务文件相对工作区根的位置，先存在者为准（不合并两份）。
var serviceFileNames = []string{".oryn/services.yaml", ".amp/services.yaml"}

// serviceSpec 是一个服务的定义。Port 在合并后是实际使用的端口（显式或自动分配），
// PortExplicit 记录定义里是否写了端口（决定是否需要就绪探测）。
type serviceSpec struct {
	Name, Command, Cwd, Health, ThreadID string
	Source                               apigen.OrbServiceSource
	Port                                 int
	PortExplicit                         bool
	Env                                  map[string]string
	Links                                []apigen.OrbPortalLink
}

// sameDefinition 报告两份定义在「是否需要重启进程」意义上是否相同（不比较所属线程：
// 线程只影响 PUBLIC_URL，已在运行的服务不因它变化而重启）。
func (a serviceSpec) sameDefinition(b serviceSpec) bool {
	return a.Name == b.Name && a.Command == b.Command && a.Cwd == b.Cwd && a.Health == b.Health &&
		a.Source == b.Source && a.Port == b.Port && a.PortExplicit == b.PortExplicit &&
		maps.Equal(a.Env, b.Env) && slices.EqualFunc(a.Links, b.Links, sameLink)
}

func sameLink(a, b apigen.OrbPortalLink) bool {
	return a.Title == b.Title && a.Path == b.Path && strOr(a.Folder) == strOr(b.Folder)
}

func strOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// fileEntry 是文件里的一个服务；err 非空表示定义无效（以 failed 列出、不启动）。
type fileEntry struct {
	spec serviceSpec
	err  string
}

// rawService 是 services.yaml 中一个服务的原始形态（未知字段忽略，如 `review`、`description`）。
type rawService struct {
	Command   string            `yaml:"command"`
	Cwd       string            `yaml:"cwd"`
	Port      int               `yaml:"port"`
	Env       map[string]string `yaml:"env"`
	Health    string            `yaml:"health"`
	Portal    yaml.Node         `yaml:"portal"`
	Portals   []rawLink         `yaml:"portals"`
	Platforms []string          `yaml:"platforms"`
}

// rawLink 是 `portals[]` 的一项：`{url, title, folder?}`，或 Amp 的分组形态 `{folder, links: [{url, title}]}`。
type rawLink struct {
	URL    string    `yaml:"url"`
	Title  string    `yaml:"title"`
	Folder string    `yaml:"folder"`
	Links  []rawLink `yaml:"links"`
}

// loadServiceFile 读取工作区里的服务文件。found=false 表示两个位置都没有文件；
// err 是整份文件的错误（读失败、YAML 语法错误、`services` 不是映射），此时 entries 无意义。
func loadServiceFile(workdir string, warn func(string)) (entries []fileEntry, found bool, err error) {
	for _, rel := range serviceFileNames {
		path := filepath.Join(workdir, rel)
		b, rerr := os.ReadFile(path)
		if errors.Is(rerr, os.ErrNotExist) {
			continue
		}
		if rerr != nil {
			return nil, true, fmt.Errorf("%s: %w", rel, rerr)
		}
		entries, err = parseServiceFile(b, warn)
		if err != nil {
			return nil, true, fmt.Errorf("%s: %w", rel, err)
		}
		return entries, true, nil
	}
	return nil, false, nil
}

// parseServiceFile 解析 services.yaml 的内容，按文件顺序返回服务；`platforms` 给出且不含 linux 的服务跳过。
func parseServiceFile(data []byte, warn func(string)) ([]fileEntry, error) {
	var doc struct {
		Services yaml.Node `yaml:"services"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	n := &doc.Services
	switch {
	case n.Kind == 0 || (n.Kind == yaml.ScalarNode && n.Tag == "!!null"):
		return nil, nil
	case n.Kind != yaml.MappingNode:
		return nil, errors.New("`services` must be a mapping of service name to definition")
	}
	var out []fileEntry
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		name, val := n.Content[i].Value, n.Content[i+1]
		e := fileEntry{spec: serviceSpec{Name: name, Source: apigen.OrbServiceSourceFile, Links: []apigen.OrbPortalLink{}}}
		if seen[name] {
			e.err = "duplicate service name"
			out = append(out, e)
			continue
		}
		seen[name] = true
		var raw rawService
		if err := val.Decode(&raw); err != nil {
			e.err = "invalid definition: " + err.Error()
			out = append(out, e)
			continue
		}
		if raw.Platforms != nil && !slices.Contains(raw.Platforms, "linux") {
			continue
		}
		e.spec, e.err = raw.spec(name, warn)
		out = append(out, e)
	}
	return out, nil
}

// spec 把原始定义转成 serviceSpec 并校验；返回的错误串非空表示该服务无效。
func (raw rawService) spec(name string, warn func(string)) (serviceSpec, string) {
	sp := serviceSpec{
		Name: name, Source: apigen.OrbServiceSourceFile, Command: raw.Command, Cwd: raw.Cwd,
		Health: raw.Health, Port: raw.Port, PortExplicit: raw.Port != 0, Env: raw.Env,
		Links: []apigen.OrbPortalLink{},
	}
	main, err := parsePortalField(name, &raw.Portal, warn)
	if err != nil {
		return sp, err.Error()
	}
	if main != nil {
		sp.Links = append(sp.Links, *main)
	}
	for _, l := range raw.Portals {
		items := []rawLink{l}
		if len(l.Links) > 0 {
			items = l.Links
			for i := range items {
				items[i].Folder = l.Folder
			}
		}
		for _, it := range items {
			path, skip, err := linkPath(it.URL)
			if skip {
				warn(fmt.Sprintf("service %s: skipping portal link %q (only paths starting with / are supported)", name, it.URL))
				continue
			}
			if err != nil {
				return sp, err.Error()
			}
			title := it.Title
			if title == "" {
				title = path
			}
			link := apigen.OrbPortalLink{Title: title, Path: path}
			if it.Folder != "" {
				link.Folder = new(it.Folder)
			}
			sp.Links = append(sp.Links, link)
		}
	}
	if msg := validateSpec(&sp); msg != "" {
		return sp, msg
	}
	return sp, ""
}

// parsePortalField 解析 `portal`：true → `{服务名, /}`；字符串 → `{服务名, 该路径}`；映射 `{url?, title?}` 同理；
// false / 省略 → nil。绝对 URL 跳过并记日志。
func parsePortalField(name string, n *yaml.Node, warn func(string)) (*apigen.OrbPortalLink, error) {
	switch n.Kind {
	case 0:
		return nil, nil
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			return nil, nil
		}
		if n.Tag == "!!bool" {
			var b bool
			if err := n.Decode(&b); err != nil {
				return nil, fmt.Errorf("invalid portal: %v", err)
			}
			if !b {
				return nil, nil
			}
			return &apigen.OrbPortalLink{Title: name, Path: "/"}, nil
		}
		path, skip, err := linkPath(n.Value)
		if skip {
			warn(fmt.Sprintf("service %s: skipping portal %q (only paths starting with / are supported)", name, n.Value))
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return &apigen.OrbPortalLink{Title: name, Path: path}, nil
	case yaml.MappingNode:
		var m struct {
			URL   string `yaml:"url"`
			Title string `yaml:"title"`
		}
		if err := n.Decode(&m); err != nil {
			return nil, fmt.Errorf("invalid portal: %v", err)
		}
		u := m.URL
		if u == "" {
			u = "/"
		}
		path, skip, err := linkPath(u)
		if skip {
			warn(fmt.Sprintf("service %s: skipping portal %q (only paths starting with / are supported)", name, u))
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		title := m.Title
		if title == "" {
			title = name
		}
		return &apigen.OrbPortalLink{Title: title, Path: path}, nil
	default:
		return nil, errors.New("invalid portal: must be true/false, a path, or {url, title}")
	}
}

// linkPath 校验 portal 链接路径；绝对 URL（含 `://`）返回 skip=true。
func linkPath(u string) (path string, skip bool, err error) {
	switch {
	case strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//"):
		return u, false, nil
	case strings.Contains(u, "://"):
		return "", true, nil
	default:
		return "", false, fmt.Errorf("invalid portal url %q: must be a path starting with /", u)
	}
}

// validateSpec 校验并规范化定义（文件服务与动态服务共用）；返回非空串表示无效。
func validateSpec(sp *serviceSpec) string {
	if !serviceNameRe.MatchString(sp.Name) {
		return "invalid service name (want ^[a-z0-9][a-z0-9-]{0,31}$)"
	}
	if strings.TrimSpace(sp.Command) == "" {
		return "command is required"
	}
	cwd, err := cleanCwd(sp.Cwd)
	if err != nil {
		return err.Error()
	}
	sp.Cwd = cwd
	if sp.Port < 0 || sp.Port > 65535 {
		return fmt.Sprintf("port %d out of range 1-65535", sp.Port)
	}
	for k := range sp.Env {
		if !envKeyRe.MatchString(k) {
			return fmt.Sprintf("invalid env name %q", k)
		}
		if reservedServiceEnv[k] {
			return fmt.Sprintf("env %s is managed by Oryn and cannot be set", k)
		}
	}
	if sp.Health != "" && (!strings.HasPrefix(sp.Health, "/") || len(sp.Health) > maxLinkPath) {
		return "health must be an HTTP path starting with /"
	}
	if sp.Links == nil {
		sp.Links = []apigen.OrbPortalLink{}
	}
	for _, l := range sp.Links {
		if utf8.RuneCountInString(l.Title) > maxLinkTitle || utf8.RuneCountInString(strOr(l.Folder)) > maxLinkTitle {
			return fmt.Sprintf("portal title longer than %d characters", maxLinkTitle)
		}
		if len(l.Path) > maxLinkPath {
			return fmt.Sprintf("portal url longer than %d bytes", maxLinkPath)
		}
	}
	return ""
}

// cleanCwd 规范化相对工作区根的 cwd；绝对路径或逃出工作区的路径无效。
func cleanCwd(c string) (string, error) {
	if c == "" {
		return "", nil
	}
	if filepath.IsAbs(c) {
		return "", fmt.Errorf("cwd %q must be relative to the workspace root", c)
	}
	cl := filepath.Clean(c)
	if cl == "." {
		return "", nil
	}
	if cl == ".." || strings.HasPrefix(cl, "../") {
		return "", fmt.Errorf("cwd %q escapes the workspace root", c)
	}
	return cl, nil
}

// publicURLRefs 返回 env 值里引用到的服务名（去重、有序）。
func publicURLRefs(env map[string]string) []string {
	var out []string
	for _, v := range env {
		for _, m := range publicURLRefRe.FindAllStringSubmatch(v, -1) {
			if !slices.Contains(out, m[1]) {
				out = append(out, m[1])
			}
		}
	}
	slices.Sort(out)
	return out
}
