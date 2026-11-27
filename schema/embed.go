// Package schema 内嵌 Oryn 的 OpenAPI 规格与视图工具契约，供运行时读取（全局助手的 oryn 工具与技能文档由 OpenAPI 生成；
// render_ui / render_html 的描述、参数与系统提示词指引来自 view-tools.json）。
package schema

import _ "embed"

// OpenAPI 是 openapi.yaml 的原文。
//
//go:embed openapi.yaml
var OpenAPI []byte

// ViewTools 是 view-tools.json 的原文：视图工具（render_ui / render_html）的单一真源。
// 结构为 { "guide": string, "tools": [{ "name", "label", "description", "parameters" }] }；
// 全局助手在 orynd 内解析它，工作线程由 orbd 原样写到 orb 内交给 Core 扩展读取。
//
//go:embed view-tools.json
var ViewTools []byte
