package memory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// RelayPathPrefix 是 Hindsight 调用模型、嵌入、重排的中转入口（不在 /api/v1 鉴权组内，凭中转令牌鉴权）。
const RelayPathPrefix = "/memory-relay/v1/"

// 中转入口里 LLM 用途以外的段：嵌入、重排，以及 gemini 协议共用的段。
const (
	RelayEmbed  = "embed"
	RelayRerank = "rerank"
	RelayGemini = "gemini"
)

// rerankEndpoint 是 Cohere 兼容重排的原生路径。Hindsight 的 Cohere 客户端不拼路径，`_RERANKER_COHERE_BASE_URL`
// 必须是完整端点，所以写进 `.env` 时带上它。
const rerankEndpoint = "/v1/rerank"

// 嵌入与重排的别名模型。
const (
	AliasEmbed  = "oryn-embed"
	AliasRerank = "oryn-rerank"
)

// OutputLanguage 是写进 `HINDSIGHT_API_LLM_OUTPUT_LANGUAGE` 的值。Hindsight 把它原样拼进提示词
// 「Respond exclusively in <值>. Translate any source content into <值>.」（hindsight_api/engine/prompt_utils.py
// 的 output_language_directive，步骤 0.2 附录 A 第 8 项），所以用模型最不会误解的英文语言名。
const OutputLanguage = "Simplified Chinese"

// 固定的部署参数。
const (
	tenantExtension = "hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension"
	workerID        = "oryn-1"
	llmConcurrency  = "4"
	workerSlots     = "4"
	// keepValue 是 `.env` 片段里代替密钥的占位文本（密钥只在生成的那一次显示）。
	keepValue = "<保持原值>"
)

// Purposes 是五种用途，顺序固定（`.env`、模型接口都按这个顺序）。
var Purposes = []apigen.MemoryPurpose{
	apigen.MemoryPurposeDefault, apigen.MemoryPurposeRetain, apigen.MemoryPurposeReflect,
	apigen.MemoryPurposeConsolidation, apigen.MemoryPurposeMentalModel,
}

// purposeEnvPrefix 是各用途在 Hindsight 里的 LLM 环境变量前缀。
func purposeEnvPrefix(p apigen.MemoryPurpose) string {
	switch p {
	case apigen.MemoryPurposeRetain:
		return "HINDSIGHT_API_RETAIN_LLM_"
	case apigen.MemoryPurposeReflect:
		return "HINDSIGHT_API_REFLECT_LLM_"
	case apigen.MemoryPurposeConsolidation:
		return "HINDSIGHT_API_CONSOLIDATION_LLM_"
	case apigen.MemoryPurposeMentalModel:
		return "HINDSIGHT_API_MENTAL_MODEL_REFRESH_LLM_"
	}
	return "HINDSIGHT_API_LLM_"
}

// PurposeAlias 是用途写进 `.env` 的别名模型名；中转时替换成管理页选定的真实模型。
func PurposeAlias(p apigen.MemoryPurpose) string {
	return "oryn-" + strings.ReplaceAll(string(p), "_", "-")
}

// PurposeForAlias 由别名模型名认出用途；gemini 的请求只能这样认（它们都走 RelayGemini 段）。
func PurposeForAlias(alias string) (apigen.MemoryPurpose, bool) {
	for _, p := range Purposes {
		if PurposeAlias(p) == alias {
			return p, true
		}
	}
	return "", false
}

// relayURL 是写进 `.env` 的中转地址 `<PublicURL>/memory-relay/v1/<段>`。中转按「段之后的路径 = 该协议的原生路径」接收
// （步骤 0.2 实测）：
//   - LLM 用途段（default、retain、reflect、consolidation、mental_model）：openai 协议拼 `/chat/completions`，
//     部分调用拼 `/responses`；anthropic 协议拼 `/v1/messages`；
//   - gemini：provider 忽略 `*_LLM_BASE_URL`，只认进程级的 `GOOGLE_GEMINI_BASE_URL`，段为 RelayGemini，
//     路径 `/v1beta/models/<别名>:generateContent`，用途按 PurposeForAlias 认；
//   - embed：openai 协议拼 `/embeddings`；
//   - rerank：值本身就是完整端点（段后带 rerankEndpoint）。
func relayURL(publicURL, segment string) string {
	u := publicURL + RelayPathPrefix + segment
	if segment == RelayRerank {
		u += rerankEndpoint
	}
	return u
}

// envLine 是 `.env` 的一行；Key 为空时是注释（Value 为注释文本）。
type envLine struct {
	Key, Value string
	// Secret 为真时，片段里用 keepValue 代替值。
	Secret bool
}

// envInput 是生成 `.env` 的输入。
type envInput struct {
	publicURL    string
	hindsightKey string
	relayToken   string
	families     map[apigen.MemoryPurpose]apigen.MemoryFamily
	embedDims    *int64
}

// purposeLines 是一种用途的四行 LLM 配置。
func purposeLines(in envInput, p apigen.MemoryPurpose) []envLine {
	prefix, fam := purposeEnvPrefix(p), in.families[p]
	if !fam.Valid() {
		fam = apigen.MemoryFamilyOpenai
	}
	return []envLine{
		{Key: prefix + "PROVIDER", Value: string(fam)},
		{Key: prefix + "MODEL", Value: PurposeAlias(p)},
		{Key: prefix + "BASE_URL", Value: relayURL(in.publicURL, string(p))},
		{Key: prefix + "API_KEY", Value: in.relayToken, Secret: true},
	}
}

// envLines 是完整 `.env` 的有序内容；键集由它唯一决定（EnvKeys 与 deploy/hindsight/.env.example 的测试都以它为准）。
func envLines(in envInput) []envLine {
	dims := ""
	if in.embedDims != nil {
		dims = strconv.FormatInt(*in.embedDims, 10)
	}
	lines := []envLine{
		{Value: "鉴权：请求必须带 Authorization: Bearer <API key>（只有 orynd 持有）"},
		{Key: "HINDSIGHT_API_TENANT_EXTENSION", Value: tenantExtension},
		{Key: "HINDSIGHT_API_TENANT_API_KEY", Value: in.hindsightKey, Secret: true},
		{Value: "固定 worker ID（issue #2165）、中文检索、输出语言与并发上限"},
		{Key: "HINDSIGHT_API_WORKER_ID", Value: workerID},
		{Key: "HINDSIGHT_API_TEXT_SEARCH_EXTENSION", Value: "pgroonga"},
		{Key: "HINDSIGHT_API_LLM_OUTPUT_LANGUAGE", Value: OutputLanguage},
		{Key: "HINDSIGHT_API_LLM_MAX_CONCURRENT", Value: llmConcurrency},
		{Key: "HINDSIGHT_API_WORKER_MAX_SLOTS", Value: workerSlots},
		{Value: "模型：每种用途写固定别名，经 orynd 中转替换成管理页选定的真实模型"},
	}
	for _, p := range Purposes {
		lines = append(lines, purposeLines(in, p)...)
	}
	return append(lines,
		envLine{Value: "gemini 协议不读上面的 *_LLM_BASE_URL，只读这一个进程级地址（按别名模型名区分用途）"},
		envLine{Key: "GOOGLE_GEMINI_BASE_URL", Value: relayURL(in.publicURL, RelayGemini)},
		envLine{Value: "嵌入（OpenAI 兼容）与重排（Cohere 兼容）同样经 orynd 中转"},
		envLine{Key: "HINDSIGHT_API_EMBEDDINGS_PROVIDER", Value: "openai"},
		envLine{Key: "HINDSIGHT_API_EMBEDDINGS_OPENAI_BASE_URL", Value: relayURL(in.publicURL, RelayEmbed)},
		envLine{Key: "HINDSIGHT_API_EMBEDDINGS_OPENAI_API_KEY", Value: in.relayToken, Secret: true},
		envLine{Key: "HINDSIGHT_API_EMBEDDINGS_OPENAI_MODEL", Value: AliasEmbed},
		envLine{Key: "HINDSIGHT_API_EMBEDDINGS_OPENAI_DIMENSIONS", Value: dims},
		envLine{Key: "HINDSIGHT_API_RERANKER_PROVIDER", Value: "cohere"},
		envLine{Key: "HINDSIGHT_API_RERANKER_COHERE_BASE_URL", Value: relayURL(in.publicURL, RelayRerank)},
		envLine{Key: "HINDSIGHT_API_RERANKER_COHERE_API_KEY", Value: in.relayToken, Secret: true},
		envLine{Key: "HINDSIGHT_API_RERANKER_COHERE_MODEL", Value: AliasRerank},
	)
}

// EnvKeys 返回生成的 `.env` 的全部键，顺序与文件一致。
func EnvKeys() []string {
	var keys []string
	for _, l := range envLines(envInput{}) {
		if l.Key != "" {
			keys = append(keys, l.Key)
		}
	}
	return keys
}

// renderEnv 把行渲染成文本；redact 为真时密钥写成 keepValue。
func renderEnv(lines []envLine, redact bool) string {
	var b strings.Builder
	for _, l := range lines {
		switch {
		case l.Key == "":
			b.WriteString("\n# " + l.Value + "\n")
		case redact && l.Secret:
			b.WriteString(l.Key + "=" + keepValue + "\n")
		default:
			b.WriteString(l.Key + "=" + l.Value + "\n")
		}
	}
	return strings.TrimPrefix(b.String(), "\n")
}

// HashRelayToken 是中转令牌落库的形式：SHA-256 的十六进制。
func HashRelayToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// VerifyRelayToken 判断中转令牌是否为最近一次生成的那一个；未生成或查询出错时一律拒绝。
func (s *Service) VerifyRelayToken(ctx context.Context, token string) bool {
	if token == "" {
		return false
	}
	st, err := s.loadSettings(ctx)
	if err != nil || st.RelayTokenHash == nil || *st.RelayTokenHash == "" {
		return false
	}
	got := HashRelayToken(token)
	return subtle.ConstantTimeCompare([]byte(got), []byte(*st.RelayTokenHash)) == 1
}

func randomSecret(prefix string) (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buf[:]), nil
}

// families 返回各用途当前选定模型的协议类型：没单独选的用途跟随 default，没选模型或没有目录解析器时按 openai。
func (s *Service) families(ctx context.Context) (map[apigen.MemoryPurpose]apigen.MemoryFamily, error) {
	rows, err := s.cfg.Q.ListMemoryModels(ctx)
	if err != nil {
		return nil, err
	}
	chosen := make(map[apigen.MemoryPurpose]db.MemoryModel, len(rows))
	for _, r := range rows {
		chosen[apigen.MemoryPurpose(r.Purpose)] = r
	}
	out := make(map[apigen.MemoryPurpose]apigen.MemoryFamily, len(Purposes))
	for _, p := range Purposes {
		out[p] = apigen.MemoryFamilyOpenai
		row, ok := chosen[p]
		if !ok {
			row, ok = chosen[apigen.MemoryPurposeDefault]
		}
		if !ok || s.cfg.Family == nil {
			continue
		}
		fam, err := s.cfg.Family(ctx, row.ProviderKey, row.ModelID)
		if err != nil {
			return nil, fmt.Errorf("查询用途 %s 的模型协议：%w", p, err)
		}
		if fam.Valid() {
			out[p] = fam
		}
	}
	return out, nil
}

// embedDimensions 返回已配置的嵌入维度（未配置时为 nil）。
func (s *Service) embedDimensions(ctx context.Context) (*int64, error) {
	eps, err := s.cfg.Q.ListMemoryEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range eps {
		if e.Kind == string(apigen.MemoryEndpointEmbed) {
			return e.Dimensions, nil
		}
	}
	return nil, nil
}

// GenerateEnvFile 轮换 Hindsight API key 与中转令牌（旧值立即失效），返回完整的 `.env`（R3）。
// 两个密钥只出现在这次的返回值里：库里只存 key 的密文与令牌的哈希。写进文件的各用途协议类型记为已部署。
func (s *Service) GenerateEnvFile(ctx context.Context) (apigen.MemoryEnvFile, error) {
	if s.cfg.PublicURL == "" {
		return apigen.MemoryEnvFile{}, &Error{Status: http.StatusConflict, Code: "public_url_missing", Message: "orynd 未配置 public_url，无法生成中转地址"}
	}
	fams, err := s.families(ctx)
	if err != nil {
		return apigen.MemoryEnvFile{}, err
	}
	dims, err := s.embedDimensions(ctx)
	if err != nil {
		return apigen.MemoryEnvFile{}, err
	}
	key, err := randomSecret("hsk_")
	if err != nil {
		return apigen.MemoryEnvFile{}, err
	}
	token, err := randomSecret("omr_")
	if err != nil {
		return apigen.MemoryEnvFile{}, err
	}
	sealed, err := s.cfg.Box.Seal(keyAAD, []byte(key))
	if err != nil {
		return apigen.MemoryEnvFile{}, err
	}
	famJSON, err := json.Marshal(fams)
	if err != nil {
		return apigen.MemoryEnvFile{}, err
	}
	before := s.Enabled(ctx)
	now := s.cfg.Now().Unix()
	if _, err := s.cfg.Q.GetMemorySettings(ctx); errors.Is(err, sql.ErrNoRows) {
		if err := s.cfg.Q.UpsertMemorySettings(ctx, db.UpsertMemorySettingsParams{UpdatedAt: now}); err != nil {
			return apigen.MemoryEnvFile{}, err
		}
	} else if err != nil {
		return apigen.MemoryEnvFile{}, err
	}
	hash := HashRelayToken(token)
	if _, err := s.cfg.Q.SetMemoryKeys(ctx, db.SetMemoryKeysParams{HindsightKeyEnc: sealed, RelayTokenHash: &hash, KeysRotatedAt: &now}); err != nil {
		return apigen.MemoryEnvFile{}, err
	}
	if _, err := s.cfg.Q.SetMemoryDeployedFamilies(ctx, db.SetMemoryDeployedFamiliesParams{DeployedFamilies: string(famJSON), UpdatedAt: now}); err != nil {
		return apigen.MemoryEnvFile{}, err
	}
	s.restartIfChanged(ctx, before, s.Enabled(ctx), nil)
	in := envInput{publicURL: s.cfg.PublicURL, hindsightKey: key, relayToken: token, families: fams, embedDims: dims}
	header := fmt.Sprintf("# Oryn 生成的 Hindsight 配置（deploy/hindsight/.env）。API key 与中转令牌只显示这一次，\n"+
		"# 重新生成会让旧值立即失效。生成时间（Unix 秒）：%d\n\n", now)
	return apigen.MemoryEnvFile{Content: header + renderEnv(envLines(in), false), GeneratedAt: now}, nil
}

// EnvSnippet 返回给定用途按当前选定协议类型应写进 `.env` 的行，密钥写成「<保持原值>」。
// 用于模型协议变化、需要重启 Hindsight 时提示主人改哪几行。
func (s *Service) EnvSnippet(ctx context.Context, purposes []apigen.MemoryPurpose) (string, error) {
	fams, err := s.families(ctx)
	if err != nil {
		return "", err
	}
	in := envInput{publicURL: s.cfg.PublicURL, families: fams}
	var lines []envLine
	for _, p := range Purposes {
		for _, want := range purposes {
			if p == want {
				lines = append(lines, purposeLines(in, p)...)
				break
			}
		}
	}
	return renderEnv(lines, true), nil
}
