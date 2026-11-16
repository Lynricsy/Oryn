package artifacts

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"database/sql"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/Lynricsy/Oryn/internal/apigen"
	"github.com/Lynricsy/Oryn/internal/ids"
	"github.com/Lynricsy/Oryn/internal/portalhost"
	"github.com/Lynricsy/Oryn/internal/store/db"
)

// 访问级别。
const (
	AccessPrivate = "private"
	AccessCode    = "code"
	AccessPublic  = "public"
)

// PublishPathPrefix 是主域回退模式下发布页的挂载前缀：发布根是 `<public_url>/p/<slug>/`。
const PublishPathPrefix = "/p/"

const (
	// codeAlphabet 是访问码的字母表（去掉易混的 0 o 1 l i）。
	codeAlphabet = "23456789abcdefghjkmnpqrstuvwxyz"
	// codeLen 是访问码的长度。
	codeLen = 4
	// MaxCodeFailures 是连续输错多少次后锁定访问码（换码才解锁）。
	MaxCodeFailures = 10
	// scanMaxFileBytes 是发布前密钥扫描的单文件上限，更大的文件不扫。
	scanMaxFileBytes = 2 << 20
)

// scanExts 是发布前做密钥扫描的文本文件扩展名。
var scanExts = map[string]bool{
	".html": true, ".htm": true, ".css": true, ".js": true, ".mjs": true, ".json": true,
	".svg": true, ".md": true, ".txt": true, ".csv": true, ".xml": true,
}

// 访问码页（步骤 8.2）用到的结果。
var (
	ErrCodeWrong  = errors.New("artifacts: 访问码不对")
	ErrCodeLocked = errors.New("artifacts: 访问码被输错太多次，已锁定")
)

// ScanError 是发布为 code / public 时密钥扫描有命中且请求没有确认（REST 映射为 409 secrets_found）。
type ScanError struct{ Scan apigen.ShareScan }

func (e *ScanError) Error() string { return "secrets_found: 这个版本里有疑似密钥" }

// PublishConfig 是发布的配置（server 注入）。
type PublishConfig struct {
	// Base 决定发布地址：配置了 portal_domain 时是 `p-<slug>.<portal_domain>`，否则是主域的 /p/<slug>/。
	Base portalhost.Base
	// GrantKey 是授权令牌的 HMAC 密钥（DeriveGrantKey(orb 令牌主密钥)）。
	GrantKey []byte
	// Scan 对一组文件做密钥扫描（share.Service.ScanContent）；nil = 不扫描。
	Scan func(ctx context.Context, files map[string][]byte) apigen.ShareScan
	// Visitors 返回某个发布页当前的访客数（步骤 8.2 的心跳计数）；nil = 0。
	Visitors func(slug string) int
}

// ConfigurePublishing 打开发布功能；须在服务对外提供之前调用。
func (s *Service) ConfigurePublishing(cfg PublishConfig) { s.pub = &cfg }

func (s *Service) publishing() (*PublishConfig, error) {
	if s.pub == nil || len(s.pub.GrantKey) == 0 {
		return nil, errors.New("artifacts: 发布未配置")
	}
	return s.pub, nil
}

// Publication 是一条发布记录。
type Publication struct {
	ID, ArtifactID, Slug, Access string
	// Code 是访问码（没有生成过时为空）。
	Code       string
	CodeGen    int64
	CodeLocked bool
	Version    int
	Follow     bool
	// PendingVersion 是跟随时遇到疑似密钥、等待确认的版本；0 表示没有。
	PendingVersion       int
	CreatedAt, UpdatedAt time.Time
	// URL 是发布根地址；Visitors 是当前访客数。
	URL      string
	Visitors int
}

// API 返回 REST 表示（访问码只在 access = code 时给出）。
func (p Publication) API() apigen.ArtifactPublication {
	out := apigen.ArtifactPublication{
		ID: p.ID, URL: p.URL, Access: apigen.ArtifactAccess(p.Access), CodeLocked: p.CodeLocked,
		Version: p.Version, Follow: p.Follow, Visitors: p.Visitors,
		CreatedAt: p.CreatedAt.UnixMilli(), UpdatedAt: p.UpdatedAt.UnixMilli(),
	}
	if p.Access == AccessCode && p.Code != "" {
		out.Code = &p.Code
	}
	if p.PendingVersion > 0 {
		out.PendingVersion = &p.PendingVersion
	}
	return out
}

// publishRoot 返回发布根地址。
func (c *PublishConfig) publishRoot(slug string) string {
	if c.Base.Enabled() {
		return c.Base.ArtifactURL('p', slug)
	}
	return c.Base.Origin + PublishPathPrefix + slug + "/"
}

func (c *PublishConfig) fromPubRow(r db.ArtifactPublication) Publication {
	p := Publication{
		ID: r.ID, ArtifactID: r.ArtifactID, Slug: r.Slug, Access: r.Access, Code: deref(r.Code), CodeGen: r.CodeGen,
		CodeLocked: r.CodeLocked != 0, Version: int(r.Version), Follow: r.Follow != 0,
		CreatedAt: time.UnixMilli(r.CreatedAt), UpdatedAt: time.UnixMilli(r.UpdatedAt),
		URL: c.publishRoot(r.Slug),
	}
	if r.PendingVersion != nil {
		p.PendingVersion = int(*r.PendingVersion)
	}
	if c.Visitors != nil {
		p.Visitors = c.Visitors(r.Slug)
	}
	return p
}

func validAccess(a string) bool { return a == AccessPrivate || a == AccessCode || a == AccessPublic }

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// newCode 返回一个新访问码（拒绝采样，字母表里每个字符等概率）。
func newCode() string {
	const limit = 256 - 256%len(codeAlphabet)
	out := make([]byte, 0, codeLen)
	var buf [8]byte
	for len(out) < codeLen {
		_, _ = rand.Read(buf[:])
		for _, b := range buf {
			if int(b) < limit && len(out) < codeLen {
				out = append(out, codeAlphabet[int(b)%len(codeAlphabet)])
			}
		}
	}
	return string(out)
}

// pubLock 串行化同一 artifact 的发布记录读改写（与版本写入用不同的键）。
func (s *Service) pubLock(id string) func() { return s.locks.lock("publish:" + id) }

// Publication 返回 artifact 的发布记录；未发布时为 nil。
func (s *Service) Publication(ctx context.Context, id string) (*Publication, error) {
	cfg, err := s.publishing()
	if err != nil {
		return nil, err
	}
	if _, err := getRow(ctx, s.st.Q, id); err != nil {
		return nil, err
	}
	r, err := s.st.Q.GetArtifactPublication(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p := cfg.fromPubRow(r)
	return &p, nil
}

// PublicationBySlug 按 slug 取发布记录（发布页用）；不存在 → not_found。
func (s *Service) PublicationBySlug(ctx context.Context, slug string) (Publication, error) {
	cfg, err := s.publishing()
	if err != nil {
		return Publication{}, err
	}
	r, err := s.st.Q.GetArtifactPublicationBySlug(ctx, slug)
	if errors.Is(err, sql.ErrNoRows) {
		return Publication{}, opErr(CodeNotFound, "这个页面已不在线上。")
	}
	if err != nil {
		return Publication{}, err
	}
	return cfg.fromPubRow(r), nil
}

// PublishedArtifact 是发布列表的一项。
type PublishedArtifact struct {
	Artifact    Artifact
	Publication Publication
}

// Publications 列出全部发布记录（附 artifact），最近更新的在前。
func (s *Service) Publications(ctx context.Context) ([]PublishedArtifact, error) {
	cfg, err := s.publishing()
	if err != nil {
		return nil, err
	}
	rows, err := s.st.Q.ListArtifactPublications(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PublishedArtifact, 0, len(rows))
	for _, r := range rows {
		a := fromRow(r.Artifact)
		a.Published = r.ArtifactPublication.Access
		out = append(out, PublishedArtifact{Artifact: a, Publication: cfg.fromPubRow(r.ArtifactPublication)})
	}
	return out, nil
}

// PublicationUsage 返回全部已发布版本的大小之和。
func (s *Service) PublicationUsage(ctx context.Context) (int64, error) {
	return s.st.Q.ArtifactPublicationUsage(ctx)
}

// PublishParams 是 Publish 的参数；Version / Follow 为 nil 时新建取最新版本 / 跟随，更新时不变。
type PublishParams struct {
	Access              string
	Version             *int
	Follow              *bool
	AcknowledgeFindings bool
}

// Publish 发布 artifact 或更新它的发布记录。发布为 code / public 时，若暴露面变大（新建、换版本或从 private 改来）
// 且没有确认，先对该版本做密钥扫描，有命中回 *ScanError。改访问级别时 code_gen 加一（已发出的授权失效）；
// 改成 code 而还没有访问码时生成一个。回收站里的 artifact 不能新发布，已有的发布记录仍可修改。
func (s *Service) Publish(ctx context.Context, id string, p PublishParams) (Publication, error) {
	cfg, err := s.publishing()
	if err != nil {
		return Publication{}, err
	}
	if !validAccess(p.Access) {
		return Publication{}, opErr(CodeInvalid, "access 必须是 private、code 或 public。")
	}
	defer s.pubLock(id)()
	a, err := getRow(ctx, s.st.Q, id)
	if err != nil {
		return Publication{}, err
	}
	cur, err := s.st.Q.GetArtifactPublication(ctx, id)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Publication{}, err
	}
	if !exists && a.DeletedAt != nil {
		return Publication{}, deletedErr(id)
	}

	version := a.Head
	if exists {
		version = cur.Version
	}
	if p.Version != nil {
		if *p.Version < 1 {
			return Publication{}, opErr(CodeInvalid, "version 必须从 1 开始。")
		}
		if version, err = resolveVersion(a, *p.Version); err != nil {
			return Publication{}, err
		}
	}
	follow := !exists || cur.Follow != 0
	if p.Follow != nil {
		follow = *p.Follow
	}

	exposed := p.Access != AccessPrivate && (!exists || cur.Version != version || cur.Access == AccessPrivate)
	if exposed && !p.AcknowledgeFindings && cfg.Scan != nil {
		files, err := s.scanFiles(ctx, id, int(version))
		if err != nil {
			return Publication{}, err
		}
		if res := cfg.Scan(ctx, files); len(res.Findings) > 0 {
			return Publication{}, &ScanError{Scan: res}
		}
	}

	now := s.now().UnixMilli()
	if !exists {
		ins := db.InsertArtifactPublicationParams{
			ID: ids.ArtifactPublication(), ArtifactID: id, Slug: newToken(), Access: p.Access,
			Version: version, Follow: boolInt(follow), CreatedAt: now, UpdatedAt: now,
		}
		if p.Access == AccessCode {
			c := newCode()
			ins.Code = &c
		}
		if err := s.st.Q.InsertArtifactPublication(ctx, ins); err != nil {
			return Publication{}, err
		}
	} else {
		upd := db.UpdateArtifactPublicationParams{
			ArtifactID: id, Access: p.Access, Code: cur.Code, CodeGen: cur.CodeGen, CodeFailures: cur.CodeFailures,
			CodeLocked: cur.CodeLocked, Version: version, Follow: boolInt(follow), PendingVersion: cur.PendingVersion, UpdatedAt: now,
		}
		if cur.Access != p.Access {
			upd.CodeGen++
		}
		if p.Access == AccessCode && cur.Code == nil {
			c := newCode()
			upd.Code, upd.CodeFailures, upd.CodeLocked = &c, 0, 0
		}
		// 发布到了等待确认的版本（或更新的版本），等待随之结束
		if cur.PendingVersion != nil && version >= *cur.PendingVersion {
			upd.PendingVersion = nil
		}
		if err := s.st.Q.UpdateArtifactPublication(ctx, upd); err != nil {
			return Publication{}, err
		}
	}
	if !exists || cur.Access != p.Access {
		s.changed()
	}
	r, err := s.st.Q.GetArtifactPublication(ctx, id)
	if err != nil {
		return Publication{}, err
	}
	return cfg.fromPubRow(r), nil
}

// scanFiles 读出某个版本里要做密钥扫描的文本文件（按扩展名，单文件不超过 2 MiB）。
func (s *Service) scanFiles(ctx context.Context, id string, version int) (map[string][]byte, error) {
	fs, err := s.Files(ctx, id, version)
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, f := range fs {
		if f.Size > scanMaxFileBytes || !scanExts[strings.ToLower(path.Ext(f.Path))] {
			continue
		}
		b, _, err := s.ReadFile(ctx, id, version, f.Path)
		if err != nil {
			return nil, err
		}
		out[f.Path] = b
	}
	return out, nil
}

// RotateCode 换一个访问码：旧码与已发出的授权随即失效（code_gen 加一），并解除锁定。
func (s *Service) RotateCode(ctx context.Context, id string) (Publication, error) {
	cfg, err := s.publishing()
	if err != nil {
		return Publication{}, err
	}
	defer s.pubLock(id)()
	if _, err := getRow(ctx, s.st.Q, id); err != nil {
		return Publication{}, err
	}
	cur, err := s.st.Q.GetArtifactPublication(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Publication{}, opErr(CodeNotFound, "artifact %s 还没有发布。", id)
	}
	if err != nil {
		return Publication{}, err
	}
	c := newCode()
	upd := db.UpdateArtifactPublicationParams{
		ArtifactID: id, Access: cur.Access, Code: &c, CodeGen: cur.CodeGen + 1, CodeFailures: 0, CodeLocked: 0,
		Version: cur.Version, Follow: cur.Follow, PendingVersion: cur.PendingVersion, UpdatedAt: s.now().UnixMilli(),
	}
	if err := s.st.Q.UpdateArtifactPublication(ctx, upd); err != nil {
		return Publication{}, err
	}
	r, err := s.st.Q.GetArtifactPublication(ctx, id)
	if err != nil {
		return Publication{}, err
	}
	return cfg.fromPubRow(r), nil
}

// Unpublish 取消发布（删掉发布记录，链接随即失效）；没有发布时什么都不做。
func (s *Service) Unpublish(ctx context.Context, id string) error {
	defer s.pubLock(id)()
	if _, err := getRow(ctx, s.st.Q, id); err != nil {
		return err
	}
	n, err := s.st.Q.DeleteArtifactPublication(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		s.changed()
	}
	return nil
}

// DeleteAndUnpublish 把 artifact 移到「最近删除」，并在同一事务里取消发布。
func (s *Service) DeleteAndUnpublish(ctx context.Context, id string) (Artifact, error) {
	defer s.pubLock(id)()
	r, err := getRow(ctx, s.st.Q, id)
	if err != nil {
		return Artifact{}, err
	}
	changed := false
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if r.DeletedAt == nil {
			now := s.now().UnixMilli()
			if _, err := q.SetArtifactDeleted(ctx, db.SetArtifactDeletedParams{ID: id, DeletedAt: &now}); err != nil {
				return err
			}
			changed = true
		}
		n, err := q.DeleteArtifactPublication(ctx, id)
		if n > 0 {
			changed = true
		}
		return err
	})
	if err != nil {
		return Artifact{}, err
	}
	if changed {
		s.changed()
	}
	return s.Get(ctx, id)
}

// grantURL 返回带授权段的发布地址 `<发布根>~<授权>/`。
func (c *PublishConfig) grantURL(slug, grant string) string {
	return c.publishRoot(slug) + "~" + grant + "/"
}

// Grant 给所有者签发一个打开发布页的授权（有效 GrantTTL），返回带授权段的地址。
func (s *Service) Grant(ctx context.Context, slug string) (string, error) {
	cfg, err := s.publishing()
	if err != nil {
		return "", err
	}
	p, err := s.PublicationBySlug(ctx, slug)
	if err != nil {
		return "", err
	}
	return cfg.grantURL(slug, MakeGrant(cfg.GrantKey, slug, p.CodeGen, s.now().Add(GrantTTL))), nil
}

// GrantValid 报告授权对这条发布记录当前是否有效（签名、slug、访问码代数与有效期）。
func (s *Service) GrantValid(p Publication, grant string) bool {
	cfg, err := s.publishing()
	if err != nil {
		return false
	}
	return VerifyGrant(cfg.GrantKey, grant, p.Slug, p.CodeGen, s.now())
}

// UnlockCode 用访问码换授权（访问码页用）。码对：清零失败计数并返回授权；码错：失败计数加一，
// 到 MaxCodeFailures 次锁定，回 ErrCodeWrong 或 ErrCodeLocked；已锁定时直接回 ErrCodeLocked。
func (s *Service) UnlockCode(ctx context.Context, slug, code string) (string, error) {
	cfg, err := s.publishing()
	if err != nil {
		return "", err
	}
	p, err := s.PublicationBySlug(ctx, slug)
	if err != nil {
		return "", err
	}
	// 与换码、改访问级别串行，并让并发的猜测逐个计数
	defer s.pubLock(p.ArtifactID)()
	if p, err = s.PublicationBySlug(ctx, slug); err != nil {
		return "", err
	}
	if p.Access != AccessCode {
		return "", opErr(CodeInvalid, "这个页面不需要访问码。")
	}
	if p.CodeLocked {
		return "", ErrCodeLocked
	}
	code = strings.ToLower(strings.TrimSpace(code))
	if p.Code != "" && hmac.Equal([]byte(code), []byte(p.Code)) {
		if err := s.st.Q.ResetArtifactPublicationCodeFailures(ctx, slug); err != nil {
			return "", err
		}
		return MakeGrant(cfg.GrantKey, slug, p.CodeGen, s.now().Add(GrantTTL)), nil
	}
	r, err := s.st.Q.FailArtifactPublicationCode(ctx, db.FailArtifactPublicationCodeParams{Max: MaxCodeFailures, Slug: slug})
	if err != nil {
		return "", err
	}
	if r.CodeLocked != 0 {
		return "", ErrCodeLocked
	}
	return "", ErrCodeWrong
}
