package retention

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-06")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-2", Reason: "审计", FrozenOn: MustParseDate("2025-01-07")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-2"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// 关闭后重新打开同一位置，已成功办理的记录必须完整可查。
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	h1, found, err := s2.History("A-1")
	if err != nil || !found {
		t.Fatalf("A-1 重开后丢失: %v %v", found, err)
	}
	if h1.Destroyed {
		t.Fatal("A-1 不应处于销毁状态")
	}
	if len(h1.Freezes) != 2 || len(h1.ActiveFreezes) != 1 {
		t.Fatalf("冻结/解除历史不完整: %+v", h1.Freezes)
	}
	f1 := h1.Freezes[0]
	if !f1.Released || f1.ReleaseReason != "结案" || !f1.ReleasedOn.Equal(MustParseDate("2025-01-06")) {
		t.Fatalf("解除信息重开后不正确: %+v", f1)
	}
	if h1.Freezes[1].Released || h1.ActiveFreezes[0].ID != "F-2" {
		t.Fatalf("F-2 应仍未解除: %+v", h1.Freezes)
	}

	h2, found, _ := s2.History("A-2")
	if !found || !h2.Destroyed || h2.ManifestApplicationID != "APP-1" {
		t.Fatalf("A-2 销毁状态重开后不正确: %+v found=%v", h2, found)
	}
	if h2.Manifest == nil || !h2.Manifest.ProcessedOn.Equal(MustParseDate("2025-01-10")) ||
		len(h2.Manifest.Entries) != 1 || h2.Manifest.Entries[0].Category != "凭证" {
		t.Fatalf("清册重开后不完整: %+v", h2.Manifest)
	}
	m, found, err := s2.GetManifest("APP-1")
	if err != nil || !found || m.ApplicationID != "APP-1" {
		t.Fatalf("按申请编号查不到清册: %v %v %+v", found, err, m)
	}

	// 重开后业务仍可继续办理：解除 F-2 后销毁 A-1。
	if err := s2.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "F-2", Reason: "审计结束", ReleasedOn: MustParseDate("2025-01-10")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatalf("重开后应能继续销毁: %v", err)
	}
}

// 同进程内两个指向同一位置的 Store 并发办理，也要有明确先后。
func TestConcurrentStoresInProcess(t *testing.T) {
	dir := t.TempDir()
	setup, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg(t, setup, "A-1", "合同", "2020-01-01", "2025-01-10")
	if err := setup.Close(); err != nil {
		t.Fatal(err)
	}

	for round := 0; round < 30; round++ {
		s1, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		s2, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		freezeID := fmt.Sprintf("F-%d", round)

		var wg sync.WaitGroup
		var freezeErr, destroyErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			freezeErr = s1.Freeze(FreezeInput{
				ArchiveID: "A-1", FreezeID: freezeID, Reason: "诉讼",
				FrozenOn: MustParseDate("2025-01-10"),
			})
		}()
		go func() {
			defer wg.Done()
			_, destroyErr = s2.Destroy(DestructionRequest{
				ApplicationID: fmt.Sprintf("APP-%d", round),
				ProcessedOn:   MustParseDate("2025-01-10"),
				ArchiveIDs:    []string{"A-1"},
			})
		}()
		wg.Wait()

		switch {
		case freezeErr == nil && destroyErr == nil:
			t.Fatalf("第 %d 轮：冻结与销毁同时成功，违反互斥", round)
		case freezeErr != nil && destroyErr != nil:
			// 仅当销毁先成功时，冻结才应因已销毁失败；这一轮销毁不应失败。
			t.Fatalf("第 %d 轮：两者都失败 freeze=%v destroy=%v", round, freezeErr, destroyErr)
		case freezeErr == nil:
			// 冻结先成功：销毁必须被未解除冻结挡住。
			if !errors.Is(destroyErr, ErrActiveFreeze) {
				t.Fatalf("第 %d 轮：冻结先成功却未挡住销毁: %v", round, destroyErr)
			}
			// 解除后下一轮可销毁；为保持后续轮次语义，这里直接解除。
			if err := s1.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: freezeID, Reason: "结案", ReleasedOn: MustParseDate("2025-01-10")}); err != nil {
				t.Fatal(err)
			}
		default:
			// 销毁先成功：冻结必须因档案已销毁失败，后续轮次无需继续。
			if !errors.Is(freezeErr, ErrDestroyed) {
				t.Fatalf("第 %d 轮：销毁先成功，后续冻结应失败: %v", round, freezeErr)
			}
			h, found, _ := s1.History("A-1")
			if !found || !h.Destroyed || len(h.ActiveFreezes) != 0 {
				t.Fatalf("第 %d 轮：销毁成功后状态不正确: %+v", round, h)
			}
			s1.Close()
			s2.Close()
			return
		}
		s1.Close()
		s2.Close()
	}
}

// --- 跨进程并发：两个独立的本机程序同时办理同一档案 ---

// TestSubprocessHelper 不是普通测试，只在被父进程以辅助进程方式启动时执行单次操作。
func TestSubprocessHelper(t *testing.T) {
	dir := os.Getenv("RETENTION_HELPER_DIR")
	if dir == "" {
		return
	}
	op := os.Getenv("RETENTION_HELPER_OP")
	s, err := Open(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open: %v\n", err)
		os.Exit(2)
	}
	switch op {
	case "freeze":
		err = s.Freeze(FreezeInput{
			ArchiveID: "A-1", FreezeID: os.Getenv("RETENTION_HELPER_FREEZE_ID"),
			Reason: "诉讼", FrozenOn: MustParseDate("2025-01-10"),
		})
	case "destroy":
		_, err = s.Destroy(DestructionRequest{
			ApplicationID: "APP-X", ProcessedOn: MustParseDate("2025-01-10"),
			ArchiveIDs: []string{"A-1"},
		})
	default:
		fmt.Fprintf(os.Stderr, "unknown op %q\n", op)
		os.Exit(2)
	}
	if err != nil {
		// 业务失败以退出码 3 回传，最后一行给出错误哨兵，供父进程分类。
		fmt.Printf("ERROR: %v\n", err)
		os.Exit(3)
	}
}

type helperResult struct {
	op      string
	failed  bool
	message string
}

func runHelper(t *testing.T, dir, op, freezeID string) helperResult {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestSubprocessHelper", "-test.v")
	cmd.Env = append(os.Environ(),
		"RETENTION_HELPER_DIR="+dir,
		"RETENTION_HELPER_OP="+op,
		"RETENTION_HELPER_FREEZE_ID="+freezeID,
	)
	out, _ := cmd.CombinedOutput()
	r := helperResult{op: op, failed: !cmd.ProcessState.Success()}
	last := ""
	for _, line := range splitLines(string(out)) {
		if line == "PASS" || line == "FAIL" || line == "SKIP" {
			continue
		}
		last = line
	}
	r.message = last
	return r
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func TestCrossProcessFreezeVsDestroy(t *testing.T) {
	for round := 0; round < 10; round++ {
		dir := filepath.Join(t.TempDir(), "vault")
		setup, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		reg(t, setup, "A-1", "合同", "2020-01-01", "2025-01-10")
		if err := setup.Close(); err != nil {
			t.Fatal(err)
		}

		freezeID := fmt.Sprintf("F-%d", round)
		var wg sync.WaitGroup
		results := make([]helperResult, 2)
		wg.Add(2)
		go func() { defer wg.Done(); results[0] = runHelper(t, dir, "freeze", freezeID) }()
		go func() { defer wg.Done(); results[1] = runHelper(t, dir, "destroy", freezeID) }()
		wg.Wait()

		var freezeRes, destroyRes helperResult
		for _, r := range results {
			if r.op == "freeze" {
				freezeRes = r
			} else {
				destroyRes = r
			}
		}

		check, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		h, found, _ := check.History("A-1")
		if !found {
			t.Fatalf("第 %d 轮：档案丢失", round)
		}
		// 核心不变量：绝不能既已销毁又存在未解除冻结。
		if h.Destroyed && len(h.ActiveFreezes) != 0 {
			t.Fatalf("第 %d 轮：出现已销毁但仍有未解除冻结的记录: %+v", round, h)
		}

		switch {
		case !freezeRes.failed && !destroyRes.failed:
			t.Fatalf("第 %d 轮：两个程序同时成功", round)
		case !freezeRes.failed:
			t.Logf("第 %d 轮：冻结先成功", round)
			// 冻结先成功，销毁必须明确失败。
			if !destroyRes.failed || !contains(destroyRes.message, "冻结") {
				t.Fatalf("第 %d 轮：冻结先成功却未挡住销毁: %+v", round, destroyRes)
			}
			if h.Destroyed || len(h.ActiveFreezes) != 1 {
				t.Fatalf("第 %d 轮：冻结成功后状态错误: %+v", round, h)
			}
		default:
			t.Logf("第 %d 轮：销毁先成功", round)
			// 销毁先成功，后续冻结必须明确失败。
			if freezeRes.failed != true || !contains(freezeRes.message, "销毁") {
				t.Fatalf("第 %d 轮：销毁先成功却未阻止冻结: %+v", round, freezeRes)
			}
			if !h.Destroyed {
				t.Fatalf("第 %d 轮：销毁结果未持久化", round)
			}
		}
		check.Close()
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
