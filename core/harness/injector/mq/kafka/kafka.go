// Package kafka 是 Kafka 故障注入器（路径 A 阶段 2 任务 2.6）。
//
// 3 类 KAFKA 故障（覆盖黄金 case）：
//   - kafka.kill_broker
//   - kafka.inject_consumer_lag
//   - kafka.inject_partition_skew
//
// 当前骨架：接口契约 + 3 个注入方法清单 + 错误处理。
// 完整实现在 Task 2.6 followup PR（confluent-kafka-go + producer flood + 暂停 consumer）。
package kafka

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/vincent-wuhan/opskeeper/core/harness/injector"
)

// Injector 是 Kafka 故障注入器。
type Injector struct {
	mu     sync.Mutex
	active map[string]injector.InjectResult
	// connStr string  // 完整实现：admin DSN
	// adminDB *client // 完整实现：admin 连接
}

// New 创建 kafka Injector。
func New() *Injector {
	return &Injector{active: make(map[string]injector.InjectResult)}
}

// Type 返回 type prefix。
func (i *Injector) Type() string { return "kafka." }

// CheckAvailable 自报不可用。
//
// 骨架不碰任何真实系统，而一个不碰真实系统的注入器报"可用"，
// 会让每一个调用方都以为故障真的注进去了。理由写进错误里，
// 是为了让人知道差什么，而不是只知道一句 false。
func (i *Injector) CheckAvailable(ctx context.Context) error {
	return fmt.Errorf("%w: kafka injector is a skeleton — 没有配置 Kafka broker 连接",
		injector.ErrUnavailable)
}

// Inject 执行故障注入（骨架：占位返回）。
func (i *Injector) Inject(ctx context.Context, spec injector.InjectSpec) (*injector.InjectResult, error) {
	// 不可用就一步都不走。这一条不是防御性编程：骨架的 Inject 之后会
	// 写进 active map 并返回一个看起来很像成功的结果，不在这里拦住，
	// 一次"注入成功"就会一路走到 judge 那里变成一个假的回归结论。
	if !supportedTypes[spec.Type] {
		return nil, fmt.Errorf("%w: %s", injector.ErrUnsupportedType, spec.Type)
	}
	// 不可用就一步都不走。这一条不是防御性编程：骨架的 Inject 之后会
	// 写进 active map 并返回一个看起来很像成功的结果，不在这里拦住，
	// 一次"注入成功"就会一路走到 judge 那里变成一个假的回归结论。
	//
	// 它排在类型检查**之后**：认不出的类型和跑不了的注入器是两回事，
	// 接线之后前者会一直是真的错误，而把它报成"不可用"等于让调用方
	// 去查环境。
	if err := i.CheckAvailable(ctx); err != nil {
		return nil, err
	}
	id := spec.InjectID
	if id == "" {
		id = fmt.Sprintf("kafka-inj-%d", len(i.active)+1)
	}
	res := injector.InjectResult{
		InjectID:  id,
		Type:      spec.Type,
		StartedAt: time.Now(),
		Metadata:  map[string]string{"skeleton": "true", "duration": spec.Duration.String()},
	}
	i.mu.Lock()
	i.active[id] = res
	i.mu.Unlock()
	return &res, nil
}

// supportedTypes 是这个 injector 认识的全部注入类型。
//
// 它原先是 Inject 里一个 switch 的 case 列表。清单从"死代码"变成"数据"之后，
// 调用方能列出它，也能被测试逐条覆盖——上一版那份清单没有任何东西能读它。
var supportedTypes = map[string]bool{
	"kafka.kill_broker":           true,
	"kafka.inject_consumer_lag":   true,
	"kafka.inject_partition_skew": true,
}

// SupportedTypes 返回这个 injector 认识的全部注入类型（已排序）。
//
// 它此前是一个 switch 的 case 列表——一份从不执行、也无法被查询的清单：
// 调用方想知道"这个 case 到底要注入什么"只能去读源码。
func SupportedTypes() []string {
	out := make([]string, 0, len(supportedTypes))
	for typ := range supportedTypes {
		out = append(out, typ)
	}
	sort.Strings(out)
	return out
}

// Cleanup 清理注入（骨架：从 active map 移除，幂等）。
func (i *Injector) Cleanup(ctx context.Context, injectID string) error {
	if injectID == "" {
		return injector.ErrInjectionNotFound
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := i.active[injectID]; !ok {
		return injector.ErrInjectionNotFound
	}
	delete(i.active, injectID)
	return nil
}

var _ injector.Injector = (*Injector)(nil)
