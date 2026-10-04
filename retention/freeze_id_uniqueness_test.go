package retention

import (
	"errors"
	"strings"
	"testing"
)

// 同一档案的冻结历史中保存了两条相同编号的冻结时，即使其余内容全部合法，
// Open 也必须按保存记录损坏失败（ErrCorruptState）：错误指出档案编号与
// 重复的冻结编号，原文件保持原样。两条都未解除、一解一未解、两条均已解除
// （即使原因、日期和解除信息完全一致）结果相同，不能合并成一条。
// 已销毁档案（清册归属、期限与处理日期都正确、冻结全部合法解除）同样遵守。
func TestOpenRejectsDuplicateFreezeIDWithinArchive(t *testing.T) {
	freezeActive := func(id string) string {
		return `{"id":"` + id + `","reason":"诉讼","frozen_on":"2025-01-05","released":false}`
	}
	freezeReleased := func(id, reason, frozenOn, releasedOn string) string {
		return `{"id":"` + id + `","reason":"` + reason + `","frozen_on":"` + frozenOn +
			`","released":true,"release_reason":"结案","released_on":"` + releasedOn + `"}`
	}
	archive := func(id string, destroyed bool, freezes string) string {
		destroyedFields := `,"destroyed":false`
		if destroyed {
			destroyedFields = `,"destroyed":true,"manifest_id":"APP-1"`
		}
		return `"` + id + `":{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10"` +
			destroyedFields + `,"freezes":[` + freezes + `]}`
	}
	entry := func(id string) string {
		return `{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"2025-01-10"}`
	}
	state := func(archivesJSON, entriesJSON string) string {
		manifests := "{}"
		if entriesJSON != "" {
			manifests = `{"APP-1":{"application_id":"APP-1","processed_on":"2025-01-10","entries":[` + entriesJSON + `]}}`
		}
		return `{"version":1,"archives":{` + archivesJSON + `},"manifests":` + manifests + `}`
	}

	corrupt := map[string]struct {
		state   string
		wantAll []string // 错误信息必须全部包含
	}{
		"两条同号冻结均未解除且内容完全一致": {
			state(archive("A-1", false, freezeActive("F-1")+","+freezeActive("F-1")), ""),
			[]string{"A-1", "F-1"},
		},
		"一条已解除另一条未解除": {
			state(archive("A-1", false,
				freezeReleased("F-1", "诉讼", "2025-01-05", "2025-01-06")+","+freezeActive("F-1")), ""),
			[]string{"A-1", "F-1"},
		},
		"两条同号均已解除且信息完全一致": {
			state(archive("A-1", false,
				freezeReleased("F-1", "诉讼", "2025-01-05", "2025-01-06")+","+
					freezeReleased("F-1", "诉讼", "2025-01-05", "2025-01-06")), ""),
			[]string{"A-1", "F-1"},
		},
		"两条同号均已解除但原因日期不同": {
			state(archive("A-1", false,
				freezeReleased("F-1", "诉讼", "2025-01-05", "2025-01-06")+","+
					freezeReleased("F-1", "协查", "2025-01-07", "2025-01-08")), ""),
			[]string{"A-1", "F-1"},
		},
		"多条不同编号中混入一条重复": {
			state(archive("A-1", false,
				freezeReleased("F-1", "诉讼", "2025-01-05", "2025-01-06")+","+
					freezeActive("F-2")+","+freezeActive("F-2")), ""),
			[]string{"A-1", "F-2"},
		},
		"已销毁且全部合法解除但同号重复": {
			// 清册归属、条目快照、处理日期都合法，重复编号仍必须拒绝。
			state(archive("A-1", true,
				freezeReleased("F-1", "诉讼", "2025-01-05", "2025-01-06")+","+
					freezeReleased("F-1", "诉讼", "2025-01-05", "2025-01-06")),
				entry("A-1")),
			[]string{"A-1", "F-1"},
		},
		"多档案中第二份出现重复": {
			state(archive("A-1", false, freezeActive("F-1"))+","+
				archive("A-2", false, freezeActive("F-7")+","+freezeActive("F-7")), ""),
			[]string{"A-2", "F-7"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("同一档案冻结历史出现同号记录时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			for _, want := range tc.wantAll {
				if !strings.Contains(msg, want) {
					t.Fatalf("错误信息应指出 %q，得到 %v", want, err)
				}
			}
			if !strings.Contains(msg, "冻结") {
				t.Fatalf("错误信息应说明冻结历史出现重复，得到 %v", err)
			}
			if got := readStateFile(t, dir); got != tc.state {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：唯一性只限定在同一份档案内。不同档案各有一条同编号冻结合法；
	// 同一档案多条不同编号冻结（含已销毁且全部合法解除）继续合法。
	t.Run("合法记录对照组", func(t *testing.T) {
		valid := map[string]string{
			"不同档案各有同编号冻结": state(
				archive("A-1", false, freezeActive("F-1"))+","+archive("A-2", false, freezeActive("F-1")), ""),
			"同一档案多条不同编号": state(archive("A-1", false,
				freezeReleased("F-1", "诉讼", "2025-01-05", "2025-01-06")+","+freezeActive("F-2")), ""),
			"已销毁档案多条不同编号且全部解除": state(archive("A-1", true,
				freezeReleased("F-1", "诉讼", "2025-01-05", "2025-01-06")+","+
					freezeReleased("F-2", "协查", "2025-01-07", "2025-01-08")), entry("A-1")),
		}
		for name, content := range valid {
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				writeStateFile(t, dir, content)
				s, err := Open(dir)
				if err != nil {
					t.Fatalf("合法记录应能打开: %v", err)
				}
				defer s.Close()
			})
		}
	})
}

// 保管库打开后保存内容才出现同一档案冻结编号重复：
// 下一次使用有效输入查询历史、取回清册（含已成功申请的幂等重放）、销毁前
// 核对与各项办理都必须按整库记录损坏失败——即使操作的是另一份正常档案——
// 解除申请不得挑一条同号记录写入原因和日期，销毁申请不得生成清册，
// 不返回正常历史、清册或部分核对报告，原保存内容保持原样。
func TestDuplicateFreezeIDAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")
	reg(t, s, "A-3", "单据", "2020-01-01", "2025-01-10")
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-1", FreezeID: "F-2", Reason: "审计", FrozenOn: MustParseDate("2025-01-07"),
	}); err != nil {
		t.Fatal(err)
	}
	// A-3 先合法销毁，供损坏后验证“取回清册”同样整库失败。
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-3", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-3"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetManifest("APP-3"); err != nil || !found {
		t.Fatalf("损坏前清册应可取回: found=%v err=%v", found, err)
	}

	// 把 A-1 的第二条冻结编号改成与第一条相同。
	rewriteState(t, dir, func(doc map[string]any) {
		freezes := doc["archives"].(map[string]any)["A-1"].(map[string]any)["freezes"].([]any)
		freezes[1].(map[string]any)["id"] = "F-1"
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误指出档案与重复的冻结编号。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("冻结历史出现同号记录时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "A-1") ||
		!strings.Contains(err.Error(), "F-1") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出 A-1/F-1，得到 %v", err)
	}

	// 已打开实例：即使本次只操作与重复记录无关的正常档案 A-2，
	// 也必须按整库损坏失败。
	if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后查询无关档案也必须失败且无结论: found=%v err=%v", found, err)
	}
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History 必须失败且不得给出正常冻结历史: found=%v err=%v", found, err)
	}
	if m, found, err := s.GetManifest("APP-3"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("损坏后 GetManifest 必须失败: found=%v err=%v %+v", found, err, m)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出可以办理等结论或部分报告: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
	}
	// 已成功申请的幂等重放同样不能取回原清册。
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-3", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-3"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后已成功申请的重放也必须失败且不得返回原清册: %+v err=%v", m, err)
	}
	if err := s.Register(RegisterInput{
		ID: "A-4", Category: "凭证", Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-06-30"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Register 必须失败: %v", err)
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-2", FreezeID: "F-X", Reason: "诉讼", FrozenOn: MustParseDate("2025-06-01"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Freeze 必须失败: %v", err)
	}
	// 解除申请不得在两条同号记录中挑一条写入原因和日期。
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-08"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Release 不得挑一条同号记录解除: %v", err)
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-9", ArchiveID: "A-2",
		OriginalEnd: MustParseDate("2025-06-30"), NewEnd: MustParseDate("2026-06-30"),
		RevisedOn: MustParseDate("2025-06-01"), Reason: "延期",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Revise 必须失败: %v", err)
	}

	// 失败不得触发修复：不改冻结编号，不删除、合并或自动解除重复记录。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 恢复唯一编号后，合法记录恢复可用：解除只作用于唯一一条 F-1，
	// A-1 的 F-2 仍是未解除阻碍，无关档案业务不受影响。
	rewriteState(t, dir, func(doc map[string]any) {
		freezes := doc["archives"].(map[string]any)["A-1"].(map[string]any)["freezes"].([]any)
		freezes[1].(map[string]any)["id"] = "F-2"
	})
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-08"),
	}); err != nil {
		t.Fatalf("恢复后解除唯一编号 F-1 应成功: %v", err)
	}
	h, found, err := s.History("A-1")
	if err != nil || !found || len(h.Freezes) != 2 {
		t.Fatalf("恢复后完整历史应保留两条冻结: found=%v err=%v %+v", found, err, h)
	}
	if !h.Freezes[0].Released || h.Freezes[0].ReleaseReason != "结案" ||
		h.Freezes[1].Released || h.Freezes[1].ID != "F-2" {
		t.Fatalf("解除应只作用于 F-1，F-2 保持未解除: %+v", h.Freezes)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil || r.Status != CheckBlocked {
		t.Fatalf("恢复后 F-2 仍应逐条作为销毁阻碍: %+v err=%v", r, err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); err != nil {
		t.Fatalf("恢复后无关档案应能正常办理销毁: %v", err)
	}
}

// 唯一性只限定在同一份档案内：不同档案各有一条同编号冻结是合法情况，
// 解除其中一份档案的冻结不能影响另一份；同一档案多条不同编号冻结的完整历史
// 按登记顺序返回，未解除记录逐条作为销毁阻碍，全部合法解除后才按既有到期
// 规则办理销毁。重开后行为不变。
func TestFreezeIDUniquenessIsScopedPerArchive(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	for _, archiveID := range []string{"A-1", "A-2"} {
		if err := s.Freeze(FreezeInput{
			ArchiveID: archiveID, FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05"),
		}); err != nil {
			t.Fatalf("不同档案使用相同冻结编号应合法: %v", err)
		}
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-1", FreezeID: "F-2", Reason: "审计", FrozenOn: MustParseDate("2025-01-06"),
	}); err != nil {
		t.Fatal(err)
	}

	// 只解除 A-1 的 F-1：A-2 的同编号冻结必须保持未解除。
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-07"),
	}); err != nil {
		t.Fatal(err)
	}
	h1, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("查询 A-1 失败: found=%v err=%v", found, err)
	}
	if len(h1.Freezes) != 2 || !h1.Freezes[0].Released || h1.Freezes[0].ID != "F-1" ||
		h1.Freezes[1].ID != "F-2" || h1.Freezes[1].Released {
		t.Fatalf("A-1 历史应按登记顺序保留两条且只解除 F-1: %+v", h1.Freezes)
	}
	if len(h1.ActiveFreezes) != 1 || h1.ActiveFreezes[0].ID != "F-2" {
		t.Fatalf("A-1 应只剩 F-2 未解除: %+v", h1.ActiveFreezes)
	}
	h2, found, err := s.History("A-2")
	if err != nil || !found {
		t.Fatalf("查询 A-2 失败: found=%v err=%v", found, err)
	}
	if len(h2.Freezes) != 1 || h2.Freezes[0].ID != "F-1" || h2.Freezes[0].Released {
		t.Fatalf("A-2 的同编号冻结不应受 A-1 解除影响: %+v", h2.Freezes)
	}

	// A-2 仍被其 F-1 阻挡；A-1 仍被 F-2 阻挡，核对时逐条列出。
	r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"),
		ArchiveIDs: []string{"A-1", "A-2"},
	})
	if err != nil || r.Status != CheckBlocked {
		t.Fatalf("两份档案都应存在未解除冻结阻碍: %+v err=%v", r, err)
	}
	if len(r.Results) != 2 ||
		len(r.Results[0].Obstructions) != 1 || r.Results[0].Obstructions[0].Freeze.FreezeID != "F-2" ||
		len(r.Results[1].Obstructions) != 1 || r.Results[1].Obstructions[0].Freeze.FreezeID != "F-1" {
		t.Fatalf("阻碍应逐条对应各自档案的未解除冻结: %+v", r.Results)
	}

	// 重开后跨档案同编号仍合法，隔离关系保持不变。
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("含跨档案同编号冻结的记录应能重新打开: %v", err)
	}
	defer s2.Close()
	h2b, found, err := s2.History("A-2")
	if err != nil || !found || len(h2b.ActiveFreezes) != 1 || h2b.ActiveFreezes[0].ID != "F-1" {
		t.Fatalf("重开后 A-2 的 F-1 应仍未解除: found=%v err=%v %+v", found, err, h2b.ActiveFreezes)
	}

	// 两份档案的冻结全部合法解除后，才按既有到期规则办理销毁。
	if err := s2.Release(ReleaseInput{
		ArchiveID: "A-1", FreezeID: "F-2", Reason: "审计结束", ReleasedOn: MustParseDate("2025-01-08"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s2.Release(ReleaseInput{
		ArchiveID: "A-2", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-08"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"),
		ArchiveIDs: []string{"A-1", "A-2"},
	}); err != nil {
		t.Fatalf("全部合法解除且到期后应能销毁: %v", err)
	}
}
