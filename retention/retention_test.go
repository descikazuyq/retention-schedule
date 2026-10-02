package retention

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// openTestStore 打开一个临时保存位置，测试结束自动关闭。
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestDateParsing(t *testing.T) {
	ok := []string{"2024-01-01", "2024-02-29", "2023-12-31", "2026-07-15"}
	for _, s := range ok {
		d, err := ParseDate(s)
		if err != nil {
			t.Errorf("ParseDate(%q) 意外失败: %v", s, err)
			continue
		}
		if got := d.String(); got != s {
			t.Errorf("ParseDate(%q) 往返后为 %q", s, got)
		}
	}
	bad := []string{
		"", "2024-1-1", "2024/01/01", "2024-02-30", "2024-13-01",
		"2024-00-01", "2024-01-00", "2024-01-32", "2024-02-29-",
		"2023-02-29", "abcd-ef-gh", "2024-01-01x",
	}
	for _, s := range bad {
		if _, err := ParseDate(s); err == nil {
			t.Errorf("ParseDate(%q) 应当失败", s)
		}
	}
}

func TestRegisterAndQuery(t *testing.T) {
	s := openTestStore(t)
	if err := s.Register("A001", "会计档案", "2024-01-01", "2024-12-31"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	rec, err := s.Archive("A001")
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if rec.Number != "A001" || rec.Category != "会计档案" {
		t.Errorf("登记内容不一致: %+v", rec)
	}
	if rec.StartDate.String() != "2024-01-01" || rec.Deadline.String() != "2024-12-31" {
		t.Errorf("日期不一致: %+v", rec)
	}
	if rec.Destroyed || rec.Inventory != "" || len(rec.Freezes) != 0 {
		t.Errorf("新档案不应有销毁或冻结记录: %+v", rec)
	}
}

func TestRegisterValidation(t *testing.T) {
	s := openTestStore(t)
	// 先登记一条合法记录，后续失败不应影响它。
	if err := s.Register("A001", "会计档案", "2024-01-01", "2024-12-31"); err != nil {
		t.Fatalf("Register: %v", err)
	}

	cases := []struct {
		name            string
		number, cat     string
		start, deadline string
		wantErr         error
	}{
		{"编号空白", "   ", "会计档案", "2024-01-01", "2024-12-31", ErrBlankField},
		{"类别空白", "A002", "\t", "2024-01-01", "2024-12-31", ErrBlankField},
		{"起算日格式错", "A002", "会计档案", "2024/01/01", "2024-12-31", ErrInvalidDate},
		{"截止日不存在", "A002", "会计档案", "2024-01-01", "2024-02-30", ErrInvalidDate},
		{"起算日不存在", "A002", "会计档案", "2024-13-01", "2024-12-31", ErrInvalidDate},
		{"截止日早于起算日", "A002", "会计档案", "2024-12-31", "2024-01-01", ErrDeadlineBeforeStart},
		{"重复编号", "A001", "其他类别", "2025-01-01", "2025-12-31", ErrDuplicateNumber},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Register(tc.number, tc.cat, tc.start, tc.deadline)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Register 错误 = %v, 期望 %v", err, tc.wantErr)
			}
		})
	}

	// 已有记录不受影响。
	rec, err := s.Archive("A001")
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if rec.Category != "会计档案" || rec.Deadline.String() != "2024-12-31" {
		t.Errorf("失败的登记影响了已有记录: %+v", rec)
	}
}

func TestArchiveNotFound(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.Archive("NOPE"); !errors.Is(err, ErrArchiveNotFound) {
		t.Errorf("Archive 未登记编号错误 = %v, 期望 ErrArchiveNotFound", err)
	}
}

func TestExpiryBoundary(t *testing.T) {
	s := openTestStore(t)
	if err := s.Register("A001", "会计档案", "2024-01-01", "2024-01-31"); err != nil {
		t.Fatal(err)
	}
	// 提前一天仍不能销毁。
	if _, err := s.Destroy("APP1", "2024-01-30", "A001"); !errors.Is(err, ErrNotExpired) {
		t.Errorf("提前一天销毁错误 = %v, 期望 ErrNotExpired", err)
	}
	// 截止日当天即视为到期。
	inv, err := s.Destroy("APP1", "2024-01-31", "A001")
	if err != nil {
		t.Fatalf("截止日当天销毁: %v", err)
	}
	if inv.ProcessDate.String() != "2024-01-31" || len(inv.Items) != 1 {
		t.Errorf("清册内容不一致: %+v", inv)
	}
}

func TestFreezeLifecycle(t *testing.T) {
	s := openTestStore(t)
	if err := s.Register("A001", "会计档案", "2024-01-01", "2024-12-31"); err != nil {
		t.Fatal(err)
	}

	// 冻结原因/编号空白。
	if err := s.Freeze("A001", "F1", "   ", "2024-06-01"); !errors.Is(err, ErrBlankField) {
		t.Errorf("空白冻结原因错误 = %v, 期望 ErrBlankField", err)
	}
	if err := s.Freeze("A001", "  ", "诉讼", "2024-06-01"); !errors.Is(err, ErrBlankField) {
		t.Errorf("空白冻结编号错误 = %v, 期望 ErrBlankField", err)
	}

	// 正常冻结两条。
	if err := s.Freeze("A001", "F1", "诉讼保全", "2024-06-01"); err != nil {
		t.Fatalf("Freeze F1: %v", err)
	}
	if err := s.Freeze("A001", "F2", "审计要求", "2024-07-01"); err != nil {
		t.Fatalf("Freeze F2: %v", err)
	}
	// 同档案内冻结编号重复。
	if err := s.Freeze("A001", "F1", "重复", "2024-08-01"); !errors.Is(err, ErrDuplicateFreezeNumber) {
		t.Errorf("重复冻结编号错误 = %v, 期望 ErrDuplicateFreezeNumber", err)
	}
	// 不存在的档案不能冻结。
	if err := s.Freeze("NOPE", "F1", "诉讼", "2024-06-01"); !errors.Is(err, ErrArchiveNotFound) {
		t.Errorf("不存在档案冻结错误 = %v, 期望 ErrArchiveNotFound", err)
	}

	// 解除日期不能早于冻结日期。
	if err := s.Unfreeze("A001", "F1", "诉讼结束", "2024-05-31"); !errors.Is(err, ErrUnfreezeBeforeFreeze) {
		t.Errorf("早于冻结日解除错误 = %v, 期望 ErrUnfreezeBeforeFreeze", err)
	}
	// 解除原因空白。
	if err := s.Unfreeze("A001", "F1", "  ", "2024-08-01"); !errors.Is(err, ErrBlankField) {
		t.Errorf("空白解除原因错误 = %v, 期望 ErrBlankField", err)
	}
	// 正常解除 F1。
	if err := s.Unfreeze("A001", "F1", "诉讼结束", "2024-08-01"); err != nil {
		t.Fatalf("Unfreeze F1: %v", err)
	}
	// 不能重复解除。
	if err := s.Unfreeze("A001", "F1", "诉讼结束", "2024-09-01"); !errors.Is(err, ErrAlreadyUnfrozen) {
		t.Errorf("重复解除错误 = %v, 期望 ErrAlreadyUnfrozen", err)
	}
	// 不能解除不存在的冻结。
	if err := s.Unfreeze("A001", "F9", "诉讼结束", "2024-09-01"); !errors.Is(err, ErrFreezeNotFound) {
		t.Errorf("解除不存在冻结错误 = %v, 期望 ErrFreezeNotFound", err)
	}

	// F2 未解除，销毁被阻止。
	if _, err := s.Destroy("APP1", "2024-12-31", "A001"); !errors.Is(err, ErrFrozen) {
		t.Errorf("有未解除冻结时销毁错误 = %v, 期望 ErrFrozen", err)
	}
	// 解除 F2 后可以销毁。
	if err := s.Unfreeze("A001", "F2", "审计结束", "2024-10-01"); err != nil {
		t.Fatalf("Unfreeze F2: %v", err)
	}
	if _, err := s.Destroy("APP1", "2024-12-31", "A001"); err != nil {
		t.Fatalf("全部解除后销毁: %v", err)
	}

	// 销毁后不能再冻结。
	if err := s.Freeze("A001", "F3", "诉讼", "2025-01-01"); !errors.Is(err, ErrArchiveDestroyed) {
		t.Errorf("已销毁档案冻结错误 = %v, 期望 ErrArchiveDestroyed", err)
	}

	// 核对视图：登记内容、全部冻结及解除历史、销毁状态、清册编号。
	rec, err := s.Archive("A001")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Destroyed || rec.Inventory != "APP1" {
		t.Errorf("销毁状态不一致: %+v", rec)
	}
	if len(rec.Freezes) != 2 {
		t.Fatalf("冻结历史条数 = %d, 期望 2", len(rec.Freezes))
	}
	f1 := rec.Freezes[0]
	if f1.Number != "F1" || f1.Reason != "诉讼保全" || f1.Date.String() != "2024-06-01" {
		t.Errorf("F1 冻结记录不一致: %+v", f1)
	}
	if !f1.Unfrozen() || f1.Unfreeze == nil || f1.Unfreeze.Reason != "诉讼结束" || f1.Unfreeze.Date.String() != "2024-08-01" {
		t.Errorf("F1 解除记录不一致: %+v", f1.Unfreeze)
	}
	f2 := rec.Freezes[1]
	if !f2.Unfrozen() || f2.Unfreeze == nil || f2.Unfreeze.Reason != "审计结束" || f2.Unfreeze.Date.String() != "2024-10-01" {
		t.Errorf("F2 解除记录不一致: %+v", f2.Unfreeze)
	}
}

func TestDestroyValidation(t *testing.T) {
	s := openTestStore(t)
	for _, n := range []string{"A001", "A002", "A003"} {
		if err := s.Register(n, "会计档案", "2024-01-01", "2024-12-31"); err != nil {
			t.Fatal(err)
		}
	}

	// 名单为空。
	if _, err := s.Destroy("APP1", "2024-12-31"); !errors.Is(err, ErrEmptyDestroyList) {
		t.Errorf("空名单销毁错误 = %v, 期望 ErrEmptyDestroyList", err)
	}
	// 名单编号重复。
	if _, err := s.Destroy("APP1", "2024-12-31", "A001", "A001"); !errors.Is(err, ErrDuplicateInList) {
		t.Errorf("重复名单销毁错误 = %v, 期望 ErrDuplicateInList", err)
	}
	// 档案不存在。
	if _, err := s.Destroy("APP1", "2024-12-31", "A001", "NOPE"); !errors.Is(err, ErrArchiveNotFound) {
		t.Errorf("不存在档案销毁错误 = %v, 期望 ErrArchiveNotFound", err)
	}
	// 未到期。
	if _, err := s.Destroy("APP1", "2024-12-30", "A001"); !errors.Is(err, ErrNotExpired) {
		t.Errorf("未到期销毁错误 = %v, 期望 ErrNotExpired", err)
	}
	// 有未解除冻结。
	if err := s.Freeze("A002", "F1", "诉讼", "2024-06-01"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy("APP1", "2024-12-31", "A001", "A002"); !errors.Is(err, ErrFrozen) {
		t.Errorf("含冻结档案销毁错误 = %v, 期望 ErrFrozen", err)
	}

	// 整次失败：A001 到期但 A002 有冻结，成功也不能留下部分销毁。
	for _, n := range []string{"A001", "A002", "A003"} {
		rec, err := s.Archive(n)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Destroyed {
			t.Errorf("失败的销毁不应销毁任何档案: %s", n)
		}
	}
	if _, err := s.Inventory("APP1"); !errors.Is(err, ErrInventoryNotFound) {
		t.Errorf("失败的销毁不应留下清册, Inventory 错误 = %v", err)
	}
}

func TestDestroySuccessAndIdempotent(t *testing.T) {
	s := openTestStore(t)
	for _, n := range []string{"A001", "A002"} {
		if err := s.Register(n, "会计档案", "2024-01-01", "2024-12-31"); err != nil {
			t.Fatal(err)
		}
	}

	// 成功销毁两个档案。
	inv, err := s.Destroy("APP1", "2024-12-31", "A002", "A001")
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if inv.ApplicationNumber != "APP1" || inv.ProcessDate.String() != "2024-12-31" || len(inv.Items) != 2 {
		t.Fatalf("清册内容不一致: %+v", inv)
	}
	// 清册快照包含各档案当时的登记内容。
	byNumber := map[string]InventoryItem{}
	for _, it := range inv.Items {
		byNumber[it.Number] = it
	}
	for _, n := range []string{"A001", "A002"} {
		it := byNumber[n]
		if it.Category != "会计档案" || it.StartDate.String() != "2024-01-01" || it.Deadline.String() != "2024-12-31" {
			t.Errorf("清册快照 %s 不一致: %+v", n, it)
		}
	}

	// 两个档案都标记为已销毁，且清册编号可核对。
	for _, n := range []string{"A001", "A002"} {
		rec, err := s.Archive(n)
		if err != nil {
			t.Fatal(err)
		}
		if !rec.Destroyed || rec.Inventory != "APP1" {
			t.Errorf("销毁状态不一致: %+v", rec)
		}
	}

	// 完全相同的申请再次提交（顺序不同），返回原清册。
	again, err := s.Destroy("APP1", "2024-12-31", "A001", "A002")
	if err != nil {
		t.Fatalf("幂等重提: %v", err)
	}
	if again.ApplicationNumber != inv.ApplicationNumber || again.ProcessDate != inv.ProcessDate || len(again.Items) != len(inv.Items) {
		t.Errorf("幂等返回与原清册不一致: %+v vs %+v", again, inv)
	}

	// 沿用编号但改变日期：明确失败。
	if _, err := s.Destroy("APP1", "2024-12-30", "A001", "A002"); !errors.Is(err, ErrApplicationConflict) {
		t.Errorf("改变日期错误 = %v, 期望 ErrApplicationConflict", err)
	}
	// 沿用编号但改变集合：明确失败。
	if _, err := s.Destroy("APP1", "2024-12-31", "A001"); !errors.Is(err, ErrApplicationConflict) {
		t.Errorf("改变集合错误 = %v, 期望 ErrApplicationConflict", err)
	}
	if _, err := s.Destroy("APP1", "2024-12-31", "A001", "A002", "A003"); !errors.Is(err, ErrApplicationConflict) {
		t.Errorf("增加档案错误 = %v, 期望 ErrApplicationConflict", err)
	}

	// 已销毁档案不能再出现在另一份清册中。
	if _, err := s.Destroy("APP2", "2025-12-31", "A001"); !errors.Is(err, ErrArchiveDestroyed) {
		t.Errorf("重复销毁错误 = %v, 期望 ErrArchiveDestroyed", err)
	}
	if _, err := s.Destroy("APP2", "2025-12-31", "A003"); !errors.Is(err, ErrArchiveNotFound) {
		t.Errorf("不存在档案销毁错误 = %v, 期望 ErrArchiveNotFound", err)
	}
}

func TestFailedApplicationCanBeResubmitted(t *testing.T) {
	s := openTestStore(t)
	if err := s.Register("A001", "会计档案", "2024-01-01", "2024-12-31"); err != nil {
		t.Fatal(err)
	}
	// 第一次因未到期失败。
	if _, err := s.Destroy("APP1", "2024-12-30", "A001"); !errors.Is(err, ErrNotExpired) {
		t.Fatalf("第一次销毁错误 = %v", err)
	}
	// 条件改变后用同一编号再次提交，成功。
	if _, err := s.Destroy("APP1", "2024-12-31", "A001"); err != nil {
		t.Fatalf("再次提交: %v", err)
	}
	// 清册可查。
	inv, err := s.Inventory("APP1")
	if err != nil {
		t.Fatal(err)
	}
	if inv.ProcessDate.String() != "2024-12-31" {
		t.Errorf("清册处理日不一致: %+v", inv)
	}
}

func TestReopenPersistsRecords(t *testing.T) {
	dir := t.TempDir()

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Register("A001", "会计档案", "2024-01-01", "2024-12-31"); err != nil {
		t.Fatal(err)
	}
	if err := s.Freeze("A001", "F1", "诉讼保全", "2024-06-01"); err != nil {
		t.Fatal(err)
	}
	if err := s.Freeze("A001", "F2", "审计要求", "2024-07-01"); err != nil {
		t.Fatal(err)
	}
	if err := s.Unfreeze("A001", "F1", "诉讼结束", "2024-08-01"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// 重新打开同一位置，记录完整可查。
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	rec, err := s2.Archive("A001")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Category != "会计档案" || rec.StartDate.String() != "2024-01-01" || rec.Deadline.String() != "2024-12-31" {
		t.Errorf("登记内容丢失或不一致: %+v", rec)
	}
	if len(rec.Freezes) != 2 {
		t.Fatalf("冻结历史条数 = %d, 期望 2", len(rec.Freezes))
	}
	if rec.Freezes[0].Number != "F1" || rec.Freezes[0].Unfreeze == nil ||
		rec.Freezes[0].Unfreeze.Reason != "诉讼结束" || rec.Freezes[0].Unfreeze.Date.String() != "2024-08-01" {
		t.Errorf("F1 解除历史不一致: %+v", rec.Freezes[0])
	}
	if rec.Freezes[1].Number != "F2" || rec.Freezes[1].Unfreeze != nil {
		t.Errorf("F2 应为未解除: %+v", rec.Freezes[1])
	}

	// 继续办理销毁并再次重开，清册仍在。
	if err := s2.Unfreeze("A001", "F2", "审计结束", "2024-10-01"); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Destroy("APP1", "2024-12-31", "A001"); err != nil {
		t.Fatal(err)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}

	s3, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	inv, err := s3.Inventory("APP1")
	if err != nil {
		t.Fatal(err)
	}
	if inv.ApplicationNumber != "APP1" || len(inv.Items) != 1 || inv.Items[0].Number != "A001" {
		t.Errorf("重开后清册不一致: %+v", inv)
	}
	rec, _ = s3.Archive("A001")
	if !rec.Destroyed || rec.Inventory != "APP1" {
		t.Errorf("重开后销毁状态不一致: %+v", rec)
	}
}

func TestInventoryQuery(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.Inventory("NOPE"); !errors.Is(err, ErrInventoryNotFound) {
		t.Errorf("不存在清册错误 = %v, 期望 ErrInventoryNotFound", err)
	}
	if err := s.Register("A001", "会计档案", "2024-01-01", "2024-12-31"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy("APP1", "2024-12-31", "A001"); err != nil {
		t.Fatal(err)
	}
	invs, err := s.Inventories()
	if err != nil {
		t.Fatal(err)
	}
	if len(invs) != 1 || invs[0].ApplicationNumber != "APP1" {
		t.Errorf("清册列表不一致: %+v", invs)
	}
}

func TestInProcessConcurrent(t *testing.T) {
	s := openTestStore(t)
	if err := s.Register("A001", "会计档案", "2024-01-01", "2024-12-31"); err != nil {
		t.Fatal(err)
	}

	// 两个 goroutine 同时办理冻结与销毁，结果必有先后。
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs <- s.Freeze("A001", "F1", "诉讼保全", "2024-06-01")
	}()
	go func() {
		defer wg.Done()
		_, err := s.Destroy("APP1", "2024-12-31", "A001")
		errs <- err
	}()
	wg.Wait()
	close(errs)

	var freezeErr, destroyErr error
	for err := range errs {
		// 按调用顺序区分不了，用错误内容判断。
		if err == nil {
			continue
		}
		if errors.Is(err, ErrFrozen) || errors.Is(err, ErrArchiveDestroyed) {
			destroyErr = err
		} else {
			freezeErr = err
		}
	}
	// 不变量：不允许存在仍有未解除冻结却已销毁的记录。
	rec, err := s.Archive("A001")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Destroyed {
		for _, f := range rec.Freezes {
			if f.Unfreeze == nil {
				t.Fatal("已销毁档案存在未解除冻结")
			}
		}
	}
	// 冻结与销毁不可能同时成功。
	if freezeErr == nil && destroyErr == nil {
		t.Fatal("冻结与销毁不应同时成功")
	}
}

// 以下测试通过子进程模拟两个本机程序同时办理业务。
// 子进程用 RETENTION_WORKER 环境变量标识，见 TestWorker*。

func TestWorkerFreeze(t *testing.T) {
	if os.Getenv("RETENTION_WORKER") == "" {
		t.Skip("非工作进程")
	}
	s, err := Open(os.Getenv("RETENTION_DIR"))
	if err != nil {
		fmt.Println("FREEZE_ERROR:", err)
		return
	}
	defer s.Close()
	if err := s.Freeze("A001", "F1", "诉讼保全", "2024-06-01"); err != nil {
		fmt.Println("FREEZE_FAIL:", err)
		return
	}
	fmt.Println("FREEZE_OK")
}

func TestWorkerDestroy(t *testing.T) {
	if os.Getenv("RETENTION_WORKER") == "" {
		t.Skip("非工作进程")
	}
	s, err := Open(os.Getenv("RETENTION_DIR"))
	if err != nil {
		fmt.Println("DESTROY_ERROR:", err)
		return
	}
	defer s.Close()
	if _, err := s.Destroy("APP1", "2024-12-31", "A001"); err != nil {
		fmt.Println("DESTROY_FAIL:", err)
		return
	}
	fmt.Println("DESTROY_OK")
}

func TestCrossProcessConcurrent(t *testing.T) {
	for round := 0; round < 5; round++ {
		dir := filepath.Join(t.TempDir(), "store")
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Register("A001", "会计档案", "2024-01-01", "2024-12-31"); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}

		freezeCmd := exec.Command(os.Args[0], "-test.run=^TestWorkerFreeze$")
		freezeCmd.Env = append(os.Environ(), "RETENTION_WORKER=1", "RETENTION_DIR="+dir)
		destroyCmd := exec.Command(os.Args[0], "-test.run=^TestWorkerDestroy$")
		destroyCmd.Env = append(os.Environ(), "RETENTION_WORKER=1", "RETENTION_DIR="+dir)

		var freezeOut, destroyOut bytes.Buffer
		freezeCmd.Stdout = &freezeOut
		destroyCmd.Stdout = &destroyOut

		if err := freezeCmd.Start(); err != nil {
			t.Fatal(err)
		}
		if err := destroyCmd.Start(); err != nil {
			t.Fatal(err)
		}
		_ = freezeCmd.Wait()
		_ = destroyCmd.Wait()

		freezeOK := strings.Contains(freezeOut.String(), "FREEZE_OK")
		destroyOK := strings.Contains(destroyOut.String(), "DESTROY_OK")
		if freezeOK == destroyOK {
			t.Fatalf("第 %d 轮：冻结与销毁结果应恰好一个成功，freeze=%q destroy=%q", round, freezeOut.String(), destroyOut.String())
		}

		// 重新打开核对不变量。
		s2, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := s2.Archive("A001")
		if err != nil {
			t.Fatal(err)
		}
		if rec.Destroyed {
			for _, f := range rec.Freezes {
				if f.Unfreeze == nil {
					t.Fatalf("第 %d 轮：已销毁档案存在未解除冻结", round)
				}
			}
		}
		if freezeOK && rec.Destroyed {
			t.Fatalf("第 %d 轮：冻结成功但档案被销毁", round)
		}
		if destroyOK && !rec.Destroyed {
			t.Fatalf("第 %d 轮：销毁成功但档案未标记销毁", round)
		}
		_ = s2.Close()
	}
}
