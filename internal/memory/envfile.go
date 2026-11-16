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
	// rerank 是重排模式：配了重排接口为 cohere，否则为 rrf（v5）；无效值按 rrf。
	rerank apigen.MemoryEndpointsRerankMode
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

// rerankLines 是重排的四行：provider 按模式写；cohere 的三行总是写出（键集不变），rrf 模式下 Hindsight 忽略它们。
func rerankLines(in envInput) []envLine {
	mode := in.rerank
	if !mode.Valid() {
		mode = apigen.MemoryRerankRrf
	}
	return []envLine{
		{Key: "HINDSIGHT_API_RERANKER_PROVIDER", Value: string(mode)},
		{Key: "HINDSIGHT_API_RERANKER_COHERE_BASE_URL", Value: relayURL(in.publicURL, RelayRerank)},
		{Key: "HINDSIGHT_API_RERANKER_COHERE_API_KEY", Value: in.relayToken, Secret: true},
		{Key: "HINDSIGHT_API_RERANKER_COHERE_MODEL", Value: AliasRerank},
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
	lines = append(lines,
		envLine{Value: "gemini 协议不读上面的 *_LLM_BASE_URL，只读这一个进程级地址（按别名模型名区分用途）"},
		envLine{Key: "GOOGLE_GEMINI_BASE_URL", Value: relayURL(in.publicURL, RelayGemini)},
		envLine{Value: "嵌入（OpenAI 兼容）经 orynd 中转"},
		envLine{Key: "HINDSIGHT_API_EMBEDDINGS_PROVIDER", Value: "openai"},
		envLine{Key: "HINDSIGHT_API_EMBEDDINGS_OPENAI_BASE_URL", Value: relayURL(in.publicURL, RelayEmbed)},
		envLine{Key: "HINDSIGHT_API_EMBEDDINGS_OPENAI_API_KEY", Value: in.relayToken, Secret: true},
		envLine{Key: "HINDSIGHT_API_EMBEDDINGS_OPENAI_MODEL", Value: AliasEmbed},
		envLine{Key: "HINDSIGHT_API_EMBEDDINGS_OPENAI_DIMENSIONS", Value: dims},
		envLine{Value: "重排：管理页配了重排接口时为 cohere（Cohere 兼容，经 orynd 中转）；没配时为 rrf" +
			"（Hindsight 自带，只按 RRF 分融合排序、不调用模型，下面三行 cohere 配置被忽略）"},
	)
	return append(lines, rerankLines(in)...)
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

// endpointsEnv 返回 `.env` 里与远端接口相关的值：已配置的嵌入维度（未配置时为 nil）与重排模式。
func (s *Service) endpointsEnv(ctx context.Context) (*int64, apigen.MemoryEndpointsRerankMode, error) {
	eps, err := s.cfg.Q.ListMemoryEndpoints(ctx)
	if err != nil {
		return nil, "", err
	}
	var dims *int64
	for _, e := range eps {
		if e.Kind == string(apigen.MemoryEndpointEmbed) {
			dims = e.Dimensions
		}
	}
	return dims, rerankModeOf(eps), nil
}

// rerankModeOf 由接口配置得出重排模式：有重排接口为 cohere，否则为 rrf（v5）。
func rerankModeOf(eps []db.MemoryEndpoint) apigen.MemoryEndpointsRerankMode {
	for _, e := range eps {
		if e.Kind == string(apigen.MemoryEndpointRerank) {
			return apigen.MemoryRerankCohere
		}
	}
	return apigen.MemoryRerankRrf
}

// deployedRerankKey 是 deployed_families JSON 里记录已部署重排模式的键，与各用途的协议类型存在一起（无需迁移）。
const deployedRerankKey = "rerank"

// deployedJSON 是写进 deployed_families 的内容：各用途的协议类型，加上重排模式。
func deployedJSON(fams map[apigen.MemoryPurpose]apigen.MemoryFamily, rerank apigen.MemoryEndpointsRerankMode) (string, error) {
	out := make(map[string]string, len(fams)+1)
	for p, f := range fams {
		out[string(p)] = string(f)
	}
	out[deployedRerankKey] = string(rerank)
	raw, err := json.Marshal(out)
	return string(raw), err
}

// deployedRerank 返回 `.env` 里已部署的重排模式；generated 为假表示还没生成过 `.env`（谈不上重启）。
// v5 之前的 deployed_families 没有 rerank 键，那时 `.env` 固定写 cohere，按 cohere 处理。
func deployedRerank(st db.MemorySetting) (mode apigen.MemoryEndpointsRerankMode, generated bool, err error) {
	if st.RelayTokenHash == nil || *st.RelayTokenHash == "" {
		return "", false, nil
	}
	var dep map[string]string
	if st.DeployedFamilies != "" {
		if err := json.Unmarshal([]byte(st.DeployedFamilies), &dep); err != nil {
			return "", true, err
		}
	}
	if m := apigen.MemoryEndpointsRerankMode(dep[deployedRerankKey]); m.Valid() {
		return m, true, nil
	}
	return apigen.MemoryRerankCohere, true, nil
}

// pendingRerank 返回待部署的重排模式：生成过 `.env` 且当前模式与已部署的不同时为当前模式，否则为空。
func pendingRerank(st db.MemorySetting, eps []db.MemoryEndpoint) (apigen.MemoryEndpointsRerankMode, error) {
	deployed, generated, err := deployedRerank(st)
	if err != nil || !generated {
		return "", err
	}
	if mode := rerankModeOf(eps); mode != deployed {
		return mode, nil
	}
	return "", nil
}

// GenerateEnvFile 轮换 Hindsight API key 与中转令牌（旧值立即失效），返回完整的 `.env`（R3）。
// 两个密钥只出现在这次的返回值里：库里只存 key 的密文与令牌的哈希。写进文件的各用途协议类型与重排模式记为已部署。
func (s *Service) GenerateEnvFile(ctx context.Context) (apigen.MemoryEnvFile, error) {
	if s.cfg.PublicURL == "" {
		return apigen.MemoryEnvFile{}, &Error{Status: http.StatusConflict, Code: "public_url_missing", Message: "orynd 未配置 public_url，无法生成中转地址"}
	}
	fams, err := s.families(ctx)
	if err != nil {
		return apigen.MemoryEnvFile{}, err
	}
	dims, rerank, err := s.endpointsEnv(ctx)
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
	deployed, err := deployedJSON(fams, rerank)
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
	if _, err := s.cfg.Q.SetMemoryDeployedFamilies(ctx, db.SetMemoryDeployedFamiliesParams{DeployedFamilies: deployed, UpdatedAt: now}); err != nil {
		return apigen.MemoryEnvFile{}, err
	}
	s.restartIfChanged(ctx, before, s.Enabled(ctx), nil)
	in := envInput{publicURL: s.cfg.PublicURL, hindsightKey: key, relayToken: token, families: fams, embedDims: dims, rerank: rerank}
	header := fmt.Sprintf("# Oryn 生成的 Hindsight 配置（deploy/hindsight/.env）。API key 与中转令牌只显示这一次，\n"+
		"# 重新生成会让旧值立即失效。生成时间（Unix 秒）：%d\n\n", now)
	return apigen.MemoryEnvFile{Content: header + renderEnv(envLines(in), false), GeneratedAt: now}, nil
}

// EnvSnippet 返回需要重启 Hindsight 时要改进 `.env` 的行，密钥写成「<保持原值>」：purposes 是协议类型变了的用途，
// 各给出整组 LLM 配置；rerank 非空表示重排模式变了，切到 rrf 只改 provider 一行，切到 cohere 连同三行 cohere 配置。
// 「我已重启」（ConfirmDeployed）把各用途的协议类型与重排模式一起记为已部署，所以模型卡与接口卡都给出这份完整片段，
// 否则只按其中一张卡改了 `.env` 就确认，会把另一部分尚未部署的变化一起标成已部署。
func (s *Service) EnvSnippet(ctx context.Context, purposes []apigen.MemoryPurpose, rerank apigen.MemoryEndpointsRerankMode) (string, error) {
	fams, err := s.families(ctx)
	if err != nil {
		return "", err
	}
	in := envInput{publicURL: s.cfg.PublicURL, families: fams, rerank: rerank}
	var lines []envLine
	for _, p := range Purposes {
		for _, want := range purposes {
			if p == want {
				lines = append(lines, purposeLines(in, p)...)
				break
			}
		}
	}
	if rerank != "" {
		rl := rerankLines(in)
		if rerank != apigen.MemoryRerankCohere {
			rl = rl[:1]
		}
		lines = append(lines, rl...)
	}
	return renderEnv(lines, true), nil
}
