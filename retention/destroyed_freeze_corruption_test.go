package retention

import (
	"errors"
	"strings"
	"testing"
)

// 已销毁档案仍带着未解除冻结时，即使清册归属、条目快照与处理日期都合法，
// Open 也必须按保存记录损坏失败（ErrCorruptState）：错误指出档案编号、
// 未解除的冻结编号与所属清册申请编号，原文件保持原样。
func TestOpenRejectsActiveFreezeOnDestroyedArchive(t *testing.T) {
	freezeReleased := func(id string) string {
		return `{"id":"` + id + `","reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-06"}`
	}
	freezeActive := func(id, extra string) string {
		return `{"id":"` + id + `","reason":"诉讼","frozen_on":"2025-01-05","released":false` + extra + `}`
	}
	archive := func(id string, freezes string) string {
		return `"` + id + `":{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1","freezes":[` + freezes + `]}`
	}
	entry := func(id string) string {
		return `{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"2025-01-10"}`
	}
	state := func(archivesJSON, entriesJSON string) string {
		return `{"version":1,"archives":{` + archivesJSON + `},"manifests":{"APP-1":{"application_id":"APP-1","processed_on":"2025-01-10","entries":[` + entriesJSON + `]}}}`
	}

	corrupt := map[string]struct {
		state   string
		wantAll []string // 错误信息必须全部包含
	}{
		"已销毁档案带一条未解除冻结": {
			state(archive("A-1", freezeActive("F-1", "")), entry("A-1")),
			[]string{"A-1", "F-1", "APP-1"},
		},
		"另一条已解除不能抵消未解除的一条": {
			state(archive("A-1", freezeReleased("F-1")+","+freezeActive("F-2", "")), entry("A-1")),
			[]string{"A-1", "F-2", "APP-1"},
		},
		"残留解除日期与原因但仍标记未解除": {
			// 解除与否只看保存的解除标记；残留字段不能把它当作已解除。
			state(archive("A-1",
				freezeActive("F-1", `,"release_reason":"结案","released_on":"2025-01-06"`)),
				entry("A-1")),
			[]string{"A-1", "F-1", "APP-1"},
		},
		"多档案清册中只有第二份仍被冻结": {
			state(archive("A-1", "")+","+archive("A-2", freezeActive("F-2", "")),
				entry("A-1")+","+entry("A-2")),
			[]string{"A-2", "F-2", "APP-1"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("已销毁档案仍带未解除冻结时不应打开成功")
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
			if name == "多档案清册中只有第二份仍被冻结" {
				// 错误必须定位到仍被冻结的第二份，而不是只提第一份。
				if strings.Count(msg, "A-1") > 0 {
					t.Fatalf("不应把已合法解除的 A-1 当作问题条目，得到 %v", err)
				}
			}
			if got := readStateFile(t, dir); got != tc.state {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：已销毁档案没有冻结或全部冻结均已合法解除时可正常打开，
	// 历史与清册完整可查；尚未销毁的档案保留未解除冻结仍是合法状态。
	t.Run("合法记录对照组", func(t *testing.T) {
		valid := map[string]string{
			"已销毁且没有冻结":    state(archive("A-1", ""), entry("A-1")),
			"已销毁且冻结全部已解除": state(archive("A-1", freezeReleased("F-1")), entry("A-1")),
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
				if h, found, err := s.History("A-1"); err != nil || !found || !h.Destroyed || h.Manifest == nil {
					t.Fatalf("已销毁档案的历史与清册应完整可查: found=%v err=%v %+v", found, err, h)
				}
				if _, found, err := s.GetManifest("APP-1"); err != nil || !found {
					t.Fatalf("合法清册应可取回: found=%v err=%v", found, err)
				}
			})
		}

		t.Run("未销毁档案带未解除冻结合法并显示阻碍", func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
			if err := s.Freeze(FreezeInput{
				ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05"),
			}); err != nil {
				t.Fatal(err)
			}
			h, found, err := s.History("A-1")
			if err != nil || !found {
				t.Fatalf("未销毁档案的未解除冻结应能正常查看: found=%v err=%v", found, err)
			}
			if len(h.ActiveFreezes) != 1 || h.ActiveFreezes[0].ID != "F-1" {
				t.Fatalf("历史应显示未解除冻结: %+v", h.ActiveFreezes)
			}
			r, err := s.Check(CheckRequest{
				ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
			})
			if err != nil || r.Status != CheckBlocked ||
				len(r.Results[0].Obstructions) != 1 ||
				r.Results[0].Obstructions[0].Kind != ObstructionActiveFreeze {
				t.Fatalf("销毁前核对应显示冻结阻碍: %+v err=%v", r, err)
			}
			if _, err := s.Destroy(DestructionRequest{
				ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
			}); !errors.Is(err, ErrActiveFreeze) {
				t.Fatalf("未解除冻结仍应阻止销毁，得到 %v", err)
			}
		})
	})
}

// 保管库打开后保存内容才出现“已销毁档案仍带未解除冻结”的矛盾：
// 下一次使用有效输入查询历史、取回清册、销毁前核对与各项办理都必须按整库
// 记录损坏失败——即使操作的是不在该清册中的无关档案——不返回正常历史、
// 清册或部分核对报告，不产生业务变更，原保存内容保持原样。
func TestActiveFreezeOnDestroyedAfterOpenFailsAllOperations(t *testing.T) {
	cases := map[string]func(map[string]any){
		// 只把解除标记改回 false：解除日期与解除原因仍残留在记录里，
		// 但只要仍标记为未解除，就不能把残留字段当作已解除。
		"解除标记被改回未解除且残留解除信息": func(doc map[string]any) {
			freezes := doc["archives"].(map[string]any)["A-1"].(map[string]any)["freezes"].([]any)
			freezes[0].(map[string]any)["released"] = false
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
			reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")
			if err := s.Freeze(FreezeInput{
				ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05"),
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.Release(ReleaseInput{
				ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-06"),
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Destroy(DestructionRequest{
				ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
			}); err != nil {
				t.Fatal(err)
			}
			if _, found, err := s.GetManifest("APP-1"); err != nil || !found {
				t.Fatalf("损坏前清册应可取回: found=%v err=%v", found, err)
			}

			rewriteState(t, dir, mutate)
			corruptContent := readStateFile(t, dir)

			// 重新打开必须失败，错误指出档案、冻结与所属清册申请编号。
			s2, err := Open(dir)
			if err == nil {
				s2.Close()
				t.Fatal("销毁与冻结状态矛盾时，打开必须失败")
			}
			if !errors.Is(err, ErrCorruptState) ||
				!strings.Contains(err.Error(), "A-1") ||
				!strings.Contains(err.Error(), "F-1") ||
				!strings.Contains(err.Error(), "APP-1") {
				t.Fatalf("打开错误应为 ErrCorruptState 并指出 A-1/F-1/APP-1，得到 %v", err)
			}

			// 已打开实例：即使本次只操作与该清册无关的正常档案 A-2，
			// 也必须按整库损坏失败。
			if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
				t.Fatalf("损坏后查询无关档案也必须失败且无结论: found=%v err=%v", found, err)
			}
			if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
				t.Fatalf("损坏后 History 必须失败且不得给出正常销毁历史: found=%v err=%v", found, err)
			}
			if m, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
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
			if err := s.Register(RegisterInput{
				ID: "A-3", Category: "凭证", Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-06-30"),
			}); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("损坏后 Register 必须失败: %v", err)
			}
			if err := s.Freeze(FreezeInput{
				ArchiveID: "A-2", FreezeID: "F-X", Reason: "诉讼", FrozenOn: MustParseDate("2025-06-01"),
			}); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("损坏后 Freeze 必须失败: %v", err)
			}
			if err := s.Release(ReleaseInput{
				ArchiveID: "A-2", FreezeID: "F-X", Reason: "结案", ReleasedOn: MustParseDate("2025-06-02"),
			}); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("损坏后 Release 必须失败: %v", err)
			}
			if _, err := s.Revise(ReviseInput{
				RevisionID: "R-9", ArchiveID: "A-2",
				OriginalEnd: MustParseDate("2025-06-30"), NewEnd: MustParseDate("2026-06-30"),
				RevisedOn: MustParseDate("2025-06-01"), Reason: "延期",
			}); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("损坏后 Revise 必须失败: %v", err)
			}

			// 失败不得触发修复：不自动解除冻结、不删冻结历史、不改销毁标记、不重建清册。
			if got := readStateFile(t, dir); got != corruptContent {
				t.Fatalf("失败后原保存内容被改动:\n%q", got)
			}

			// 恢复解除标记后，合法记录恢复可用：清册可查、相同申请按幂等取回，
			// 无关档案的正常业务不受影响。
			rewriteState(t, dir, func(doc map[string]any) {
				freezes := doc["archives"].(map[string]any)["A-1"].(map[string]any)["freezes"].([]any)
				freezes[0].(map[string]any)["released"] = true
			})
			if replay, err := s.Destroy(DestructionRequest{
				ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
			}); err != nil || replay.ApplicationID != "APP-1" {
				t.Fatalf("恢复后相同申请应取回原清册: %+v err=%v", replay, err)
			}
			if _, err := s.Destroy(DestructionRequest{
				ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
			}); err != nil {
				t.Fatalf("恢复后无关档案应能正常办理销毁: %v", err)
			}
		})
	}
}

// 一份清册收录多份档案，只有其中一份在打开后变成“已销毁但仍被冻结”：
// 查询另一份本来正常的成员、或只核对/办理无关档案，也必须整库失败，
// 不能返回另一份成员的正常历史。
func TestOneContradictoryManifestEntryFailsWholeVaultAfterOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	reg(t, s, "A-3", "单据", "2020-01-01", "2025-01-10")
	for _, in := range []FreezeInput{
		{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")},
		{ArchiveID: "A-2", FreezeID: "F-2", Reason: "协查", FrozenOn: MustParseDate("2025-01-05")},
	} {
		if err := s.Freeze(in); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-06"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-2", FreezeID: "F-2", Reason: "协查结束", ReleasedOn: MustParseDate("2025-01-06"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"),
		ArchiveIDs: []string{"A-1", "A-2"},
	}); err != nil {
		t.Fatal(err)
	}

	// 只把 A-2 的冻结改回未解除：A-1 仍是清册里完全正常的成员，A-3 不在该清册中。
	rewriteState(t, dir, func(doc map[string]any) {
		freezes := doc["archives"].(map[string]any)["A-2"].(map[string]any)["freezes"].([]any)
		freezes[0].(map[string]any)["released"] = false
	})
	corruptContent := readStateFile(t, dir)

	for _, id := range []string{"A-1", "A-2", "A-3"} {
		if h, found, err := s.History(id); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
			t.Fatalf("矛盾与 %s 无关时查询也必须整库失败: found=%v err=%v", id, found, err)
		}
	}
	if _, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("清册查询必须失败: found=%v err=%v", found, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" {
		t.Fatalf("核对清册内正常成员 A-1 也不能给出可以取回/可以办理等结论: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-3"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("办理无关档案 A-3 也必须失败且不得变更: %+v err=%v", m, err)
	}
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}
