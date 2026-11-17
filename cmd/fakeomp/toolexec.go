package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"sync"
	"syscall"
)

// 直接执行工具（M19，oryn-core src/modes/rpc/oryn-tool-exec.ts 同形）：
//
//	oryn_tool_defs{names?}                     → {tools:[{name, description, parameters}]}（未知名字跳过）
//	oryn_tool_call{callId, toolName, arguments} → {result:{content, details?, isError?}}
//	oryn_tool_cancel{callId}                   → {cancelled}
//	帧 {type:"oryn_tool_update", callId, partialResult}
//
// 三个命令即时处理、调用并发执行。工具是真实执行的最小子集：bash（`sh -c`，每读到一段输出发一帧累计的部分结果）、
// read / write（文件）、screenshot（固定的 1×1 PNG 图片块）；其余 computer 工具名只有定义，调用回 isError。
// 未知工具、缺参数、callId 重复、被拒的工具（checkpoint 等）是命令失败。中止时工具“抛错”：isError 结果。

var toolExecDefs = map[string]string{
	"bash": "command", "read": "path", "write": "path", "edit": "path", "grep": "pattern", "glob": "pattern",
	"find": "pattern", "ast_grep": "pattern", "ast_edit": "pattern", "eval": "code", "lsp": "action", "wait": "id",
	"screenshot": "",
}

var toolExecDenied = map[string]bool{
	"checkpoint": true, "rewind": true, "ask": true, "task": true, "todo": true, "yield": true, "goal": true,
}

// onePixelPNG 是 screenshot 返回的图片（base64）。
const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

type toolExec struct {
	mu       sync.Mutex
	inflight map[string]context.CancelFunc
}

func (s *server) handleToolExec(typ, id string, line []byte) bool {
	switch typ {
	case "oryn_tool_defs":
		var c struct {
			Names []string `json:"names"`
		}
		_ = json.Unmarshal(line, &c)
		names := c.Names
		if names == nil {
			for n := range toolExecDefs {
				names = append(names, n)
			}
		}
		tools := []map[string]any{}
		for _, n := range names {
			arg, ok := toolExecDefs[n]
			if !ok {
				continue
			}
			props := map[string]any{}
			required := []string{}
			if arg != "" {
				props[arg] = map[string]any{"type": "string"}
				required = append(required, arg)
			}
			tools = append(tools, map[string]any{
				"name": n, "description": "fakeomp " + n,
				"parameters": map[string]any{"type": "object", "properties": props, "required": required},
			})
		}
		s.ok(id, typ, map[string]any{"tools": tools})
	case "oryn_tool_call":
		var c struct {
			CallID    string         `json:"callId"`
			ToolName  string         `json:"toolName"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(line, &c)
		if c.CallID == "" || c.ToolName == "" {
			s.fail(id, typ, "oryn_tool_call requires a non-empty callId and toolName")
			return true
		}
		if toolExecDenied[c.ToolName] {
			s.fail(id, typ, "Tool "+c.ToolName+" cannot run through oryn_tool_call")
			return true
		}
		arg, ok := toolExecDefs[c.ToolName]
		if !ok {
			s.fail(id, typ, "Unknown tool: "+c.ToolName)
			return true
		}
		if _, has := c.Arguments[arg].(string); arg != "" && !has {
			s.fail(id, typ, "Validation failed for tool \""+c.ToolName+"\": "+arg+" is required")
			return true
		}
		ctx, cancel := context.WithCancel(context.Background())
		s.tools.mu.Lock()
		if s.tools.inflight[c.CallID] != nil {
			s.tools.mu.Unlock()
			cancel()
			s.fail(id, typ, "oryn_tool_call: callId "+c.CallID+" is already running")
			return true
		}
		s.tools.inflight[c.CallID] = cancel
		s.tools.mu.Unlock()
		go func() {
			defer func() {
				s.tools.mu.Lock()
				delete(s.tools.inflight, c.CallID)
				s.tools.mu.Unlock()
				cancel()
			}()
			s.ok(id, typ, map[string]any{"result": s.runTool(ctx, c.CallID, c.ToolName, c.Arguments)})
		}()
	case "oryn_tool_cancel":
		var c struct {
			CallID string `json:"callId"`
		}
		_ = json.Unmarshal(line, &c)
		s.tools.mu.Lock()
		cancel := s.tools.inflight[c.CallID]
		s.tools.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		s.ok(id, typ, map[string]any{"cancelled": cancel != nil})
	default:
		return false
	}
	return true
}

func textResult(text string, isError bool) map[string]any {
	r := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
	if isError {
		r["isError"] = true
	}
	return r
}

func (s *server) runTool(ctx context.Context, callID, name string, args map[string]any) map[string]any {
	str := func(k string) string { v, _ := args[k].(string); return v }
	switch name {
	case "bash":
		return s.runBash(ctx, callID, str("command"))
	case "read":
		b, err := os.ReadFile(str("path"))
		if err != nil {
			return textResult(err.Error(), true)
		}
		return textResult(string(b), false)
	case "write":
		if err := os.WriteFile(str("path"), []byte(str("content")), 0o644); err != nil {
			return textResult(err.Error(), true)
		}
		return textResult("Wrote "+str("path"), false)
	case "screenshot":
		return map[string]any{"content": []map[string]any{
			{"type": "text", "text": "screenshot"},
			{"type": "image", "data": onePixelPNG, "mimeType": "image/png"},
		}}
	default:
		return textResult("fakeomp: "+name+" is not implemented", true)
	}
}

// runBash 用 sh -c 执行命令（独立进程组，中止时杀整组），每读到一段输出发一帧累计的部分结果。
func (s *server) runBash(ctx context.Context, callID, command string) map[string]any {
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return textResult(err.Error(), true)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return textResult(err.Error(), true)
	}
	stop := context.AfterFunc(ctx, func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	defer stop()
	var acc bytes.Buffer
	buf := make([]byte, 4096)
	for {
		n, rerr := out.Read(buf)
		if n > 0 {
			acc.Write(buf[:n])
			s.emit(map[string]any{"type": "oryn_tool_update", "callId": callID, "partialResult": textResult(acc.String(), false)})
		}
		if rerr != nil {
			break
		}
	}
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return textResult("Cancelled by host", true)
	}
	if werr != nil {
		return textResult(acc.String()+"\n"+werr.Error(), true)
	}
	return textResult(acc.String(), false)
}
