package collector

import (
	"context"

	"github.com/vincent-wuhan/opskeeper/core/edge/model"
)

// CollectMem samples memory usage from /proc/meminfo.
// Phase 1 returns a zero value.
func CollectMem(ctx context.Context) (model.HostMetric, error) {
	_ = ctx
	return model.HostMetric{}, nil
}
