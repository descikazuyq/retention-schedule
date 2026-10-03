package retention

import (
	"errors"
	"strings"
	"testing"
)

// 已保存的最初截止日、修订记录与当前截止日必须连续对应：
// 第一条修订的原截止日等于最初截止日，后续每条的原截止日等于上一条的
// 新截止日，最后一条的新截止日等于当前截止日；没有修订时最初截止日与
// 当前截止日相同。出现断开的修订关系、当前截止日与末次修订不符、
// 修订列表中存在空记录，或已有修订却缺少最初截止日时，Open 必须按
// 记录损坏失败（ErrCorruptState），错误信息指出档案编号，能对应到
// 具体修订时同时指出修订编号，原文件保持原样。
func TestOpenRejectsBrokenRevisionChain(t *testing.T) {
	archive := func(initial, end, revisions string) string {
		head := `{"id":"A-1","category":"合同","start":"2020-01-01","end":"` + end + `",`
		if initial != "" {
			head += `"initial_end":"` + initial + `",`
		}
		return head + `"destroyed":false,"freezes":[]` + revisions + `}`
	}
	rev := func(id, oldEnd, newEnd string) string {
		return `{"id":"` + id + `","old_end":"` + oldEnd + `","new_end":"` + newEnd +
			`","revised_on":"2024-12-01","reason":"调整"}`
	}
	state := func(archiveJSON string) string {
		return `{"version":1,"archives":{"A-1":` + archiveJSON + `},"manifests":{}}`
	}

	corrupt := map[string]struct {
		state   string
		wantIDs []string
	}{
		"当前截止日与末次修订不符": {
			state(archive("2025-01-10", "2025-01-10",
				`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10")+`]`)),
			[]string{"A-1", "R-1"},
		},
		"首条修订原截止日不等于最初截止日": {
			state(archive("2025-01-10", "2026-01-10",
				`,"revisions":[`+rev("R-1", "2025-02-01", "2026-01-10")+`]`)),
			[]string{"A-1", "R-1"},
		},
		"相邻修订之间断开": {
			state(archive("2025-01-10", "2027-01-10",
				`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10")+`,`+rev("R-2", "2026-02-01", "2027-01-10")+`]`)),
			[]string{"A-1", "R-2"},
		},
		"修订列表中存在空记录": {
			state(archive("2025-01-10", "2025-01-10", `,"revisions":[null]`)),
			[]string{"A-1"},
		},
		"已有修订却缺少最初截止日": {
			state(archive("", "2026-01-10",
				`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10")+`]`)),
			[]string{"A-1", "R-1"},
		},
		"没有修订但最初截止日与当前截止日不同": {
			state(archive("2025-01-10", "2026-01-10", "")),
			[]string{"A-1"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("修订关系不衔接时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			for _, id := range tc.wantIDs {
				if !strings.Contains(err.Error(), id) {
					t.Fatalf("错误信息应指出 %s，得到 %v", id, err)
				}
			}
			if got := readStateFile(t, dir); got != tc.state {
				t.Fatalf("打开失败后原文件被改动: %q -> %q", tc.state, got)
			}
		})
	}
}

// 期限被延长、缩短或改回早先用过的日期，只要修订衔接完整就应正常读取；
// 修订日期仅用于记录，历史不按该日期重新排列。核对与销毁按当前生效的
// 截止日判断，截止日当天即到期。
func TestValidRevisionChainsKeepWorking(t *testing.T) {
	archive := func(end string, revisions string) string {
		return `{"id":"A-1","category":"合同","start":"2020-01-01","end":"` + end +
			`","initial_end":"2025-01-10","destroyed":false,"freezes":[]` + revisions + `}`
	}
	rev := func(id, oldEnd, newEnd, on string) string {
		return `{"id":"` + id + `","old_end":"` + oldEnd + `","new_end":"` + newEnd +
			`","revised_on":"` + on + `","reason":"调整"}`
	}
	state := func(archiveJSON string) string {
		return `{"version":1,"archives":{"A-1":` + archiveJSON + `},"manifests":{}}`
	}

	chains := map[string]struct {
		state      string
		currentEnd string
		revisions  int
	}{
		"延长": {
			state(archive("2026-01-10", `,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10", "2024-12-01")+`]`)),
			"2026-01-10", 1,
		},
		"缩短": {
			state(archive("2024-06-01", `,"revisions":[`+rev("R-1", "2025-01-10", "2024-06-01", "2024-01-01")+`]`)),
			"2024-06-01", 1,
		},
		"改回早先用过的日期": {
			state(archive("2025-01-10", `,"revisions":[`+
				rev("R-1", "2025-01-10", "2026-01-10", "2024-12-01")+`,`+
				rev("R-2", "2026-01-10", "2025-01-10", "2025-01-05")+`]`)),
			"2025-01-10", 2,
		},
		"修订日期乱序仍按保存顺序衔接": {
			state(archive("2027-01-10", `,"revisions":[`+
				rev("R-1", "2025-01-10", "2026-01-10", "2025-06-01")+`,`+
				rev("R-2", "2026-01-10", "2027-01-10", "2024-12-01")+`]`)),
			"2027-01-10", 2,
		},
	}
	for name, tc := range chains {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err != nil {
				t.Fatalf("衔接完整的修订记录应能打开: %v", err)
			}
			defer s.Close()
			h, found, err := s.History("A-1")
			if err != nil || !found {
				t.Fatalf("历史查询失败: found=%v err=%v", found, err)
			}
			if !h.InitialEnd.Equal(MustParseDate("2025-01-10")) ||
				!h.RetentionEnd.Equal(MustParseDate(tc.currentEnd)) ||
				len(h.Revisions) != tc.revisions {
				t.Fatalf("历史不符: %+v", h)
			}
			// 核对按当前生效的截止日判断，截止日当天即到期。
			r, err := s.Check(CheckRequest{
				ApplicationID: "APP-1",
				ProcessedOn:   MustParseDate(tc.currentEnd),
				ArchiveIDs:    []string{"A-1"},
			})
			if err != nil || r.Status != CheckReady {
				t.Fatalf("当前截止日当天应到期可办: status=%s err=%v", r.Status, err)
			}
		})
	}
}

// 保管库打开后保存内容才出现修订关系矛盾：下一次查询、核对与办理都必须
// 返回 ErrCorruptState，不给出部分历史、核对结论或清册，也不改动原保存
// 内容；即使本次只操作另一份正常档案，同样按整库损坏失败。
func TestRevisionCorruptionAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	revise(t, s, "R-1", "A-1", "2025-01-10", "2026-01-10", "2024-12-01", "延长")
	if _, found, err := s.History("A-1"); err != nil || !found {
		t.Fatalf("损坏前查询应成功: found=%v err=%v", found, err)
	}

	// 把当前截止日改回修订前的值：修订记录已延长到 2026-01-10，
	// 保存的当前截止日却仍是 2025-01-10，两个日期相互矛盾。
	rewriteState(t, dir, func(doc map[string]any) {
		doc["archives"].(map[string]any)["A-1"].(map[string]any)["end"] = "2025-01-10"
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开整个位置必须失败，错误指出 A-1 与 R-1。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("当前截止日与末次修订不符时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "A-1") || !strings.Contains(err.Error(), "R-1") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出 A-1/R-1，得到 %v", err)
	}

	// 已打开的实例：查询异常档案不得给出部分历史或相互矛盾的期限。
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History 必须返回 ErrCorruptState 且无结论: found=%v err=%v", found, err)
	}
	// 只操作另一份正常档案也同样失败：检查针对整个保管库。
	if _, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("查询正常档案 A-2 也应返回 ErrCorruptState: found=%v err=%v", found, err)
	}
	if _, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("损坏后 GetManifest 必须返回 ErrCorruptState: found=%v err=%v", found, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出核对结论: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
	}
	if err := s.Register(RegisterInput{
		ID: "A-3", Category: "合同",
		Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-01-10"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Register 必须失败: %v", err)
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-2", FreezeID: "F-1", Reason: "审计", FrozenOn: MustParseDate("2025-01-07"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Freeze 必须失败: %v", err)
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-2", ArchiveID: "A-2",
		OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
		RevisedOn: MustParseDate("2025-01-07"), Reason: "延期",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Revise 必须失败: %v", err)
	}

	// 失败不得触发重新保存：损坏内容原样保留。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}

// 修订关系矛盾出现在另一份档案（含已销毁档案）身上时，打开与后续办理
// 同样整库失败，不因本次只操作正常档案而放过异常记录。
func TestBrokenRevisionChainOnOtherArchiveFailsWholeVault(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "D-1", "凭证", "2020-01-01", "2025-01-10")
	revise(t, s, "R-1", "D-1", "2025-01-10", "2024-06-01", "2024-01-01", "缩短")
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-D", ProcessedOn: MustParseDate("2024-06-01"), ArchiveIDs: []string{"D-1"},
	}); err != nil {
		t.Fatal(err)
	}

	// 损坏发生在已销毁档案 D-1 身上：两条修订之间断开。
	rewriteState(t, dir, func(doc map[string]any) {
		revisions := doc["archives"].(map[string]any)["D-1"].(map[string]any)["revisions"].([]any)
		revisions[0].(map[string]any)["new_end"] = "2024-07-01"
	})
	corruptContent := readStateFile(t, dir)

	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("已销毁档案的修订关系断开时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "D-1") || !strings.Contains(err.Error(), "R-1") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出 D-1/R-1，得到 %v", err)
	}

	if _, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("查询无关档案 A-1 也应返回 ErrCorruptState: found=%v err=%v", found, err)
	}
	if _, found, err := s.GetManifest("APP-D"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("查询异常档案所属清册也应失败: found=%v err=%v", found, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" {
		t.Fatalf("核对无关档案也应整次失败: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("不得在损坏状态下生成清册: %+v err=%v", m, err)
	}
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}
