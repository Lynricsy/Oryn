//go:build !linux

package providerrt

import "github.com/Lynricsy/Oryn/internal/apigen"

func (s *hostMetricsSampler) fillPlatformMetrics(_ *apigen.ProviderHostMetrics, _ string) {}
