package retention

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func revise(t *testing.T, s *Store, revID, archiveID, oldEnd, newEnd, on, reason string) RevisionRecord {
	t.Helper()
	rec, err := s.Revise(ReviseInput{
		RevisionID:  revID,
		ArchiveID:   archiveID,
		OriginalEnd: MustParseDate(oldEnd),
		NewEnd:      MustParseDate(newEnd),
		RevisedOn:   MustParseDate(on),
		Reason:      reason,
	})
	if err != nil {
		t.Fatalf("修订 %s 失败: %v", revID, err)
	}
	return rec
}

func TestReviseBasicAndHistory(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-001", "合同", "2020-01-01", "2025-01-10")

	rec := revise(t, s, "R-1", "A-001", "2025-01-10", "2026-02-20", "2024-12-01", " 业务需要 ")
	if rec.RevisionID != "R-1" || rec.ArchiveID != "A-001" ||
		!rec.OldEnd.Equal(MustParseDate("2025-01-10")) ||
		!rec.NewEnd.Equal(MustParseDate("2026-02-20")) ||
		!rec.RevisedOn.Equal(MustParseDate("2024-12-01")) ||
		rec.Reason != "业务需要" {
		t.Fatalf("返回的修订记录不符: %+v", rec)
	}

	// 再次修订（缩短），基于新的当前截止日。
	revise(t, s, "R-2", "A-001", "2026-02-20", "2025-06-30", "2025-01-05", "重新评估")

	h, found, err := s.History("A-001")
	if err != nil || !found {
		t.Fatalf("核对历史失败: found=%v err=%v", found, err)
	}
	if !h.InitialEnd.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("最初截止日应为登记值，得到 %s", h.InitialEnd)
	}
	if !h.RetentionEnd.Equal(MustParseDate("2025-06-30")) {
		t.Fatalf("当前截止日应为最后一次修订值，得到 %s", h.RetentionEnd)
	}
	if len(h.Revisions) != 2 {
		t.Fatalf("应有两条修订记录，得到 %d", len(h.Revisions))
	}
	first, second := h.Revisions[0], h.Revisions[1]
	if first.RevisionID != "R-1" || !first.OldEnd.Equal(MustParseDate("2025-01-10")) ||
		!first.NewEnd.Equal(MustParseDate("2026-02-20")) || first.Reason != "业务需要" {
		t.Fatalf("第一条修订记录不符: %+v", first)
	}
	if second.RevisionID != "R-2" || !second.OldEnd.Equal(MustParseDate("2026-02-20")) ||
		!second.NewEnd.Equal(MustParseDate("2025-06-30")) {
		t.Fatalf("第二条修订记录不符: %+v", second)
	}
}

func TestReviseValidation(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-001", "合同", "2020-01-01", "2025-01-10")

	valid := ReviseInput{
		RevisionID:  "R-1",
		ArchiveID:   "A-001",
		OriginalEnd: MustParseDate("2025-01-10"),
		NewEnd:      MustParseDate("2026-01-10"),
		RevisedOn:   MustParseDate("2024-12-01"),
		Reason:      "延长",
	}

	// 空白编号与原因（含纯空格）。
	for _, blank := range []string{"", "  ", "\t"} {
		in := valid
		in.RevisionID = blank
		if err := mustReviseErr(s, in); !errors.Is(err, ErrBlankField) {
			t.Fatalf("空白修订编号应失败，得到 %v", err)
		}
		in = valid
		in.Reason = blank
		if err := mustReviseErr(s, in); !errors.Is(err, ErrBlankField) {
			t.Fatalf("空白修订原因应失败，得到 %v", err)
		}
	}

	// 日期缺失或无效。
	in := valid
	in.NewEnd = Date{}
	if err := mustReviseErr(s, in); !errors.Is(err, ErrInvalidDate) {
		t.Fatalf("新截止日缺失应失败，得到 %v", err)
	}
	in = valid
	in.OriginalEnd = Date{}
	if err := mustReviseErr(s, in); !errors.Is(err, ErrInvalidDate) {
		t.Fatalf("原截止日缺失应失败，得到 %v", err)
	}
	in = valid
	in.RevisedOn = Date{}
	if err := mustReviseErr(s, in); !errors.Is(err, ErrInvalidDate) {
		t.Fatalf("修订日期缺失应失败，得到 %v", err)
	}

	// 新截止日等于所提交的原截止日。
	in = valid
	in.NewEnd = in.OriginalEnd
	if err := mustReviseErr(s, in); !errors.Is(err, ErrRevisionEndUnchanged) {
		t.Fatalf("新截止日等于原截止日应失败，得到 %v", err)
	}

	// 新截止日早于起算日（等于起算日合法）。
	in = valid
	in.NewEnd = MustParseDate("2019-12-31")
	if err := mustReviseErr(s, in); !errors.Is(err, ErrRetentionEndBeforeStart) {
		t.Fatalf("新截止日早于起算日应失败，得到 %v", err)
	}
	in = valid
	in.NewEnd = MustParseDate("2020-01-01")
	if _, err := s.Revise(in); err != nil {
		t.Fatalf("新截止日等于起算日应允许，得到 %v", err)
	}

	// 档案不存在。
	in = valid
	in.RevisionID = "R-2"
	in.ArchiveID = "A-404"
	if err := mustReviseErr(s, in); !errors.Is(err, ErrNotFound) {
		t.Fatalf("档案不存在应失败 ErrNotFound，得到 %v", err)
	}

	// 全部失败都不留下修订历史，期限不变。
	h, _, _ := s.History("A-001")
	if len(h.Revisions) != 1 || !h.RetentionEnd.Equal(MustParseDate("2020-01-01")) {
		t.Fatalf("失败不应留下历史或改变期限: %+v", h)
	}
}

func mustReviseErr(s *Store, in ReviseInput) error {
	_, err := s.Revise(in)
	if err == nil {
		return nil
	}
	return err
}

func TestReviseEndChanged(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-001", "合同", "2020-01-01", "2025-01-10")
	revise(t, s, "R-1", "A-001", "2025-01-10", "2026-01-10", "2024-12-01", "延长")

	// 仍按旧截止日提交：期限已变化，错误带当前截止日。
	_, err := s.Revise(ReviseInput{
		RevisionID:  "R-2",
		ArchiveID:   "A-001",
		OriginalEnd: MustParseDate("2025-01-10"),
		NewEnd:      MustParseDate("2027-01-10"),
		RevisedOn:   MustParseDate("2024-12-02"),
		Reason:      "再延长",
	})
	if !errors.Is(err, ErrRetentionEndChanged) {
		t.Fatalf("应返回期限已变化错误，得到 %v", err)
	}
	var changed *RetentionEndChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("错误应为 RetentionEndChangedError，得到 %T", err)
	}
	if !changed.CurrentEnd.Equal(MustParseDate("2026-01-10")) {
		t.Fatalf("错误应带当前截止日 2026-01-10，得到 %s", changed.CurrentEnd)
	}

	// 失败不改变期限、不留历史。
	h, _, _ := s.History("A-001")
	if len(h.Revisions) != 1 || !h.RetentionEnd.Equal(MustParseDate("2026-01-10")) {
		t.Fatalf("失败后状态不应变化: %+v", h)
	}

	// 失败过的编号可重新提交（按当前截止日）。
	revise(t, s, "R-2", "A-001", "2026-01-10", "2027-01-10", "2024-12-02", "再延长")
}

func TestReviseIdempotentReplay(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-001", "合同", "2020-01-01", "2025-01-10")

	in := ReviseInput{
		RevisionID:  " R-1 ",
		ArchiveID:   " A-001 ",
		OriginalEnd: MustParseDate("2025-01-10"),
		NewEnd:      MustParseDate("2026-01-10"),
		RevisedOn:   MustParseDate("2024-12-01"),
		Reason:      " 延长 ",
	}
	first, err := s.Revise(in)
	if err != nil {
		t.Fatalf("首次修订失败: %v", err)
	}

	// 同编号同内容（含首尾空白差异）重试：取回原记录，不增加历史。
	again, err := s.Revise(in)
	if err != nil {
		t.Fatalf("同内容重试应成功: %v", err)
	}
	if again != first {
		t.Fatalf("重试应取回同一条记录: %+v vs %+v", again, first)
	}

	// 后来再次改过期限后，重试仍取回原记录。
	revise(t, s, "R-2", "A-001", "2026-01-10", "2027-01-10", "2025-01-01", "再延长")
	again, err = s.Revise(in)
	if err != nil || again != first {
		t.Fatalf("期限再改后重试仍应取回原记录: %+v err=%v", again, err)
	}

	// 销毁后重试仍能取回原记录。
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2027-01-10"),
		ArchiveIDs:    []string{"A-001"},
	}); err != nil {
		t.Fatalf("销毁失败: %v", err)
	}
	again, err = s.Revise(in)
	if err != nil || again != first {
		t.Fatalf("销毁后重试仍应取回原记录: %+v err=%v", again, err)
	}

	h, _, _ := s.History("A-001")
	if len(h.Revisions) != 2 {
		t.Fatalf("重试不应增加历史，应有 2 条，得到 %d", len(h.Revisions))
	}
}

func TestReviseConflict(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-001", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-002", "合同", "2020-01-01", "2025-01-10")

	base := ReviseInput{
		RevisionID:  "R-1",
		ArchiveID:   "A-001",
		OriginalEnd: MustParseDate("2025-01-10"),
		NewEnd:      MustParseDate("2026-01-10"),
		RevisedOn:   MustParseDate("2024-12-01"),
		Reason:      "延长",
	}
	if _, err := s.Revise(base); err != nil {
		t.Fatalf("首次修订失败: %v", err)
	}

	// 沿用成功编号，逐项改变内容都构成冲突。
	variants := []ReviseInput{
		func() ReviseInput { in := base; in.ArchiveID = "A-002"; return in }(),
		func() ReviseInput { in := base; in.OriginalEnd = MustParseDate("2025-01-11"); return in }(),
		func() ReviseInput { in := base; in.NewEnd = MustParseDate("2026-02-10"); return in }(),
		func() ReviseInput { in := base; in.RevisedOn = MustParseDate("2024-12-02"); return in }(),
		func() ReviseInput { in := base; in.Reason = "其他原因"; return in }(),
	}
	for i, in := range variants {
		if _, err := s.Revise(in); !errors.Is(err, ErrRevisionConflict) {
			t.Fatalf("变体 %d 应返回编号冲突，得到 %v", i, err)
		}
	}

	// 修订编号全库唯一：另一档案也不能复用同一编号办理不同内容。
	in := base
	in.ArchiveID = "A-002"
	if _, err := s.Revise(in); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("跨档案复用编号应冲突，得到 %v", err)
	}
}

func TestReviseAndApplicationIDMutualExclusion(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-001", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-002", "合同", "2020-01-01", "2025-01-10")
	revise(t, s, "R-1", "A-001", "2025-01-10", "2024-06-01", "2024-01-01", "缩短")

	// 销毁申请编号不能占用修订编号。
	_, err := s.Destroy(DestructionRequest{
		ApplicationID: "R-1",
		ProcessedOn:   MustParseDate("2024-06-01"),
		ArchiveIDs:    []string{"A-001"},
	})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("申请编号占用修订编号应失败，得到 %v", err)
	}

	// 修订编号也不能占用已成功的销毁申请编号。
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"A-002"},
	}); err != nil {
		t.Fatalf("销毁失败: %v", err)
	}
	_, err = s.Revise(ReviseInput{
		RevisionID:  "APP-1",
		ArchiveID:   "A-001",
		OriginalEnd: MustParseDate("2024-06-01"),
		NewEnd:      MustParseDate("2024-07-01"),
		RevisedOn:   MustParseDate("2024-01-02"),
		Reason:      "占用申请编号",
	})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("修订编号占用申请编号应失败，得到 %v", err)
	}
}

func TestReviseFrozenArchive(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-001", "合同", "2020-01-01", "2025-01-10")
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-001", FreezeID: "F-1",
		Reason: "诉讼", FrozenOn: MustParseDate("2024-06-01"),
	}); err != nil {
		t.Fatalf("冻结失败: %v", err)
	}

	// 冻结档案允许缩短期限，但修订不解除冻结。
	revise(t, s, "R-1", "A-001", "2025-01-10", "2024-06-02", "2024-06-01", "缩短")
	h, _, _ := s.History("A-001")
	if len(h.ActiveFreezes) != 1 {
		t.Fatalf("修订不应解除冻结: %+v", h.ActiveFreezes)
	}

	// 期限已缩短到到期，但冻结仍阻止销毁。
	_, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2024-06-02"),
		ArchiveIDs:    []string{"A-001"},
	})
	if !errors.Is(err, ErrActiveFreeze) {
		t.Fatalf("冻结应仍阻止销毁，得到 %v", err)
	}

	// 解除冻结后按新期限销毁成功。
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-001", FreezeID: "F-1",
		Reason: "结案", ReleasedOn: MustParseDate("2024-06-02"),
	}); err != nil {
		t.Fatalf("解除冻结失败: %v", err)
	}
	m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2024-06-02"),
		ArchiveIDs:    []string{"A-001"},
	})
	if err != nil {
		t.Fatalf("解除冻结后销毁应成功: %v", err)
	}
	// 清册记录销毁成功时的截止日。
	if !m.Entries[0].End.Equal(MustParseDate("2024-06-02")) {
		t.Fatalf("清册应记录销毁时截止日，得到 %s", m.Entries[0].End)
	}

	// 已销毁档案不能再修订。
	_, err = s.Revise(ReviseInput{
		RevisionID:  "R-2",
		ArchiveID:   "A-001",
		OriginalEnd: MustParseDate("2024-06-02"),
		NewEnd:      MustParseDate("2024-07-01"),
		RevisedOn:   MustParseDate("2024-06-03"),
		Reason:      "销毁后修订",
	})
	if !errors.Is(err, ErrDestroyed) {
		t.Fatalf("已销毁档案修订应失败 ErrDestroyed，得到 %v", err)
	}
}

func TestReviseAffectsExpiry(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-001", "合同", "2020-01-01", "2025-01-10")

	// 延长后，原截止日不再到期。
	revise(t, s, "R-1", "A-001", "2025-01-10", "2025-03-10", "2025-01-01", "延长")
	r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"A-001"},
	})
	if err != nil || r.Status != CheckBlocked {
		t.Fatalf("延长后原截止日应未到期: status=%s err=%v", r.Status, err)
	}

	// 缩短后按新截止日判断，截止日当天到期。
	revise(t, s, "R-2", "A-001", "2025-03-10", "2025-01-05", "2025-01-02", "缩短")
	r, err = s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-05"),
		ArchiveIDs:    []string{"A-001"},
	})
	if err != nil || r.Status != CheckReady {
		t.Fatalf("缩短后新截止日当天应到期: status=%s err=%v", r.Status, err)
	}
}

func TestReviseConcurrent(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(dir)
	if err != nil {
		t.Fatalf("打开保管库失败: %v", err)
	}
	defer s1.Close()
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("打开第二个保管库失败: %v", err)
	}
	defer s2.Close()
	reg(t, s1, "A-001", "合同", "2020-01-01", "2025-01-10")

	// 两个程序同时把同一截止日改成不同日期：只能成功一次。
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, newEnd := range []string{"2026-01-10", "2027-01-10"} {
		wg.Add(1)
		go func(i int, newEnd string) {
			defer wg.Done()
			s := []*Store{s1, s2}[i]
			_, errs[i] = s.Revise(ReviseInput{
				RevisionID:  "R-" + newEnd,
				ArchiveID:   "A-001",
				OriginalEnd: MustParseDate("2025-01-10"),
				NewEnd:      MustParseDate(newEnd),
				RevisedOn:   MustParseDate("2024-12-01"),
				Reason:      "并发修订",
			})
		}(i, newEnd)
	}
	wg.Wait()

	var succeeded, changed int
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrRetentionEndChanged):
			changed++
		default:
			t.Fatalf("并发修订出现意外错误: %v", err)
		}
	}
	if succeeded != 1 || changed != 1 {
		t.Fatalf("应恰好一次成功、一次期限已变化，得到 成功=%d 变化=%d", succeeded, changed)
	}
}

func TestRevisePersistence(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("打开保管库失败: %v", err)
	}
	reg(t, s, "A-001", "合同", "2020-01-01", "2025-01-10")
	first := revise(t, s, "R-1", "A-001", "2025-01-10", "2026-01-10", "2024-12-01", "延长")
	if err := s.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	// 重新打开后：历史完整，同编号重试仍取回原记录。
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	defer s2.Close()
	h, found, err := s2.History("A-001")
	if err != nil || !found {
		t.Fatalf("重开后核对历史失败: found=%v err=%v", found, err)
	}
	if len(h.Revisions) != 1 || !h.InitialEnd.Equal(MustParseDate("2025-01-10")) ||
		!h.RetentionEnd.Equal(MustParseDate("2026-01-10")) {
		t.Fatalf("重开后历史不完整: %+v", h)
	}
	again, err := s2.Revise(ReviseInput{
		RevisionID:  "R-1",
		ArchiveID:   "A-001",
		OriginalEnd: MustParseDate("2025-01-10"),
		NewEnd:      MustParseDate("2026-01-10"),
		RevisedOn:   MustParseDate("2024-12-01"),
		Reason:      "延长",
	})
	if err != nil || again != first {
		t.Fatalf("重开后同编号重试应取回原记录: %+v err=%v", again, err)
	}
}

func TestReviseReturnedRecordIsCopy(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-001", "合同", "2020-01-01", "2025-01-10")
	rec := revise(t, s, "R-1", "A-001", "2025-01-10", "2026-01-10", "2024-12-01", "延长")

	// 修改返回的记录不影响保存内容。
	rec.Reason = "篡改"
	rec.NewEnd = MustParseDate("2030-01-01")
	h, _, _ := s.History("A-001")
	if h.Revisions[0].Reason != "延长" || !h.Revisions[0].NewEnd.Equal(MustParseDate("2026-01-10")) {
		t.Fatalf("修改返回记录不应影响保存内容: %+v", h.Revisions[0])
	}
}

func TestLegacyVaultWithoutRevisions(t *testing.T) {
	dir := t.TempDir()
	// 手工构造引入修订功能之前的状态文件（无 initial_end / revisions 字段）。
	legacy := `{
  "version": 1,
  "archives": {
    "A-001": {
      "id": "A-001",
      "category": "合同",
      "start": "2020-01-01",
      "end": "2025-01-10",
      "destroyed": false,
      "freezes": null
    }
  },
  "manifests": {}
}`
	if err := os.WriteFile(filepath.Join(dir, stateFileName), []byte(legacy), 0o600); err != nil {
		t.Fatalf("写入旧状态文件失败: %v", err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("打开旧保管库失败: %v", err)
	}
	defer s.Close()

	// 登记截止日就是最初截止日，修订历史为空。
	h, found, err := s.History("A-001")
	if err != nil || !found {
		t.Fatalf("核对历史失败: found=%v err=%v", found, err)
	}
	if !h.InitialEnd.Equal(MustParseDate("2025-01-10")) ||
		!h.RetentionEnd.Equal(MustParseDate("2025-01-10")) ||
		len(h.Revisions) != 0 {
		t.Fatalf("旧保管库历史不符: %+v", h)
	}

	// 旧保管库可以继续办理修订。
	revise(t, s, "R-1", "A-001", "2025-01-10", "2026-01-10", "2024-12-01", "延长")
	h, _, _ = s.History("A-001")
	if len(h.Revisions) != 1 || !h.InitialEnd.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("旧保管库修订后历史不符: %+v", h)
	}
}
