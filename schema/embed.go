// Package schema 内嵌 Oryn 的 OpenAPI 规格，供运行时读取（全局助手的 oryn 工具与技能文档由它生成）。
package schema

import _ "embed"

// OpenAPI 是 openapi.yaml 的原文。
//
//go:embed openapi.yaml
var OpenAPI []byte
