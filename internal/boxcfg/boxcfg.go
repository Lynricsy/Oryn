// Package boxcfg 解析 box app 的部署配置（M23）：仓库里的 `.oryn/box.yaml`（回退 `.amp/box.yaml`）
// 与仓库根的部署目标文件 `.oryn/box-deploy.json`。纯函数，CLI 与测试共用。
package boxcfg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileNames 是 app 目录里配置文件的相对位置，先存在者为准。
var FileNames = []string{".oryn/box.yaml", ".amp/box.yaml"}

// TargetsFile 是部署目标文件相对仓库根的位置。
const TargetsFile = ".oryn/box-deploy.json"

// MaxServices 是每个 app 的服务数上限。
const MaxServices = 4

// serviceNameRe 是 box.yaml 里服务名的规则；监督器里的名字是 `<app>--<svc>`。
var serviceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,9}$`)

// ErrNotFound 表示从目录到仓库根之间没有找到 box.yaml。
var ErrNotFound = errors.New("no .oryn/box.yaml found between here and the repository root")

// Service 是一个服务：Cwd 相对 app 目录，空串 = app 目录本身。
type Service struct{ Name, Command, Cwd string }

// Config 是解析后的 box.yaml。
type Config struct {
	Build     string
	Source    bool // 缺省 true
	Artifacts []string
	Include   []string
	Services  []Service // 按文件顺序
}

// Find 从 dir 向上到 repoRoot，返回最近的含配置文件的目录相对 repoRoot 的路径（"" = 根）与配置文件的绝对路径。
func Find(repoRoot, dir string) (root, file string, err error) {
	repoRoot, err = filepath.Abs(repoRoot)
	if err != nil {
		return "", "", err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(repoRoot, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("%s is outside the repository %s", dir, repoRoot)
	}
	for {
		for _, name := range FileNames {
			f := filepath.Join(dir, name)
			if st, err := os.Stat(f); err == nil && st.Mode().IsRegular() {
				r, err := filepath.Rel(repoRoot, dir)
				if err != nil {
					return "", "", err
				}
				if r == "." {
					r = ""
				}
				return filepath.ToSlash(r), f, nil
			}
		}
		if dir == repoRoot {
			return "", "", ErrNotFound
		}
		dir = filepath.Dir(dir)
	}
}

type rawService struct {
	Command string `yaml:"command"`
	Cwd     string `yaml:"cwd"`
}

var knownKeys = []string{"build", "source", "artifacts", "include", "services"}

// Parse 解析并校验 box.yaml；未知键与被忽略的字段作为警告返回，不报错。
func Parse(data []byte) (Config, []string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Config{}, nil, fmt.Errorf("box.yaml: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return Config{}, nil, errors.New("box.yaml: top level must be a mapping")
	}
	top := doc.Content[0]
	cfg := Config{Source: true}
	var warnings []string
	var services *yaml.Node
	for i := 0; i+1 < len(top.Content); i += 2 {
		k, v := top.Content[i].Value, top.Content[i+1]
		var err error
		switch k {
		case "build":
			err = v.Decode(&cfg.Build)
		case "source":
			err = v.Decode(&cfg.Source)
		case "artifacts":
			err = v.Decode(&cfg.Artifacts)
		case "include":
			err = v.Decode(&cfg.Include)
		case "services":
			services = v
		default:
			warnings = append(warnings, fmt.Sprintf("box.yaml: unknown key %q ignored (known: %s)", k, strings.Join(knownKeys, ", ")))
		}
		if err != nil {
			return Config{}, nil, fmt.Errorf("box.yaml: %s: %w", k, err)
		}
	}
	if services == nil || services.Kind != yaml.MappingNode || len(services.Content) == 0 {
		return Config{}, nil, errors.New("box.yaml: services must be a mapping with 1 to 4 services")
	}
	if n := len(services.Content) / 2; n > MaxServices {
		return Config{}, nil, fmt.Errorf("box.yaml: %d services, at most %d", n, MaxServices)
	}
	for i := 0; i+1 < len(services.Content); i += 2 {
		name := services.Content[i].Value
		if !serviceNameRe.MatchString(name) || strings.Contains(name, "--") {
			return Config{}, nil, fmt.Errorf("box.yaml: service name %q must match %s and not contain --", name, serviceNameRe)
		}
		var rs rawService
		if err := services.Content[i+1].Decode(&rs); err != nil {
			return Config{}, nil, fmt.Errorf("box.yaml: services.%s: %w", name, err)
		}
		if strings.TrimSpace(rs.Command) == "" {
			return Config{}, nil, fmt.Errorf("box.yaml: services.%s.command is required", name)
		}
		cwd, err := cleanRel(rs.Cwd, false)
		if err != nil {
			return Config{}, nil, fmt.Errorf("box.yaml: services.%s.cwd: %w", name, err)
		}
		cfg.Services = append(cfg.Services, Service{Name: name, Command: rs.Command, Cwd: cwd})
	}
	for i, a := range cfg.Artifacts {
		c, err := cleanRel(a, false)
		if err != nil || c == "" {
			if err == nil {
				err = errors.New("must not be empty or '.'")
			}
			return Config{}, nil, fmt.Errorf("box.yaml: artifacts[%d] %q: %w", i, a, err)
		}
		cfg.Artifacts[i] = c
	}
	for i, inc := range cfg.Include {
		c, err := cleanRel(inc, true)
		if err != nil || c == "" {
			if err == nil {
				err = errors.New("must not be empty or '.'")
			}
			return Config{}, nil, fmt.Errorf("box.yaml: include[%d] %q: %w", i, inc, err)
		}
		cfg.Include[i] = c
	}
	if cfg.Source {
		if len(cfg.Artifacts) > 0 {
			warnings = append(warnings, "box.yaml: artifacts are ignored without source: false (the release holds the source)")
			cfg.Artifacts = nil
		}
	} else {
		if len(cfg.Artifacts) == 0 {
			return Config{}, nil, errors.New("box.yaml: source: false needs artifacts")
		}
		for _, s := range cfg.Services {
			if s.Cwd != "" && !slices.ContainsFunc(cfg.Artifacts, func(a string) bool { return within(s.Cwd, a) }) {
				return Config{}, nil, fmt.Errorf("box.yaml: services.%s.cwd %q must be one of the artifacts or inside one", s.Name, s.Cwd)
			}
		}
	}
	return cfg, warnings, nil
}

// cleanRel 清洗相对路径：拒绝绝对路径；allowUp 为假时拒绝含 `..` 的路径。"." 与 "" 都返回 ""。
func cleanRel(p string, allowUp bool) (string, error) {
	if p == "" {
		return "", nil
	}
	if path.IsAbs(p) || filepath.IsAbs(p) {
		return "", errors.New("must be a relative path")
	}
	c := path.Clean(filepath.ToSlash(p))
	if !allowUp && (c == ".." || strings.HasPrefix(c, "../") || strings.Contains(c, "/../")) {
		return "", errors.New("must not contain ..")
	}
	if c == "." {
		return "", nil
	}
	return c, nil
}

func within(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

// IncludeDirs 把 include 换算成相对仓库根的目录，并确认它们都在仓库里。
func IncludeDirs(root string, include []string) ([]string, error) {
	out := make([]string, 0, len(include))
	for _, inc := range include {
		p := path.Clean(path.Join(root, inc))
		if p == "." || p == ".." || strings.HasPrefix(p, "../") || path.IsAbs(p) {
			return nil, fmt.Errorf("include %q escapes the repository root", inc)
		}
		out = append(out, p)
	}
	return out, nil
}

// Target 是一个部署目标。
type Target struct {
	On      string `json:"on"`
	Name    string `json:"name"`
	Root    string `json:"root,omitempty"`
	Private bool   `json:"private,omitempty"`
}

// Targets 是 `.oryn/box-deploy.json` 的内容。
type Targets struct {
	Targets map[string]Target `json:"targets"`
}

// LoadTargets 读取仓库根的部署目标文件；不存在 = 空。
func LoadTargets(repoRoot string) (Targets, error) {
	t := Targets{Targets: map[string]Target{}}
	data, err := os.ReadFile(filepath.Join(repoRoot, TargetsFile))
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(data, &t); err != nil {
		return t, fmt.Errorf("%s: %w", TargetsFile, err)
	}
	if t.Targets == nil {
		t.Targets = map[string]Target{}
	}
	return t, nil
}

// SaveTargets 写回部署目标文件：缩进用制表符，键排序（encoding/json 对 map 本就排序），末尾换行。
func SaveTargets(repoRoot string, t Targets) error {
	if t.Targets == nil {
		t.Targets = map[string]Target{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "\t")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(t); err != nil {
		return err
	}
	f := filepath.Join(repoRoot, TargetsFile)
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	return os.WriteFile(f, buf.Bytes(), 0o644)
}

// TargetKey 返回 root 的末段；"" → "default"。
func TargetKey(root string) string {
	if root == "" {
		return "default"
	}
	return path.Base(root)
}

var scpLikeRe = regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`)

// NormalizeRepo 把 origin URL 规范成 `host/path`：主机小写、路径小写、去 `.git`、去凭据与端口，scp 形式转成 host/path。
func NormalizeRepo(originURL string) string {
	s := strings.TrimSpace(originURL)
	var host, p string
	if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.Host != "" {
		host, p = u.Hostname(), u.Path
	} else if m := scpLikeRe.FindStringSubmatch(s); m != nil {
		host, p = m[1], m[2]
	} else {
		p = s
	}
	p = strings.Trim(p, "/")
	p = strings.TrimSuffix(p, ".git")
	p = strings.Trim(p, "/")
	if host == "" {
		return strings.ToLower(p)
	}
	return strings.ToLower(host + "/" + p)
}
