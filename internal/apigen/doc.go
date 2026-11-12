// Package apigen 是由 schema/openapi.yaml 经 oapi-codegen 生成的类型与 chi server 接口（ServerInterface + Unimplemented）。
// 不要手改 *.gen.go；修改规格后运行 `make gen`。
package apigen

//go:generate go tool oapi-codegen -config cfg.yaml ../../schema/openapi.yaml
