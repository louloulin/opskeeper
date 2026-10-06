package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vincent-wuhan/opskeeper/core/harness/injector"
	hostinjector "github.com/vincent-wuhan/opskeeper/core/harness/injector/host"
	k8sinjector "github.com/vincent-wuhan/opskeeper/core/harness/injector/k8s"
	kafkainjector "github.com/vincent-wuhan/opskeeper/core/harness/injector/mq/kafka"
	rabbitmqinjector "github.com/vincent-wuhan/opskeeper/core/harness/injector/mq/rabbitmq"
	pginjector "github.com/vincent-wuhan/opskeeper/core/harness/injector/pg"
	redisinjector "github.com/vincent-wuhan/opskeeper/core/harness/injector/redis"
)

// 这一组测试守的是一句话：**harness 的故障注入没有接线，所以它不许说"注入成功"。**
//
// 上一版六个注入器的 IsAvailable() 全部 `return true`，Inject 把一条记录写进
// 内存 map 并打上 skeleton 标记，六个测试里还有三个在断言这个假动作
// （"expected skeleton marker"、"generates unique ids"）。一个不碰任何真实
// 系统的注入器，自称可用并返回 InjectID，是这条链上能造出的最贵的假象：
// 它一路走到 judge，变成一个假的回归结论。
//
// 结构性防线是接口本身：CheckAvailable 返回 error 而不是 bool——
// "不可用"这句话终于有了地方可说。

// allRegistered 是生产代码装进注册表的那六个。
func allRegistered() []injector.Injector {
	return newInjectorRegistry().Injectors()
}

func TestEveryRegisteredInjectorIsPresent(t *testing.T) {
	if got := len(allRegistered()); got != 6 {
		t.Fatalf("registered %d injectors, want 6", got)
	}
}

// 每一个注入器都必须自报不可用，并且说清差什么。
func TestNoInjectorClaimsItCanInject(t *testing.T) {
	for _, impl := range allRegistered() {
		err := impl.CheckAvailable(context.Background())
		if err == nil {
			t.Errorf("%s: CheckAvailable returned nil, but this injector touches no real system", impl.Type())
			continue
		}
		if !errors.Is(err, injector.ErrUnavailable) {
			t.Errorf("%s: error = %v, want it to wrap ErrUnavailable", impl.Type(), err)
		}
		if !strings.Contains(err.Error(), "skeleton") {
			t.Errorf("%s: error = %q, want it to say it is a skeleton", impl.Type(), err)
		}
	}
}

// 不可用就必须一步都不走：既不返回结果，也不留下可被 Cleanup 认领的 ID。
func TestNoInjectorProducesAResultWhileUnavailable(t *testing.T) {
	ctx := context.Background()
	// 用注册表里**同一批实例**去比：另建一批再比较身份，六个指针两两不等，
	// 断言会红在一个与被测行为无关的地方——这种失败会让人怀疑被测代码而不是怀疑测试。
	reg := newInjectorRegistry()
	for _, impl := range reg.Injectors() {
		types := supportedTypesOf(t, impl.Type())
		if len(types) == 0 {
			t.Errorf("%s: no supported types listed; the loop below would pass on an empty list", impl.Type())
		}
		for _, typ := range types {
			got, action, ok := reg.Route(typ)
			if !ok || got != impl {
				t.Errorf("%s: type %q did not route back to its own injector", impl.Type(), typ)
				continue
			}
			res, err := impl.Inject(ctx, injector.InjectSpec{Type: typ})
			if !errors.Is(err, injector.ErrUnavailable) {
				t.Errorf("%s %s: error = %v, want ErrUnavailable", impl.Type(), typ, err)
			}
			if res != nil {
				t.Errorf("%s %s: returned a result %+v while refusing", impl.Type(), typ, res)
			}
			if action == "" {
				t.Errorf("%s %s: Route returned an empty action", impl.Type(), typ)
			}
		}
		// 拒绝之后 Cleanup 什么也清不掉：它从来没注入过。
		if err := impl.Cleanup(ctx, ""); !errors.Is(err, injector.ErrInjectionNotFound) {
			t.Errorf("%s: Cleanup(\"\") = %v, want ErrInjectionNotFound", impl.Type(), err)
		}
	}
}

// 一个认不出的类型报"不可用"是错的——那是接线问题，与环境无关。
//
// 六个都要试。只试注册表里的第一个（host）的话，把 pg 的两个检查调换顺序
// 这条断言照样是绿的，而它守的正是"顺序"这件事。
func TestAnUnknownTypeIsReportedAsUnsupportedNotUnavailable(t *testing.T) {
	ctx := context.Background()
	for _, impl := range allRegistered() {
		_, err := impl.Inject(ctx, injector.InjectSpec{Type: impl.Type() + "does_not_exist"})
		if !errors.Is(err, injector.ErrUnsupportedType) {
			t.Errorf("%s: error = %v, want ErrUnsupportedType", impl.Type(), err)
		}
		if errors.Is(err, injector.ErrUnavailable) {
			t.Errorf("%s: an unknown type was reported as unavailable; the caller would go debug the environment", impl.Type())
		}
	}
}

// inject 子命令必须以非零退出，并且不许在输出里出现 "skeleton: true" 之外的成功迹象。
// 上一版它打印一行 "inject: case=... confirm_prod=false" 就返回 0。
func TestInjectFailsLoudlyOnAShippedCase(t *testing.T) {
	err := cmdInject(context.Background(), []string{"--case", "pg/lock-waits", "--cases-dir", shippedCasesDir})
	if err == nil {
		t.Fatal("cmdInject returned nil on a shipped case, want a non-zero exit")
	}
	if !strings.Contains(err.Error(), "not executed") {
		t.Fatalf("error = %q, want it to say the steps were not executed", err)
	}
}

// --dry-run 是这条命令现在唯一能真正完成的事：它读真实的 case 文件、
// 走真实的注册表路由、打印真实的注入类型，不假装注入发生过。
func TestInjectDryRunListsRealStepsAndSucceeds(t *testing.T) {
	if err := cmdInject(context.Background(),
		[]string{"--case", "pg/lock-waits", "--cases-dir", shippedCasesDir, "--dry-run"}); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
}

// prod 必须显式确认，这与注入器是否接线无关。
func TestProdRequiresConfirmation(t *testing.T) {
	err := cmdInject(context.Background(),
		[]string{"--case", "pg/lock-waits", "--cases-dir", shippedCasesDir, "--env", "prod"})
	if err == nil || !strings.Contains(err.Error(), "--confirm-prod") {
		t.Fatalf("error = %v, want prod to be refused without --confirm-prod", err)
	}
}

// 一个不存在的 case 必须报错，而不是当成"没有要注入的东西"然后成功。
func TestAnUnknownCaseIsAnError(t *testing.T) {
	err := cmdInject(context.Background(),
		[]string{"--case", "pg/does-not-exist", "--cases-dir", shippedCasesDir, "--dry-run"})
	if err == nil {
		t.Fatal("an unknown case returned nil, want an error")
	}
}

// supportedTypesOf 从每个注入器包读它自己列出的类型。
// 读不到就是失败：一条"每个类型都被拒绝"的断言在类型集为空时会通过。
func supportedTypesOf(t *testing.T, prefix string) []string {
	t.Helper()
	for impl := range typeIndex() {
		if impl == prefix {
			return typeIndex()[prefix]
		}
	}
	t.Fatalf("no injector with prefix %q", prefix)
	return nil
}

func typeIndex() map[string][]string {
	return map[string][]string{
		"pg.":       pginjectorTypes,
		"redis.":    redisinjectorTypes,
		"host.":     hostinjectorTypes,
		"k8s.":      k8sinjectorTypes,
		"rabbitmq.": rabbitmqinjectorTypes,
		"kafka.":    kafkainjectorTypes,
	}
}

var (
	pginjectorTypes       = mustTypes(pginjector.SupportedTypes)
	redisinjectorTypes    = mustTypes(redisinjector.SupportedTypes)
	hostinjectorTypes     = mustTypes(hostinjector.SupportedTypes)
	k8sinjectorTypes      = mustTypes(k8sinjector.SupportedTypes)
	rabbitmqinjectorTypes = mustTypes(rabbitmqinjector.SupportedTypes)
	kafkainjectorTypes    = mustTypes(kafkainjector.SupportedTypes)
)

func mustTypes(f func() []string) []string { return f() }
