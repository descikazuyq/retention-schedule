package retention

import (
	"errors"
	"testing"
)

func TestCheckValidation(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	base := CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"A-1"},
	}

	// 申请编号空白。
	for _, blank := range []string{"", "   ", "\t"} {
		req := base
		req.ApplicationID = blank
		if _, err := s.Check(req); !errors.Is(err, ErrBlankField) {
			t.Fatalf("空白申请编号 %q 应失败，得到 %v", blank, err)
		}
	}

	// 处理日期缺失（零值）。
	req := base
	req.ProcessedOn = Date{}
	if _, err := s.Check(req); !errors.Is(err, ErrInvalidDate) {
		t.Fatalf("缺失处理日期应失败，得到 %v", err)
	}

	// 名单为空。
	req = base
	req.ArchiveIDs = nil
	if _, err := s.Check(req); !errors.Is(err, ErrEmptyDestructionList) {
		t.Fatalf("空名单应失败，得到 %v", err)
	}

	// 档案编号空白。
	for _, blank := range []string{"", "   ", "\t"} {
		req := base
		req.ArchiveIDs = []string{blank}
		if _, err := s.Check(req); !errors.Is(err, ErrBlankField) {
			t.Fatalf("空白档案编号 %q 应失败，得到 %v", blank, err)
		}
	}

	// 名单内重复（完全相同）。
	req = base
	req.ArchiveIDs = []string{"A-1", "A-1"}
	if _, err := s.Check(req); !errors.Is(err, ErrDuplicateSelection) {
		t.Fatalf("名单内重复应失败，得到 %v", err)
	}

	// 同一编号仅因首尾空白不同也算重复。
	req = base
	req.ArchiveIDs = []string{"A-1", " A-1 "}
	if _, err := s.Check(req); !errors.Is(err, ErrDuplicateSelection) {
		t.Fatalf("仅首尾空白不同的重复应失败，得到 %v", err)
	}

	// 校验失败不产生报告，且编号未被占用：之后仍可正常核对。
	if report, err := s.Check(base); err != nil || report.Status != CheckOK {
		t.Fatalf("校验通过后应能正常核对，得到 report=%+v err=%v", report, err)
	}
}

func TestCheckOK(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")

	// 截止日当天即到期，无冻结：可以办理。
	report, err := s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"A-2", "A-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != CheckOK {
		t.Fatalf("应可以办理，得到 %s", report.Status)
	}
	if len(report.Archives) != 2 {
		t.Fatalf("应按提交顺序给出 2 份档案，得到 %d", len(report.Archives))
	}
	// 顺序沿用提交顺序，而非排序。
	if report.Archives[0].ID != "A-2" || report.Archives[1].ID != "A-1" {
		t.Fatalf("档案顺序应沿用提交顺序: %+v", report.Archives)
	}
	for _, ac := range report.Archives {
		if !ac.Found || ac.Destroyed || len(ac.Blockers) != 0 {
			t.Fatalf("档案 %s 不应有阻碍: %+v", ac.ID, ac)
		}
	}
}

func TestCheckNotFoundDoesNotBlockOthers(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	report, err := s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"GHOST", "A-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != CheckBlocked {
		t.Fatalf("含不存在档案应存在阻碍，得到 %s", report.Status)
	}
	if len(report.Archives) != 2 {
		t.Fatalf("应逐份给出结果，得到 %d", len(report.Archives))
	}
	// 不存在的编号单独标明。
	ghost := report.Archives[0]
	if ghost.Found || len(ghost.Blockers) != 1 || ghost.Blockers[0].Kind != BlockerNotFound {
		t.Fatalf("不存在档案应标明 BlockerNotFound: %+v", ghost)
	}
	// 其他档案继续核对，不受影响。
	a1 := report.Archives[1]
	if !a1.Found || len(a1.Blockers) != 0 {
		t.Fatalf("A-1 应无阻碍: %+v", a1)
	}
}

func TestCheckNotExpired(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	report, err := s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-09"),
		ArchiveIDs:    []string{"A-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != CheckBlocked {
		t.Fatalf("未到期应存在阻碍，得到 %s", report.Status)
	}
	ac := report.Archives[0]
	if len(ac.Blockers) != 1 || ac.Blockers[0].Kind != BlockerNotExpired {
		t.Fatalf("应标明未到期阻碍: %+v", ac.Blockers)
	}
}

func TestCheckActiveFreezeWithDetails(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")}); err != nil {
		t.Fatal(err)
	}

	report, err := s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"A-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != CheckBlocked {
		t.Fatalf("有未解除冻结应存在阻碍，得到 %s", report.Status)
	}
	ac := report.Archives[0]
	if len(ac.Blockers) != 1 || ac.Blockers[0].Kind != BlockerActiveFreeze {
		t.Fatalf("应标明未解除冻结阻碍: %+v", ac.Blockers)
	}
	b := ac.Blockers[0]
	if b.FreezeID != "F-1" || b.Reason != "诉讼" || !b.FrozenOn.Equal(MustParseDate("2025-01-05")) {
		t.Fatalf("冻结阻碍应带编号、原因和冻结日期: %+v", b)
	}
}

func TestCheckReleasedFreezeIsNotBlocker(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-06")}); err != nil {
		t.Fatal(err)
	}

	// 已解除记录不作为阻碍。
	report, err := s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"A-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != CheckOK {
		t.Fatalf("冻结已解除应可以办理，得到 %s", report.Status)
	}
}

func TestCheckNotExpiredAndMultipleFreezesBothShown(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-2", Reason: "审计", FrozenOn: MustParseDate("2025-01-07")}); err != nil {
		t.Fatal(err)
	}

	// 既未到期又有多条冻结：两类问题都要显示。
	report, err := s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-09"),
		ArchiveIDs:    []string{"A-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != CheckBlocked {
		t.Fatalf("应存在阻碍，得到 %s", report.Status)
	}
	ac := report.Archives[0]
	if len(ac.Blockers) != 3 {
		t.Fatalf("应列出未到期 + 2 条冻结共 3 项阻碍，得到 %d: %+v", len(ac.Blockers), ac.Blockers)
	}
	if ac.Blockers[0].Kind != BlockerNotExpired {
		t.Fatalf("第一项应为未到期: %+v", ac.Blockers[0])
	}
	// 冻结顺序沿用冻结历史。
	if ac.Blockers[1].Kind != BlockerActiveFreeze || ac.Blockers[1].FreezeID != "F-1" {
		t.Fatalf("第二项应为 F-1 冻结: %+v", ac.Blockers[1])
	}
	if ac.Blockers[2].Kind != BlockerActiveFreeze || ac.Blockers[2].FreezeID != "F-2" {
		t.Fatalf("第三项应为 F-2 冻结: %+v", ac.Blockers[2])
	}
	if ac.Blockers[1].Reason != "诉讼" || !ac.Blockers[1].FrozenOn.Equal(MustParseDate("2025-01-05")) {
		t.Fatalf("F-1 冻结信息不正确: %+v", ac.Blockers[1])
	}
	if ac.Blockers[2].Reason != "审计" || !ac.Blockers[2].FrozenOn.Equal(MustParseDate("2025-01-07")) {
		t.Fatalf("F-2 冻结信息不正确: %+v", ac.Blockers[2])
	}
}

func TestCheckDestroyedArchiveGivesManifestInfo(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatal(err)
	}

	report, err := s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-11"),
		ArchiveIDs:    []string{"A-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != CheckBlocked {
		t.Fatalf("已销毁档案应存在阻碍，得到 %s", report.Status)
	}
	ac := report.Archives[0]
	if !ac.Destroyed {
		t.Fatalf("应标明已销毁: %+v", ac)
	}
	if len(ac.Blockers) != 1 || ac.Blockers[0].Kind != BlockerDestroyed {
		t.Fatalf("应仅标明已销毁阻碍: %+v", ac.Blockers)
	}
	// 已销毁档案给出所属清册的申请编号和处理日期。
	if ac.ManifestApplicationID != "APP-9" {
		t.Fatalf("应给出所属清册申请编号，得到 %q", ac.ManifestApplicationID)
	}
	if !ac.ManifestProcessedOn.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("应给出所属清册处理日期，得到 %s", ac.ManifestProcessedOn)
	}
}

func TestCheckMixedBatch(t *testing.T) {
	s := openTestStore(t)
	// A-1 截止日早于处理日期：已到期且无冻结，可以办理。
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-08")
	// A-2 截止日晚于处理日期：未到期。
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	// A-3 已到期但有未解除冻结。
	reg(t, s, "A-3", "图纸", "2020-01-01", "2025-01-08")
	if err := s.Freeze(FreezeInput{ArchiveID: "A-3", FreezeID: "F", Reason: "争议", FrozenOn: MustParseDate("2025-01-08")}); err != nil {
		t.Fatal(err)
	}

	// A-1 可办理，A-2 未到期，A-3 有冻结，GHOST 不存在。
	report, err := s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-09"),
		ArchiveIDs:    []string{"A-1", "A-2", "A-3", "GHOST"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != CheckBlocked {
		t.Fatalf("混合批次应存在阻碍，得到 %s", report.Status)
	}
	if len(report.Archives) != 4 {
		t.Fatalf("应逐份给出 4 份结果，得到 %d", len(report.Archives))
	}
	// A-1 无阻碍。
	if len(report.Archives[0].Blockers) != 0 {
		t.Fatalf("A-1 应无阻碍: %+v", report.Archives[0])
	}
	// A-2 未到期。
	if len(report.Archives[1].Blockers) != 1 || report.Archives[1].Blockers[0].Kind != BlockerNotExpired {
		t.Fatalf("A-2 应未到期: %+v", report.Archives[1])
	}
	// A-3 有冻结。
	if len(report.Archives[2].Blockers) != 1 || report.Archives[2].Blockers[0].Kind != BlockerActiveFreeze {
		t.Fatalf("A-3 应有冻结: %+v", report.Archives[2])
	}
	// GHOST 不存在。
	if report.Archives[3].Found || report.Archives[3].Blockers[0].Kind != BlockerNotFound {
		t.Fatalf("GHOST 应不存在: %+v", report.Archives[3])
	}
}

func TestCheckReplayable(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1", "A-2"},
	}); err != nil {
		t.Fatal(err)
	}

	// 申请编号已成功使用，日期与集合相同（顺序无关）：可以取回原清册，
	// 不因档案已销毁而判为受阻。
	report, err := s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"A-2", "A-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != CheckReplayable {
		t.Fatalf("应可以取回原清册，得到 %s", report.Status)
	}
	if report.Manifest == nil {
		t.Fatalf("应返回原清册")
	}
	if report.Manifest.ApplicationID != "APP-1" || len(report.Manifest.Entries) != 2 {
		t.Fatalf("原清册内容不正确: %+v", report.Manifest)
	}
	// 取回的清册条目按编号排序。
	if report.Manifest.Entries[0].ID != "A-1" || report.Manifest.Entries[1].ID != "A-2" {
		t.Fatalf("清册条目排序不正确: %+v", report.Manifest.Entries)
	}
}

func TestCheckConflict(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1", "A-2"},
	}); err != nil {
		t.Fatal(err)
	}

	// 沿用编号改变日期：申请编号冲突，附原清册，不得显示可以办理。
	report, err := s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-11"),
		ArchiveIDs:    []string{"A-1", "A-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != CheckConflict {
		t.Fatalf("应申请编号冲突，得到 %s", report.Status)
	}
	if report.Manifest == nil || report.Manifest.ApplicationID != "APP-1" {
		t.Fatalf("冲突应附原清册: %+v", report.Manifest)
	}

	// 沿用编号改变集合：同样冲突。
	report, err = s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"A-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != CheckConflict {
		t.Fatalf("改变集合应申请编号冲突，得到 %s", report.Status)
	}
	if report.Manifest == nil {
		t.Fatalf("冲突应附原清册")
	}
}

func TestCheckDoesNotMutateAndDoesNotOccupyID(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	// 核对不销毁档案、不生成清册。
	report, err := s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"A-1"},
	})
	if err != nil || report.Status != CheckOK {
		t.Fatalf("核对应可以办理，得到 report=%+v err=%v", report, err)
	}
	h, found, _ := s.History("A-1")
	if !found || h.Destroyed {
		t.Fatalf("核对不应销毁档案: found=%v destroyed=%v", found, h.Destroyed)
	}
	if _, found, _ := s.GetManifest("APP-1"); found {
		t.Fatalf("核对不应生成清册")
	}

	// 核对不占用申请编号：之后仍可用同一编号正式提交。
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatalf("核对后同编号应仍可正式提交，得到 %v", err)
	}
	if _, found, _ := s.GetManifest("APP-1"); !found {
		t.Fatalf("正式提交后应有清册")
	}
}

func TestCheckReportIsSnapshot(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatal(err)
	}

	report, err := s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"A-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 调用者修改返回报告及其中清册，不得影响保存的历史。
	report.Status = CheckBlocked
	report.Manifest.ApplicationID = "TAMPERED"
	report.Manifest.Entries[0].ID = "TAMPERED"

	h, found, _ := s.History("A-1")
	if !found || h.ManifestApplicationID != "APP-1" {
		t.Fatalf("修改报告不应影响保存历史: %+v", h)
	}
	m, found, _ := s.GetManifest("APP-1")
	if !found || m.ApplicationID != "APP-1" || m.Entries[0].ID != "A-1" {
		t.Fatalf("修改报告中的清册不应影响保存的清册: %+v", m)
	}
}

func TestCheckTrimsWhitespace(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	// 编号去除首尾空白后再判断。
	report, err := s.Check(CheckRequest{
		ApplicationID: "  APP-1  ",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"  A-1  "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != CheckOK {
		t.Fatalf("去空白后应可以办理，得到 %s", report.Status)
	}
	if report.Archives[0].ID != "A-1" {
		t.Fatalf("档案编号应去除首尾空白，得到 %q", report.Archives[0].ID)
	}
}
