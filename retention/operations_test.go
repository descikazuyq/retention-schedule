package retention

import (
	"errors"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开保管库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func reg(t *testing.T, s *Store, id, category, start, end string) {
	t.Helper()
	err := s.Register(RegisterInput{
		ID:       id,
		Category: category,
		Start:    MustParseDate(start),
		End:      MustParseDate(end),
	})
	if err != nil {
		t.Fatalf("登记 %s 失败: %v", id, err)
	}
}

func TestRegisterValidation(t *testing.T) {
	s := openTestStore(t)

	valid := RegisterInput{
		ID:       "A-001",
		Category: "合同",
		Start:    MustParseDate("2020-01-01"),
		End:      MustParseDate("2025-01-01"),
	}
	if err := s.Register(valid); err != nil {
		t.Fatalf("正常登记失败: %v", err)
	}

	// 重复编号失败。
	if err := s.Register(valid); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("重复编号应失败 ErrDuplicateID，得到 %v", err)
	}

	// 编号或类别为空白（含纯空格）。
	for _, blank := range []string{"", "   ", "\t"} {
		in := valid
		in.ID = blank
		if err := s.Register(in); !errors.Is(err, ErrBlankField) {
			t.Fatalf("空白编号 %q 应失败，得到 %v", blank, err)
		}
		in = valid
		in.Category = blank
		if err := s.Register(in); !errors.Is(err, ErrBlankField) {
			t.Fatalf("空白类别 %q 应失败，得到 %v", blank, err)
		}
	}

	// 截止日早于起算日（含同一天合法）。
	early := valid
	early.ID = "A-002"
	early.End = MustParseDate("2019-12-31")
	if err := s.Register(early); !errors.Is(err, ErrRetentionEndBeforeStart) {
		t.Fatalf("截止日早于起算日应失败，得到 %v", err)
	}
	same := valid
	same.ID = "A-003"
	same.Start = MustParseDate("2022-06-06")
	same.End = MustParseDate("2022-06-06")
	if err := s.Register(same); err != nil {
		t.Fatalf("截止日等于起算日应允许，得到 %v", err)
	}

	// 各类失败后，已有记录仍可核对，且失败编号未被占用。
	if _, found, err := s.History("A-001"); err != nil || !found {
		t.Fatalf("失败后已有记录应仍可查，found=%v err=%v", found, err)
	}
	retry := valid
	retry.ID = "A-002"
	if err := s.Register(retry); err != nil {
		t.Fatalf("此前失败的编号应仍可登记，得到 %v", err)
	}
}

func TestRegisterZeroDateRejected(t *testing.T) {
	s := openTestStore(t)
	err := s.Register(RegisterInput{ID: "X", Category: "C", End: MustParseDate("2025-01-01")})
	if !errors.Is(err, ErrInvalidDate) {
		t.Fatalf("缺失起算日应失败，得到 %v", err)
	}
}

func TestFreezeAndRelease(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-01")

	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-02")}); err != nil {
		t.Fatalf("新增冻结失败: %v", err)
	}
	// 同档案内冻结编号唯一；不同档案允许同号。
	reg(t, s, "A-2", "合同", "2020-01-01", "2025-01-01")
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "再次", FrozenOn: MustParseDate("2025-01-03")}); !errors.Is(err, ErrDuplicateFreezeID) {
		t.Fatalf("重复冻结编号应失败，得到 %v", err)
	}
	if err := s.Freeze(FreezeInput{ArchiveID: "A-2", FreezeID: "F-1", Reason: "另案", FrozenOn: MustParseDate("2025-01-03")}); err != nil {
		t.Fatalf("不同档案使用相同冻结编号应允许，得到 %v", err)
	}
	// 再给 A-1 一条冻结，验证多条互不影响。
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-2", Reason: "审计", FrozenOn: MustParseDate("2025-01-04")}); err != nil {
		t.Fatalf("第二条冻结失败: %v", err)
	}

	// 不存在 / 空白原因 / 空白编号。
	if err := s.Freeze(FreezeInput{ArchiveID: "NOPE", FreezeID: "F", Reason: "r", FrozenOn: MustParseDate("2025-01-02")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("给不存在档案冻结应失败，得到 %v", err)
	}
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-3", Reason: "  ", FrozenOn: MustParseDate("2025-01-02")}); !errors.Is(err, ErrBlankField) {
		t.Fatalf("空白冻结原因应失败，得到 %v", err)
	}

	// 解除日期不能早于冻结日期。
	if err := s.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-01")}); !errors.Is(err, ErrReleaseBeforeFreeze) {
		t.Fatalf("解除日期早于冻结日期应失败，得到 %v", err)
	}
	// 解除不存在或已解除的冻结。
	if err := s.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "NOPE", Reason: "x", ReleasedOn: MustParseDate("2025-02-01")}); !errors.Is(err, ErrFreezeNotFound) {
		t.Fatalf("解除不存在冻结应失败，得到 %v", err)
	}
	if err := s.Release(ReleaseInput{ArchiveID: "NOPE", FreezeID: "F-1", Reason: "x", ReleasedOn: MustParseDate("2025-02-01")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("解除不存在档案的冻结应失败，得到 %v", err)
	}
	if err := s.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "  ", ReleasedOn: MustParseDate("2025-02-01")}); !errors.Is(err, ErrBlankField) {
		t.Fatalf("空白解除原因应失败，得到 %v", err)
	}

	// 正常解除 F-1（解除日期等于冻结日期合法）；F-2 仍然挂着。
	if err := s.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-02")}); err != nil {
		t.Fatalf("解除冻结失败: %v", err)
	}
	if err := s.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "再结一次", ReleasedOn: MustParseDate("2025-01-05")}); !errors.Is(err, ErrFreezeAlreadyReleased) {
		t.Fatalf("重复解除应失败，得到 %v", err)
	}

	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("核对失败: %v %v", found, err)
	}
	if len(h.Freezes) != 2 {
		t.Fatalf("全部冻结历史应保留 2 条，得到 %d", len(h.Freezes))
	}
	if len(h.ActiveFreezes) != 1 || h.ActiveFreezes[0].ID != "F-2" {
		t.Fatalf("应只剩 F-2 未解除，得到 %+v", h.ActiveFreezes)
	}
	f1 := h.Freezes[0]
	if !f1.Released || f1.ReleaseReason != "结案" || !f1.ReleasedOn.Equal(MustParseDate("2025-01-02")) {
		t.Fatalf("F-1 解除信息不正确: %+v", f1)
	}
}

func TestExpiryBoundaryAndFreezeBlockDestruction(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	// 提前一天不能销毁。
	if _, err := s.Destroy(DestructionRequest{ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-09"), ArchiveIDs: []string{"A-1"}}); !errors.Is(err, ErrNotExpired) {
		t.Fatalf("截止日前一天不应到期，得到 %v", err)
	}
	// 到期后但有未解除冻结，仍不能销毁。
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-10")}); err != nil {
		t.Fatalf("冻结失败: %v", err)
	}
	if _, err := s.Destroy(DestructionRequest{ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"}}); !errors.Is(err, ErrActiveFreeze) {
		t.Fatalf("截止日当天视为到期，但冻结应阻止销毁，得到 %v", err)
	}
	// 解除后，截止日当天可以销毁。
	if err := s.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-10")}); err != nil {
		t.Fatalf("解除失败: %v", err)
	}
	m, err := s.Destroy(DestructionRequest{ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"}})
	if err != nil {
		t.Fatalf("到期且无冻结应销毁成功，得到 %v", err)
	}
	if m.ApplicationID != "APP-1" || len(m.Entries) != 1 || m.Entries[0].ID != "A-1" {
		t.Fatalf("清册内容不正确: %+v", m)
	}

	// 销毁后不能再新增冻结，也不能再次销毁。
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-2", Reason: "迟来", FrozenOn: MustParseDate("2025-01-11")}); !errors.Is(err, ErrDestroyed) {
		t.Fatalf("销毁后新增冻结应失败，得到 %v", err)
	}
	if _, err := s.Destroy(DestructionRequest{ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-01-11"), ArchiveIDs: []string{"A-1"}}); !errors.Is(err, ErrArchiveAlreadyOnManifest) {
		t.Fatalf("同一档案不能出现在两份清册中，得到 %v", err)
	}
}

func TestDestroyBatchAtomicity(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	reg(t, s, "A-3", "图纸", "2020-01-01", "2025-01-10")
	if err := s.Freeze(FreezeInput{ArchiveID: "A-3", FreezeID: "F", Reason: "争议", FrozenOn: MustParseDate("2025-01-10")}); err != nil {
		t.Fatal(err)
	}

	// 空名单。
	if _, err := s.Destroy(DestructionRequest{ApplicationID: "APP", ProcessedOn: MustParseDate("2025-01-10")}); !errors.Is(err, ErrEmptyDestructionList) {
		t.Fatalf("空名单应失败，得到 %v", err)
	}
	// 名单内重复。
	if _, err := s.Destroy(DestructionRequest{ApplicationID: "APP", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1", "A-1"}}); !errors.Is(err, ErrDuplicateSelection) {
		t.Fatalf("重复名单应失败，得到 %v", err)
	}
	// 含不存在档案。
	if _, err := s.Destroy(DestructionRequest{ApplicationID: "APP", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1", "GHOST"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("含不存在档案应失败，得到 %v", err)
	}
	// A-3 有冻结：整次失败，A-1、A-2 必须仍未销毁，且没有清册。
	if _, err := s.Destroy(DestructionRequest{ApplicationID: "APP", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1", "A-2", "A-3"}}); !errors.Is(err, ErrActiveFreeze) {
		t.Fatalf("含冻结档案应整次失败，得到 %v", err)
	}
	for _, id := range []string{"A-1", "A-2", "A-3"} {
		h, found, _ := s.History(id)
		if !found || h.Destroyed {
			t.Fatalf("失败后 %s 不应被销毁: %+v", id, h)
		}
	}
	if _, found, err := s.GetManifest("APP"); err != nil || found {
		t.Fatalf("失败的申请不应留下清册，found=%v err=%v", found, err)
	}

	// 条件改变（解除 A-3）后，同一申请编号可再次提交并成功。
	if err := s.Release(ReleaseInput{ArchiveID: "A-3", FreezeID: "F", Reason: "解决", ReleasedOn: MustParseDate("2025-01-11")}); err != nil {
		t.Fatal(err)
	}
	m, err := s.Destroy(DestructionRequest{ApplicationID: "APP", ProcessedOn: MustParseDate("2025-01-12"), ArchiveIDs: []string{"A-1", "A-2", "A-3"}})
	if err != nil {
		t.Fatalf("条件满足后同编号应可成功，得到 %v", err)
	}
	if len(m.Entries) != 3 {
		t.Fatalf("清册应有 3 条，得到 %d", len(m.Entries))
	}
	// 清册为快照：条目按编号排序，登记字段取自关闭当时。
	if m.Entries[0].ID != "A-1" || m.Entries[1].ID != "A-2" || m.Entries[2].ID != "A-3" {
		t.Fatalf("清册条目排序不稳定: %+v", m.Entries)
	}
	if m.Entries[2].Category != "图纸" || !m.Entries[2].Start.Equal(MustParseDate("2020-01-01")) {
		t.Fatalf("清册快照字段不正确: %+v", m.Entries[2])
	}
}

func TestDestroyIdempotency(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")

	first, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"),
		ArchiveIDs: []string{"A-1", "A-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 顺序不同、日期与集合相同：返回原清册。
	again, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"),
		ArchiveIDs: []string{"A-2", "A-1"},
	})
	if err != nil {
		t.Fatalf("相同申请重放应返回原清册，得到 %v", err)
	}
	if again.ApplicationID != first.ApplicationID || len(again.Entries) != 2 {
		t.Fatalf("重放清册不一致: %+v", again)
	}
	// 改变日期：失败。
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-11"),
		ArchiveIDs: []string{"A-1", "A-2"},
	}); !errors.Is(err, ErrApplicationMismatch) {
		t.Fatalf("沿用编号改日期应失败，得到 %v", err)
	}
	// 改变集合：失败。
	reg(t, s, "A-3", "图纸", "2020-01-01", "2025-01-10")
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"),
		ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrApplicationMismatch) {
		t.Fatalf("沿用编号改集合应失败，得到 %v", err)
	}
}

func TestHistoryNotFoundAndManifestContents(t *testing.T) {
	s := openTestStore(t)
	if h, found, err := s.History("UNKNOWN"); err != nil || found || h.ID != "" {
		t.Fatalf("未登记编号应返回明确的不存在: found=%v err=%v h=%+v", found, err, h)
	}

	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-08")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-09")}); err != nil {
		t.Fatal(err)
	}
	m, err := s.Destroy(DestructionRequest{ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"}})
	if err != nil {
		t.Fatal(err)
	}

	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("核对失败: %v %v", found, err)
	}
	if !h.Destroyed || h.ManifestApplicationID != "APP-9" {
		t.Fatalf("销毁状态不正确: %+v", h)
	}
	if len(h.Freezes) != 1 || len(h.ActiveFreezes) != 0 || !h.Freezes[0].Released {
		t.Fatalf("冻结历史不正确: %+v", h.Freezes)
	}
	if h.Manifest == nil || h.Manifest.ApplicationID != m.ApplicationID {
		t.Fatalf("历史中应附带对应清册: %+v", h.Manifest)
	}
	if !h.Manifest.ProcessedOn.Equal(MustParseDate("2025-01-10")) || h.Manifest.Entries[0].Category != "合同" {
		t.Fatalf("清册快照内容不正确: %+v", h.Manifest)
	}
}
