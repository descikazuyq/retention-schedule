package retention

import (
	"errors"
	"sync"
	"testing"
)

func revInput(id, archiveID, orig, new, revisedOn, reason string) RevisionInput {
	return RevisionInput{
		RevisionID:  id,
		ArchiveID:   archiveID,
		OriginalEnd: MustParseDate(orig),
		NewEnd:      MustParseDate(new),
		RevisedOn:   MustParseDate(revisedOn),
		Reason:      reason,
	}
}

func TestReviseBasicExtendAndShorten(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	// 延长截止日。
	r1, err := s.Revise(revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "补充凭证"))
	if err != nil {
		t.Fatalf("延长截止日失败: %v", err)
	}
	if r1.RevisionID != "R-1" || r1.ArchiveID != "A-1" ||
		!r1.OriginalEnd.Equal(MustParseDate("2025-01-10")) ||
		!r1.NewEnd.Equal(MustParseDate("2025-02-10")) ||
		!r1.RevisedOn.Equal(MustParseDate("2025-01-15")) || r1.Reason != "补充凭证" {
		t.Fatalf("延长修订记录内容不正确: %+v", r1)
	}

	// 缩短截止日。
	r2, err := s.Revise(revInput("R-2", "A-1", "2025-02-10", "2025-01-20", "2025-01-16", "提前结清"))
	if err != nil {
		t.Fatalf("缩短截止日失败: %v", err)
	}
	if !r2.OriginalEnd.Equal(MustParseDate("2025-02-10")) || !r2.NewEnd.Equal(MustParseDate("2025-01-20")) {
		t.Fatalf("缩短修订记录内容不正确: %+v", r2)
	}

	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("核对失败: %v %v", found, err)
	}
	// 最初截止日不变，当前截止日为最新值。
	if !h.RegisteredEnd.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("最初登记截止日不应变化，得到 %s", h.RegisteredEnd)
	}
	if !h.RetentionEnd.Equal(MustParseDate("2025-01-20")) {
		t.Fatalf("当前截止日应为最新值，得到 %s", h.RetentionEnd)
	}
	if len(h.Revisions) != 2 {
		t.Fatalf("应有 2 条修订历史，得到 %d", len(h.Revisions))
	}
	if h.Revisions[0].RevisionID != "R-1" || h.Revisions[1].RevisionID != "R-2" {
		t.Fatalf("修订历史应按成功办理顺序排列: %+v", h.Revisions)
	}
}

func TestReviseValidation(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	// 空白修订编号 / 档案编号 / 原因。
	cases := []struct {
		name string
		in   RevisionInput
		want error
	}{
		{"空白修订编号", revInput("  ", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "r"), ErrBlankField},
		{"空白档案编号", revInput("R-1", "  ", "2025-01-10", "2025-02-10", "2025-01-15", "r"), ErrBlankField},
		{"空白原因", revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "  "), ErrBlankField},
		{"缺失修订日期", func() RevisionInput { in := revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "r"); in.RevisedOn = Date{}; return in }(), ErrInvalidDate},
		{"缺失原截止日", func() RevisionInput { in := revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "r"); in.OriginalEnd = Date{}; return in }(), ErrInvalidDate},
		{"缺失新截止日", func() RevisionInput { in := revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "r"); in.NewEnd = Date{}; return in }(), ErrInvalidDate},
		{"新截止日早于起算日", revInput("R-1", "A-1", "2025-01-10", "2019-12-31", "2025-01-15", "r"), ErrRetentionEndBeforeStart},
		{"新截止日等于原截止日", revInput("R-1", "A-1", "2025-01-10", "2025-01-10", "2025-01-15", "r"), ErrRevisionEndUnchanged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Revise(tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("应失败 %v，得到 %v", tc.want, err)
			}
		})
	}

	// 失败后期限与修订历史不变。
	h, found, _ := s.History("A-1")
	if !found || !h.RetentionEnd.Equal(MustParseDate("2025-01-10")) || len(h.Revisions) != 0 {
		t.Fatalf("失败后期限或修订历史不应改变: %+v", h)
	}
}

func TestReviseNotFoundAndDestroyed(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	// 档案不存在。
	if _, err := s.Revise(revInput("R-1", "NOPE", "2025-01-10", "2025-02-10", "2025-01-15", "r")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("修订不存在档案应失败，得到 %v", err)
	}

	// 销毁后不能修订。
	if _, err := s.Destroy(DestructionRequest{ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revise(revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "r")); !errors.Is(err, ErrDestroyed) {
		t.Fatalf("修订已销毁档案应失败，得到 %v", err)
	}
}

func TestReviseOriginalEndMismatch(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	// 他人先改过期限。
	if _, err := s.Revise(revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "先到")); err != nil {
		t.Fatal(err)
	}

	// 调用者仍持旧截止日提交：返回可区分的期限已变化错误，并提供当前截止日。
	_, err := s.Revise(revInput("R-2", "A-1", "2025-01-10", "2025-03-10", "2025-01-16", "后到"))
	if !errors.Is(err, ErrRevisionEndMismatch) {
		t.Fatalf("原截止日不一致应返回 ErrRevisionEndMismatch，得到 %v", err)
	}
	var mismatch *RevisionEndMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("应能取出 RevisionEndMismatchError，得到 %T", err)
	}
	if !mismatch.Current.Equal(MustParseDate("2025-02-10")) || !mismatch.Submitted.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("期限已变化错误应提供当前截止日，得到 %+v", mismatch)
	}

	// 失败不改变期限，也不留下 R-2 的历史。
	h, _, _ := s.History("A-1")
	if !h.RetentionEnd.Equal(MustParseDate("2025-02-10")) || len(h.Revisions) != 1 {
		t.Fatalf("失败后期限或历史不应改变: %+v", h)
	}

	// 用当前截止日重新提交可以成功。
	if _, err := s.Revise(revInput("R-2", "A-1", "2025-02-10", "2025-03-10", "2025-01-16", "后到")); err != nil {
		t.Fatalf("用当前截止日重新提交应成功: %v", err)
	}
}

func TestReviseIdempotency(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	first, err := s.Revise(revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "补充凭证"))
	if err != nil {
		t.Fatal(err)
	}

	// 同编号同内容重试：返回第一次成功的记录，不增加历史。
	again, err := s.Revise(revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "补充凭证"))
	if err != nil {
		t.Fatalf("同内容重放应成功: %v", err)
	}
	if again != first {
		t.Fatalf("重放应返回第一次成功的记录: %+v != %+v", again, first)
	}
	h, _, _ := s.History("A-1")
	if len(h.Revisions) != 1 {
		t.Fatalf("重放不应增加历史，得到 %d 条", len(h.Revisions))
	}

	// 文本比较以去首尾空白后的值为准。
	padded := revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "  补充凭证  ")
	if _, err := s.Revise(padded); err != nil {
		t.Fatalf("仅首尾空白不同应视为同内容重放: %v", err)
	}

	// 后来再次改过期限后，同编号重试仍取回原记录。
	if _, err := s.Revise(revInput("R-2", "A-1", "2025-02-10", "2025-03-10", "2025-01-16", "再次调整")); err != nil {
		t.Fatal(err)
	}
	afterChange, err := s.Revise(revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "补充凭证"))
	if err != nil {
		t.Fatalf("期限改过之後重放仍应成功: %v", err)
	}
	if afterChange != first {
		t.Fatalf("期限改过之後重放仍应返回原记录: %+v", afterChange)
	}

	// 销毁后同编号重试仍取回原记录。
	if _, err := s.Destroy(DestructionRequest{ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-03-10"), ArchiveIDs: []string{"A-1"}}); err != nil {
		t.Fatal(err)
	}
	afterDestroy, err := s.Revise(revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "补充凭证"))
	if err != nil {
		t.Fatalf("销毁后重放仍应成功: %v", err)
	}
	if afterDestroy != first {
		t.Fatalf("销毁后重放仍应返回原记录: %+v", afterDestroy)
	}

	// 沿用成功编号改变任何一项内容：编号冲突。
	changed := revInput("R-1", "A-1", "2025-01-10", "2025-04-10", "2025-01-15", "补充凭证")
	if _, err := s.Revise(changed); !errors.Is(err, ErrDuplicateRevisionID) {
		t.Fatalf("沿用编号改内容应冲突，得到 %v", err)
	}
}

func TestReviseFailedIDReusable(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	// 因原截止日不符失败。
	if _, err := s.Revise(revInput("R-1", "A-1", "2025-01-01", "2025-02-10", "2025-01-15", "r")); !errors.Is(err, ErrRevisionEndMismatch) {
		t.Fatalf("前置失败不正确: %v", err)
	}
	// 失败过的编号仍可重新提交并成功。
	if _, err := s.Revise(revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "r")); err != nil {
		t.Fatalf("失败过的编号应可重新提交: %v", err)
	}
}

func TestReviseIDUniqueAcrossArchivesAndCrossNamespace(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")

	// 修订编号在全部档案之间唯一：同编号用于不同档案且内容不同，冲突。
	if _, err := s.Revise(revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "r")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revise(revInput("R-1", "A-2", "2025-01-10", "2025-02-10", "2025-01-15", "r")); !errors.Is(err, ErrDuplicateRevisionID) {
		t.Fatalf("修订编号在全部档案之间应唯一，得到 %v", err)
	}

	// 修订编号与销毁申请编号互不占用。
	if _, err := s.Destroy(DestructionRequest{ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-2"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revise(revInput("APP-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "r")); !errors.Is(err, ErrRevisionIDUsedByManifest) {
		t.Fatalf("修订编号占用销毁申请编号应失败，得到 %v", err)
	}
	if _, err := s.Revise(revInput("R-2", "A-1", "2025-02-10", "2025-03-10", "2025-01-15", "r")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy(DestructionRequest{ApplicationID: "R-2", ProcessedOn: MustParseDate("2025-03-10"), ArchiveIDs: []string{"A-1"}}); !errors.Is(err, ErrApplicationIDUsedByRevision) {
		t.Fatalf("销毁申请编号占用修订编号应失败，得到 %v", err)
	}
}

func TestReviseFrozenArchiveKeepsFreezeRules(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")}); err != nil {
		t.Fatal(err)
	}

	// 被冻结的档案也允许修订期限（缩短）。
	if _, err := s.Revise(revInput("R-1", "A-1", "2025-01-10", "2025-01-05", "2025-01-15", "缩短")); err != nil {
		t.Fatalf("被冻结档案应允许修订期限: %v", err)
	}
	h, _, _ := s.History("A-1")
	if !h.RetentionEnd.Equal(MustParseDate("2025-01-05")) || len(h.ActiveFreezes) != 1 {
		t.Fatalf("修订不应解除冻结: %+v", h)
	}

	// 期限缩短后仍有未解除冻结，不能销毁。
	if _, err := s.Destroy(DestructionRequest{ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"}}); !errors.Is(err, ErrActiveFreeze) {
		t.Fatalf("缩短后仍须满足冻结规则，得到 %v", err)
	}

	// 解除冻结后，按最新截止日判断到期（截止日当天即到期），可以销毁。
	if err := s.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-10")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy(DestructionRequest{ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-05"), ArchiveIDs: []string{"A-1"}}); err != nil {
		t.Fatalf("解除冻结后按最新截止日应可销毁: %v", err)
	}
}

func TestReviseAndDestroyUseLatestDeadline(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	// 延长截止日后，核对与销毁都按最新截止日判断。
	if _, err := s.Revise(revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "延长")); err != nil {
		t.Fatal(err)
	}
	r := mustCheck(t, s, "APP-1", "2025-01-10", "A-1")
	if r.Status != CheckBlocked || len(r.Results[0].Obstructions) != 1 || r.Results[0].Obstructions[0].Kind != ObstructionNotExpired {
		t.Fatalf("延长后按新截止日应未到期: %+v", r)
	}
	if _, err := s.Destroy(DestructionRequest{ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"}}); !errors.Is(err, ErrNotExpired) {
		t.Fatalf("延长后按新截止日应未到期，得到 %v", err)
	}

	// 缩短截止日后，截止日当天即到期。
	if _, err := s.Revise(revInput("R-2", "A-1", "2025-02-10", "2025-01-10", "2025-01-16", "缩短")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy(DestructionRequest{ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"}}); err != nil {
		t.Fatalf("缩短后截止日当天应可销毁: %v", err)
	}

	// 清册记录销毁成功时的截止日（即最新截止日）。
	m, found, _ := s.GetManifest("APP-1")
	if !found || len(m.Entries) != 1 || !m.Entries[0].End.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("清册应记录销毁时的截止日: %+v", m)
	}
}

func TestReviseReturnIsDetachedCopy(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	r, err := s.Revise(revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "补充凭证"))
	if err != nil {
		t.Fatal(err)
	}

	// 调用者修改返回记录，不影响保存内容。
	r.NewEnd = MustParseDate("2099-12-31")
	r.Reason = "被篡改"

	h, _, _ := s.History("A-1")
	if !h.RetentionEnd.Equal(MustParseDate("2025-02-10")) || h.Revisions[0].NewEnd.Equal(MustParseDate("2099-12-31")) {
		t.Fatalf("修改返回记录不应影响保存内容: %+v", h)
	}
	if h.Revisions[0].Reason != "补充凭证" {
		t.Fatalf("修改返回记录不应影响保存的原因: %+v", h.Revisions[0])
	}
}

func TestRevisePersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	if _, err := s.Revise(revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "补充凭证")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	h, found, err := s2.History("A-1")
	if err != nil || !found {
		t.Fatalf("重开后档案丢失: %v %v", found, err)
	}
	if !h.RegisteredEnd.Equal(MustParseDate("2025-01-10")) || !h.RetentionEnd.Equal(MustParseDate("2025-02-10")) {
		t.Fatalf("重开后最初/当前截止日不正确: %+v", h)
	}
	if len(h.Revisions) != 1 || h.Revisions[0].RevisionID != "R-1" ||
		!h.Revisions[0].OriginalEnd.Equal(MustParseDate("2025-01-10")) ||
		!h.Revisions[0].NewEnd.Equal(MustParseDate("2025-02-10")) ||
		h.Revisions[0].Reason != "补充凭证" {
		t.Fatalf("重开后修订历史不完整: %+v", h.Revisions)
	}

	// 重开后同编号重试仍取回原记录。
	again, err := s2.Revise(revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "补充凭证"))
	if err != nil {
		t.Fatalf("重开后重放应成功: %v", err)
	}
	if again.RevisionID != "R-1" || !again.NewEnd.Equal(MustParseDate("2025-02-10")) {
		t.Fatalf("重开后重放记录不正确: %+v", again)
	}
}

func TestReviseOnVaultWithoutRevisions(t *testing.T) {
	// 现有保管库没有修订记录时继续可用，登记截止日就是最初截止日。
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	h, found, err := s2.History("A-1")
	if err != nil || !found {
		t.Fatalf("重开后档案丢失: %v %v", found, err)
	}
	if !h.RegisteredEnd.Equal(MustParseDate("2025-01-10")) || !h.RetentionEnd.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("无修订时登记截止日应即最初截止日: %+v", h)
	}
	if len(h.Revisions) != 0 {
		t.Fatalf("无修订时修订历史应为空: %+v", h.Revisions)
	}
}

// TestReviseConcurrentTwoRevises 验证两个程序同时把同一截止日改成不同日期，
// 只能成功一次，另一次收到期限已变化错误。
func TestReviseConcurrentTwoRevises(t *testing.T) {
	dir := t.TempDir()
	setup, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := setup.Close(); err != nil {
		t.Fatal(err)
	}

	const rounds = 20
	for round := 0; round < rounds; round++ {
		s1, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		s2, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}

		// 每轮用一个全新档案与全新修订编号，避免跨轮的修订编号重放。
		archiveID := "A-" + string(rune('a'+round))
		rev1, rev2 := "R-1-"+string(rune('a'+round)), "R-2-"+string(rune('a'+round))
		reg(t, s1, archiveID, "合同", "2020-01-01", "2025-01-10")

		var wg sync.WaitGroup
		var err1, err2 error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err1 = s1.Revise(revInput(rev1, archiveID, "2025-01-10", "2025-02-10", "2025-01-15", "先"))
		}()
		go func() {
			defer wg.Done()
			_, err2 = s2.Revise(revInput(rev2, archiveID, "2025-01-10", "2025-03-10", "2025-01-16", "后"))
		}()
		wg.Wait()

		switch {
		case err1 == nil && err2 == nil:
			t.Fatalf("第 %d 轮：两个修订同时成功，违反互斥", round)
		case err1 != nil && err2 != nil:
			t.Fatalf("第 %d 轮：两个修订都失败: %v %v", round, err1, err2)
		case err1 == nil:
			// 先成功者把期限改为 2025-02-10；后到者必须收到期限已变化错误并提供当前截止日。
			if !errors.Is(err2, ErrRevisionEndMismatch) {
				t.Fatalf("第 %d 轮：后到者应收到期限已变化错误，得到 %v", round, err2)
			}
			var m *RevisionEndMismatchError
			if !errors.As(err2, &m) || !m.Current.Equal(MustParseDate("2025-02-10")) {
				t.Fatalf("第 %d 轮：期限已变化错误应提供当前截止日: %v", round, err2)
			}
		default:
			if !errors.Is(err1, ErrRevisionEndMismatch) {
				t.Fatalf("第 %d 轮：后到者应收到期限已变化错误，得到 %v", round, err1)
			}
			var m *RevisionEndMismatchError
			if !errors.As(err1, &m) || !m.Current.Equal(MustParseDate("2025-03-10")) {
				t.Fatalf("第 %d 轮：期限已变化错误应提供当前截止日: %v", round, err1)
			}
		}

		// 每轮结束后期限与历史必须一致：只有一条成功修订，当前截止日是它的新截止日。
		h, _, _ := s1.History(archiveID)
		if len(h.Revisions) != 1 {
			t.Fatalf("第 %d 轮：应有 1 条修订历史，得到 %d", round, len(h.Revisions))
		}
		want := h.Revisions[0].NewEnd
		if !h.RetentionEnd.Equal(want) {
			t.Fatalf("第 %d 轮：当前截止日与历史不一致: %+v", round, h)
		}
		s1.Close()
		s2.Close()
	}
}

// TestReviseAndDestroyConcurrent 验证修订与销毁同时办理时按先后生效：
// 延长先成功，销毁须重新判断是否到期；销毁先成功，新修订失败。
func TestReviseAndDestroyConcurrent(t *testing.T) {
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

		var wg sync.WaitGroup
		var revErr, destroyErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, revErr = s1.Revise(revInput("R-1", "A-1", "2025-01-10", "2025-02-10", "2025-01-15", "延长"))
		}()
		go func() {
			defer wg.Done()
			_, destroyErr = s2.Destroy(DestructionRequest{
				ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
			})
		}()
		wg.Wait()

		switch {
		case revErr == nil && destroyErr == nil:
			t.Fatalf("第 %d 轮：修订与销毁同时成功，违反互斥", round)
		case revErr == nil:
			// 修订先成功：销毁必须按新截止日重新判断，因未到期而失败。
			if !errors.Is(destroyErr, ErrNotExpired) {
				t.Fatalf("第 %d 轮：修订先成功，销毁应按新截止日判未到期，得到 %v", round, destroyErr)
			}
		default:
			// 销毁先成功：修订必须因档案已销毁而失败。
			if !errors.Is(revErr, ErrDestroyed) {
				t.Fatalf("第 %d 轮：销毁先成功，后续修订应失败，得到 %v", round, revErr)
			}
			s1.Close()
			s2.Close()
			return
		}
		s1.Close()
		s2.Close()
	}
}
