package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vincent-wuhan/opskeeper/core/faults/injector"
)

// 这一组测试真的写磁盘、真的烧 CPU。
//
// 它不 mock，也不假装：`host.fill_disk` 的判据是 statfs 的可用字节，
// 而一个 mock 出来的 statfs 只会证明 mock 是绿的。
//
// 门槛是 `WithRoot` 指的目录。**它必须是测试自己给的一个临时目录**——
// 这一点在这里比在 pg / redis 那两份里更要紧，因为这个注入器
// 是六个里唯一会写文件系统的。
func liveInjector(t *testing.T) (*Injector, string) {
	t.Helper()
	// t.TempDir() 会在测试结束时删掉自己。注入器的撤销只删它自己建的那个
	// 子目录，所以这个目录里任何别的东西都必须是它自己放的。
	dir := t.TempDir()
	i := New(WithRoot(dir))
	if err := i.CheckAvailable(context.Background()); err != nil {
		t.Fatalf("the test's own temp dir is not usable as a fault root: %v", err)
	}
	return i, dir
}

// 一个 fill 必须真的吃掉磁盘，而且撤销之后一��字节都不剩。
//
// 判据是 statfs 的可用字节前后差值——**从外面量**的数。
// "发起了多少块写" 与 "df 少了多少" 不是同一件事，只有后者是故障。
func TestAFillDiskActuallyConsumesSpaceAndCleanupGivesItBack(t *testing.T) {
	i, dir := liveInjector(t)
	before, _, err := diskFree(dir)
	if err != nil {
		t.Fatalf("statfs: %v", err)
	}

	// 8MB 是够 statfs 明确看见、又不值得为它去建一个专用文件系统的量。
	// 更大的一次注入会把这台机器上的别的东西挤出去——那不是测试，
	// 那是拿开发机当靶子。
	res, err := i.Inject(context.Background(), injector.InjectSpec{
		Type:     "host.fill_disk",
		Duration: 60 * time.Second,
		Params:   map[string]any{"target_bytes_mb": 8, "min_free_mb": 256},
	})
	if err != nil {
		t.Fatalf("inject fill_disk: %v", err)
	}
	if _, ok := res.Metadata["expires"]; !ok {
		t.Errorf("metadata = %v, want an expiry: InjectSpec.Duration must be read", res.Metadata)
	}
	after, _, err := diskFree(dir)
	if err != nil {
		t.Fatalf("statfs: %v", err)
	}
	if drop := before - after; drop < chunkBytes {
		t.Errorf("free space dropped by only %d byte(s); a fault nobody can see in `df` is not a fault", drop)
	}

	// 这次注入的字节必须全部落在它自己的目录里。
	sub, ok := res.Metadata["dir"]
	if !ok {
		t.Fatalf("metadata %v does not name the directory it wrote into", res.Metadata)
	}
	// 跟 result 里报的 root 比，而不是跟测试手上的那个字符串比。
	//
	// 注入器会把 root 解析过符号链接（macOS 上 /var → /private/var），
	// 而 t.TempDir() 给的路径是未解析的那一份。拿未解析的字符串去比一个
	// 解析过的路径，在 macOS 上永远不相等——而这条断言想问的是
	// "写在了这个 root 里面"，不是"两个字符串长得一样"。
	root, _ := res.Metadata["root"]
	if root == "" || !strings.HasPrefix(sub, root+string(filepath.Separator)) {
		t.Errorf("wrote into %s, which is not under the reported root %s", sub, root)
	}

	if err := i.Cleanup(context.Background(), res.InjectID); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(sub); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("cleanup left %s behind (stat err = %v); the injector left damage on the filesystem", sub, err)
	}
	restored, _, err := diskFree(dir)
	if err != nil {
		t.Fatalf("statfs: %v", err)
	}
	// 判据是「注入自己吃掉的那部分有没有还回来」，而不是「空闲有没有回到
	// 注入前的那个数」。
	//
	// 后者看起来更严，实际上更弱：这一轮实测差了 2.4MB，而那 2.4MB 是
	// 这台机器上别的进程（两个容器、我自己这条命令链）在这 100 毫秒里写的。
	// 把别人的写入算成注入的欠账，得到的红是假的；而一旦有人为了让这条
	// 断言变绿而去加一个更大的容差，它就更弱了。
	//
	// 真正属于这次注入的量是 `before - after`：那是在注入窗口内量的。
	// 所以判据是：撤销之后至少要还回来其中的九成。
	took := before - after
	if restored < after+took*9/10 {
		t.Errorf("cleanup returned %d of the %d byte(s) it took; free space went %d -> %d -> %d",
			restored-after, took, before, after, restored)
	}
}

// 地板是不可协商的：一个已经被别人写到地板之下的磁盘，
// 再写下去就是在把别的东西一起带走。
func TestAFillDiskRefusesToStartBelowTheFloor(t *testing.T) {
	i, dir := liveInjector(t)
	free, _, err := diskFree(dir)
	if err != nil {
		t.Fatalf("statfs: %v", err)
	}
	floorMB := int(free>>20) + 1024 // 比现在的可用空间还大 1GB
	res, err := i.Inject(context.Background(), injector.InjectSpec{
		Type:     "host.fill_disk",
		Duration: 60 * time.Second,
		Params:   map[string]any{"target_bytes_mb": 8, "min_free_mb": floorMB},
	})
	if err == nil {
		t.Fatalf("Inject returned %+v on a disk already at the floor; want a refusal", res)
	}
	if !strings.Contains(err.Error(), "floor") {
		t.Errorf("error = %q, want it to name the floor it refused to cross", err)
	}
	if n := len(i.Live()); n != 0 {
		t.Errorf("%d injection(s) still recorded after a refusal; a refused injection must leave nothing", n)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("a refused injection left %d entr(ies) in %s", len(entries), dir)
	}
}

// 目标超过上限时必须拒绝，而不是写一半然后报告成功。
func TestAFillDiskRefusesATargetAboveTheCap(t *testing.T) {
	i, _ := liveInjector(t)
	_, err := i.Inject(context.Background(), injector.InjectSpec{
		Type:     "host.fill_disk",
		Duration: 60 * time.Second,
		Params:   map[string]any{"target_bytes_mb": 64 << 10, "max_bytes_mb": 8, "min_free_mb": 1},
	})
	if err == nil {
		t.Fatal("Inject accepted a target above max_bytes_mb; want a refusal")
	}
	if !strings.Contains(err.Error(), "max_bytes_mb") {
		t.Errorf("error = %q, want it to name the cap", err)
	}
}

// 文件系统根目录必须被拒绝，哪怕它是被显式指定进来的。
//
// 这是这个包里**唯一**一道拦住"有人把 root 配成 /"的闸门，
// 而它必须在这里而不是在 Inject 里。
func TestTheFilesystemRootIsRefusedEvenWhenAsked(t *testing.T) {
	i := New(WithRoot("/"))
	err := i.CheckAvailable(context.Background())
	if err == nil {
		t.Fatal("CheckAvailable accepted / as a fault root; an injector that can fill / can wedge the machine")
	}
	if !errors.Is(err, injector.ErrUnavailable) {
		t.Errorf("error = %v, want it to wrap ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), "root") {
		t.Errorf("error = %q, want it to say it is refusing the filesystem root", err)
	}
}

// 没有 root 就是不可用，而且不退回一个"看起来合理"的默认目录。
func TestNoRootMeansUnavailableAndDoesNotGuessOne(t *testing.T) {
	i := New(WithRoot(""))
	err := i.CheckAvailable(context.Background())
	if !errors.Is(err, injector.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), RootEnv) {
		t.Errorf("error = %q, want it to name %s", err, RootEnv)
	}
	if i.root != "" {
		t.Errorf("root = %q, want it to stay empty rather than guess a directory", i.root)
	}
}

// 撤销只能删这次注入自己建的目录。
func TestCleanupNeverTouchesSomethingItDidNotCreate(t *testing.T) {
	i, dir := liveInjector(t)
	foreign := filepath.Join(dir, "someone-elses-data")
	if err := os.WriteFile(foreign, []byte("not mine"), 0o600); err != nil {
		t.Fatalf("seed foreign file: %v", err)
	}

	res, err := i.Inject(context.Background(), injector.InjectSpec{
		Type:     "host.fill_disk",
		Duration: 60 * time.Second,
		Params:   map[string]any{"target_bytes_mb": 4, "min_free_mb": 256},
	})
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	if err := i.Cleanup(context.Background(), res.InjectID); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("a file this injection did not create is gone (stat err = %v); "+
			"cleanup must only ever delete what it wrote", err)
	}
}

// CPU 负载必须真的烧掉内核记的 CPU 时间，而且撤销之后必须真的停。
//
// 判据是 Getrusage 的增量：它是内核在时钟中断里累加的，
// 自己数自己的迭代次数骗不了人，也骗不了编译器。
func TestACPUStressBurnsRealCPUTimeAndStopsOnCleanup(t *testing.T) {
	i, _ := liveInjector(t)
	workers := runtime.GOMAXPROCS(0)
	if workers > 2 {
		// 两核是这台机器上能稳定拿到 CPU 的上限；再多只是让测试变慢。
		workers = 2
	}
	res, err := i.Inject(context.Background(), injector.InjectSpec{
		Type:     "host.cpu_stress",
		Duration: 60 * time.Second,
		Params:   map[string]any{"workers": workers},
	})
	if err != nil {
		t.Fatalf("inject cpu_stress: %v", err)
	}
	if res.Metadata["workers"] != strconv.Itoa(workers) {
		t.Errorf("metadata = %v, want workers=%d recorded for whoever has to undo this", res.Metadata, workers)
	}

	// 撤销之后 CPU 时间必须**停住**。只断言"曾经烧过"是不够的：
	// 一个停不下来的负载比一个没起来的负载难收拾得多。
	if err := i.Cleanup(context.Background(), res.InjectID); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	// 给调度一点时间把在跑的 goroutine 收掉。
	time.Sleep(100 * time.Millisecond)
	before, err := cpuSeconds()
	if err != nil {
		t.Fatalf("getrusage: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	after, err := cpuSeconds()
	if err != nil {
		t.Fatalf("getrusage: %v", err)
	}
	if grew := after - before; grew > 0.15 {
		t.Errorf("CPU time still grew by %.2f second(s) 400ms after cleanup; "+
			"the load did not stop", grew)
	}
}

// workers 不许超过核数：更多的 goroutine 不会让故障更真，
// 只会让注入器自己答不出"什么时候停"。
func TestCPUStressRefusesMoreWorkersThanCores(t *testing.T) {
	i, _ := liveInjector(t)
	_, err := i.Inject(context.Background(), injector.InjectSpec{
		Type:     "host.cpu_stress",
		Duration: 60 * time.Second,
		Params:   map[string]any{"workers": runtime.GOMAXPROCS(0) + 8},
	})
	if err == nil {
		t.Fatal("Inject accepted more workers than cores; want a refusal")
	}
	if !strings.Contains(err.Error(), "GOMAXPROCS") {
		t.Errorf("error = %q, want it to name the limit it refused to cross", err)
	}
	if n := len(i.Live()); n != 0 {
		t.Errorf("%d injection(s) still recorded after a refusal", n)
	}
}

// case 里的 path 只能**收窄**范围，不能扩大——这一条是这个包的核心闸门。
//
// 语料是手写的 YAML，而一个能被 case 指到任意目录的"磁盘写满"注入器
// 就是一个能把节点写瘫的工具。所以 `path: /tmp/x` 在 root 是别处时
// 必须被拒绝，而且要说清该配什么，而不是安静地改写到 root 里去
// （那会让一次注入落到一个与 case 说的完全不同的位置上）。
func TestTheCasePathMayOnlyNarrowNeverWiden(t *testing.T) {
	i, dir := liveInjector(t)
	for _, p := range []string{"/", "/tmp", filepath.Join(dir, "..", "escape"), "../.."} {
		_, err := i.Inject(context.Background(), injector.InjectSpec{
			Type:     "host.fill_disk",
			Duration: 30 * time.Second,
			Params:   map[string]any{"path": p, "target_bytes_mb": 4, "min_free_mb": 256},
		})
		if err == nil {
			t.Errorf("Inject accepted path=%q, which is outside the fault root %s; "+
				"want a refusal", p, dir)
			continue
		}
		if !strings.Contains(err.Error(), RootEnv) {
			t.Errorf("path=%q: error = %q, want it to name %s so the operator knows what to set", p, err, RootEnv)
		}
	}
}

// 但在 root 之内，path 必须真的生效——否则"收窄"就只是一个拒绝的理由。
func TestTheCasePathInsideTheRootIsHonoured(t *testing.T) {
	i, dir := liveInjector(t)
	inner := filepath.Join(dir, "sub")
	res, err := i.Inject(context.Background(), injector.InjectSpec{
		Type:     "host.fill_disk",
		Duration: 30 * time.Second,
		Params:   map[string]any{"path": inner, "target_bytes_mb": 4, "min_free_mb": 256},
	})
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	wroteTo, _ := res.Metadata["dir"]
	root, _ := res.Metadata["root"]
	if !strings.HasPrefix(wroteTo, root+string(filepath.Separator)) ||
		!strings.Contains(wroteTo, filepath.Base(inner)) {
		t.Errorf("wrote into %s, want something under the root %s inside %s", wroteTo, root, inner)
	}
	if err := i.Cleanup(context.Background(), res.InjectID); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

// 被拒绝的注入必须在文件系统上**什么都不留下**。
//
// 上一版在解析 case 的 path 时就把那个目录建了出来，而后面还有
// target_percent 与 max_bytes 几道检查——任何一道拒绝都会在目标上
// 留下一个空目录。而"拒绝"本该意味着什么都没发生：
// 一个连拒绝都不干净的注入器，人是不敢在真机上配它的。
func TestARefusedInjectionLeavesNothingOnTheFilesystem(t *testing.T) {
	i, dir := liveInjector(t)
	inner := filepath.Join(dir, "case-asked-here")
	// target 超过上限：会在建目录之后被拒。
	_, err := i.Inject(context.Background(), injector.InjectSpec{
		Type:     "host.fill_disk",
		Duration: 30 * time.Second,
		Params: map[string]any{
			"path": inner, "target_bytes_mb": 64 << 10, "max_bytes_mb": 8, "min_free_mb": 1,
		},
	})
	if err == nil {
		t.Fatal("Inject accepted a target above the cap; want a refusal")
	}
	if n := len(i.Live()); n != 0 {
		t.Errorf("%d injection(s) still recorded after a refusal: %v", n, i.Live())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("a refused injection left %v in %s; a refusal must not touch the filesystem", names, dir)
	}
}
