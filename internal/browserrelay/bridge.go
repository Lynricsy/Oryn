package browserrelay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"

	"github.com/Lynricsy/Oryn/internal/orbs"
)

const (
	// maxNDJSONLineBytes 是 orbd ↔ orynd browser_relay 流单行 JSON 上限（与 WS 读上限一致）。
	maxNDJSONLineBytes = 64 << 20
)

// sessionBridge 表示某线程已建立的「扩展连接 ↔ orb browser_relay yamux 流」桥接。
type sessionBridge struct {
	threadID string
	deviceID string
	client   *extClient
	stream   net.Conn

	wmu       sync.Mutex
	closeOnce sync.Once
}

func (b *sessionBridge) writeLine(payload json.RawMessage) error {
	// 压缩去掉可能存在的嵌入换行，保证严格 NDJSON（每行一条完整 JSON）
	var compact bytes.Buffer
	if err := json.Compact(&compact, payload); err != nil {
		return err
	}
	compact.WriteByte('\n')
	b.wmu.Lock()
	defer b.wmu.Unlock()
	_, err := b.stream.Write(compact.Bytes())
	return err
}

func (b *sessionBridge) close() {
	b.closeOnce.Do(func() {
		_ = b.stream.Close()
	})
}

// ensureBridge 在线程已绑定在线扩展且所在 orb 支持 browser-relay 时建立（或保持）流桥接。
func (s *Service) ensureBridge(ctx context.Context, threadID string) {
	if s.orbs == nil {
		return
	}
	row, err := s.st.Q.GetThreadBrowserRelayBinding(ctx, threadID)
	if err != nil || row.DeviceRevokedAt != nil {
		return
	}
	th, err := s.st.Q.GetThread(ctx, threadID)
	if err != nil || th.Kind != "work" || th.OrbID == nil || *th.OrbID == "" {
		return
	}
	orbID := *th.OrbID

	s.mu.Lock()
	client := s.clients[row.DeviceID]
	existing := s.bridges[threadID]
	s.mu.Unlock()
	if client == nil {
		return
	}
	if existing != nil && existing.client == client {
		return
	}
	if !s.orbs.Connected(orbID) || !s.orbs.HasFeature(orbID, orbs.FeatureBrowserRelay) {
		return
	}

	st, err := s.orbs.DialBrowserRelay(ctx, orbID, threadID)
	if err != nil {
		slog.Debug("打开 orb browser_relay 流失败", "orb", orbID, "thread", threadID, "err", err)
		return
	}

	br := &sessionBridge{
		threadID: threadID,
		deviceID: row.DeviceID,
		client:   client,
		stream:   st,
	}

	var replaced *sessionBridge
	s.mu.Lock()
	// 复核客户端仍然在线且仍是同一连接
	if s.clients[row.DeviceID] != client {
		s.mu.Unlock()
		br.close()
		return
	}
	replaced = s.bridges[threadID]
	s.bridges[threadID] = br
	s.mu.Unlock()

	if replaced != nil {
		replaced.close()
	}

	var projName string
	if th.ProjectID != nil {
		if p, err := s.st.Q.GetProject(ctx, *th.ProjectID); err == nil {
			projName = p.Name
		}
	}
	title := strings.TrimSpace(th.Title)
	if title == "" {
		title = threadID
	}
	// 通知扩展为本线程打开会话（扩展随后会发送过滤后的 hello）
	client.send(map[string]any{
		"t":           "session.open",
		"threadId":    threadID,
		"threadTitle": title,
		"projectName": projName,
	})

	go s.pumpOrbToExt(br)
}

func (s *Service) closeBridge(threadID, reason string) {
	s.mu.Lock()
	br := s.bridges[threadID]
	if br != nil {
		delete(s.bridges, threadID)
	}
	delete(s.stats, threadID)
	s.mu.Unlock()
	if br != nil {
		br.close()
		br.client.send(map[string]any{
			"t":        "session.close",
			"threadId": threadID,
			"reason":   reason,
		})
	}
}

func (s *Service) forwardExtFrame(c *extClient, threadID string, payload json.RawMessage) {
	s.mu.Lock()
	br := s.bridges[threadID]
	s.mu.Unlock()
	if br == nil || br.client != c {
		return
	}
	if err := br.writeLine(payload); err != nil {
		slog.Debug("转发扩展帧到 orb 失败", "thread", threadID, "err", err)
		s.dropBridge(br, "orb_stream_closed")
	}
}

func (s *Service) pumpOrbToExt(br *sessionBridge) {
	defer s.dropBridge(br, "orb_stopped")
	r := bufio.NewReaderSize(br.stream, 64<<10)
	for {
		line, err := readNDJSONLine(r, maxNDJSONLineBytes)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				slog.Debug("读取 orb browser_relay 流结束", "thread", br.threadID, "err", err)
			}
			return
		}
		if len(line) == 0 {
			continue
		}
		if !br.client.send(map[string]any{
			"t":        "session.frame",
			"threadId": br.threadID,
			"payload":  json.RawMessage(line),
		}) {
			return
		}
	}
}

func (s *Service) dropBridge(br *sessionBridge, reason string) {
	br.close()
	s.mu.Lock()
	cur := s.bridges[br.threadID]
	removed := cur == br
	if removed {
		delete(s.bridges, br.threadID)
		delete(s.stats, br.threadID)
	}
	s.mu.Unlock()
	if removed {
		br.client.send(map[string]any{
			"t":        "session.close",
			"threadId": br.threadID,
			"reason":   reason,
		})
		s.emitThreadState(s.ctx, br.threadID)
		s.emitClients(s.ctx)
	}
}

// readNDJSONLine 读取一行以 \n 结尾的字节（不含 \r\n），超过 maxBytes 返回错误。
func readNDJSONLine(r *bufio.Reader, maxBytes int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > maxBytes {
			return nil, errors.New("browserrelay: line exceeds limit")
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(buf) > 0 {
				return bytes.TrimSpace(buf), nil
			}
			return nil, err
		}
		return bytes.TrimSpace(buf), nil
	}
}
