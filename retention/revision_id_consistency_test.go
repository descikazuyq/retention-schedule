package retention

import (
	"errors"
	"strings"
	"testing"
)

// 已保存的修订历史必须满足办理时的编号约束：每个成功修订编号在整个保管库
// 内只对应一条保存的修订记录，且不能与已关闭清册的申请编号相同。同一档案
// 历史中重复使用编号、不同档案各自保存同号修订（即使内容完全相同）、
// 修订编号与清册申请编号相同，或修订编号为空白时，即使每份档案的期限
// 日期都能连续对应，Open 也必须按保存记录损坏失败（ErrCorruptState），
// 错误信息指出冲突编号与涉及的档案编号（跨档案时两份都要能看出，
// 涉及清册时要能识别申请编号），原文件保持原样。
func TestOpenRejectsDuplicateSavedRevisionIDs(t *testing.T) {
	archive := func(id, end string, destroyed bool, revisions ...string) string {
		rev := ""
		if len(revisions) > 0 {
			rev = `,"revisions":[` + strings.Join(revisions, ",") + `]`
		}
		destroyedFields := `,"destroyed":false`
		if destroyed {
			destroyedFields = `,"destroyed":true,"manifest_id":"APP-1"`
		}
		return `{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"` + end +
			`","initial_end":"2025-01-10"` + destroyedFields + `,"freezes":[]` + rev + `}`
	}
	rec := func(id, oldEnd, newEnd string) string {
		return `{"id":"` + id + `","old_end":"` + oldEnd + `","new_end":"` + newEnd +
			`","revised_on":"2024-12-01","reason":"调整"}`
	}
	entry := func(id, end string) string {
		return `{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"` + end + `"}`
	}
	state := func(archives, manifests string) string {
		return `{"version":1,"archives":{` + archives + `},"manifests":{` + manifests + `}}`
	}

	corrupt := map[string]struct {
		state   string
		wantIDs []string
	}{
		"同一档案历史中重复使用修订编号": {
			state(
				`"A-1":`+archive("A-1", "2027-01-10", false,
					rec("R-1", "2025-01-10", "2026-01-10"),
					rec("R-1", "2026-01-10", "2027-01-10")),
				"",
			),
			[]string{"R-1", "A-1"},
		},
		"不同档案各自保存同号修订": {
			state(
				`"A-1":`+archive("A-1", "2026-01-10", false, rec("R-1", "2025-01-10", "2026-01-10"))+
					`,"A-2":`+archive("A-2", "2025-06-01", false, rec("R-1", "2025-01-10", "2025-06-01")),
				"",
			),
			[]string{"R-1", "A-1", "A-2"},
		},
		"跨档案同号修订且内容完全相同仍损坏": {
			state(
				`"A-1":`+archive("A-1", "2026-01-10", false, rec("R-1", "2025-01-10", "2026-01-10"))+
					`,"A-2":`+archive("A-2", "2026-01-10", false, rec("R-1", "2025-01-10", "2026-01-10")),
				"",
			),
			[]string{"R-1", "A-1", "A-2"},
		},
		"修订编号与已关闭清册申请编号相同": {
			state(
				`"A-1":`+archive("A-1", "2026-01-10", true, rec("APP-1", "2025-01-10", "2026-01-10")),
				`"APP-1":{"application_id":"APP-1","processed_on":"2026-01-10","entries":[`+entry("A-1", "2026-01-10")+`]}`,
			),
			[]string{"APP-1", "A-1"},
		},
		"已保存修订编号为空白": {
			state(
				`"A-1":`+archive("A-1", "2026-01-10", false, rec("", "2025-01-10", "2026-01-10")),
				"",
			),
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
				t.Fatal("保存历史违反修订编号约束时不应打开成功")
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

// 保管库打开后保存内容才出现跨档案重复修订编号：下一次使用有效输入的
// 历史查询、销毁前核对与各项办理都必须明确报记录损坏——冲突属于整个
// 保管库，不能因为本次选中的档案没有重复编号就返回正常结果；
// 查询失败不能表现成档案不存在，核对失败不能给出部分报告，
// 办理失败不能改变期限、冻结记录或生成清册，原保存内容保持原样。
func TestDuplicateRevisionIDAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	reg(t, s, "A-3", "合同", "2020-01-01", "2025-01-10")
	revise(t, s, "R-1", "A-1", "2025-01-10", "2026-01-10", "2024-12-01", "延长")
	revise(t, s, "R-2", "A-2", "2025-01-10", "2025-06-01", "2024-12-01", "缩短")
	if _, found, err := s.History("A-3"); err != nil || !found {
		t.Fatalf("损坏前查询应成功: found=%v err=%v", found, err)
	}

	// 把 A-2 的修订改成与 A-1 相同的编号：两条记录内容不同，
	// 但即使内容相同结论也一样（见 Open 的同内容用例）。
	rewriteState(t, dir, func(doc map[string]any) {
		doc["archives"].(map[string]any)["A-2"].(map[string]any)["revisions"].([]any)[0].(map[string]any)["id"] = "R-1"
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误同时指出冲突编号与两份档案。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("跨档案重复修订编号时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "R-1") ||
		!strings.Contains(err.Error(), "A-1") || !strings.Contains(err.Error(), "A-2") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出 R-1/A-1/A-2，得到 %v", err)
	}

	// 已打开实例：查询没有重复编号的 A-3 也必须报损坏，且不能表现成不存在。
	if h, found, err := s.History("A-3"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后查询正常档案 A-3 必须返回 ErrCorruptState 且不能像不存在: found=%v err=%v", found, err)
	}
	// 冲突双方档案本身也不得返回正常历史。
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History(A-1) 必须返回 ErrCorruptState: found=%v err=%v", found, err)
	}
	if _, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("损坏后 GetManifest 必须返回 ErrCorruptState: found=%v err=%v", found, err)
	}
	// 核对不能给出部分报告。
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-3"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出部分报告: %+v err=%v", r, err)
	}
	// 办理不能生成清册或改变任何记录。
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-3"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
	}
	if err := s.Register(RegisterInput{
		ID: "A-4", Category: "凭证",
		Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-01-10"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Register 必须失败: %v", err)
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-3", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-07"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Freeze 必须失败: %v", err)
	}
	// 即使使用一个从未用过的全新修订编号，也不能在损坏状态下办理。
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-9", ArchiveID: "A-3",
		OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
		RevisedOn: MustParseDate("2025-01-07"), Reason: "延期",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Revise 必须失败: %v", err)
	}

	// 失败不得触发重新保存：不自动改编号、不删除历史、不重建清册。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}

// 冲突记录所属档案已经销毁时，编号冲突仍然属于整个保管库：
// 查询、核对、办理与冲突记录无关的正常档案同样报记录损坏。
func TestRevisionIDConflictOnDestroyedArchiveFailsWholeVault(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "D-1", "凭证", "2020-01-01", "2025-01-10")
	revise(t, s, "R-D", "D-1", "2025-01-10", "2024-06-01", "2024-01-01", "缩短")
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-D", ProcessedOn: MustParseDate("2024-06-01"), ArchiveIDs: []string{"D-1"},
	}); err != nil {
		t.Fatal(err)
	}

	// 把已销毁档案 D-1 的修订编号改成销毁它的清册申请编号。
	rewriteState(t, dir, func(doc map[string]any) {
		doc["archives"].(map[string]any)["D-1"].(map[string]any)["revisions"].([]any)[0].(map[string]any)["id"] = "APP-D"
	})
	corruptContent := readStateFile(t, dir)

	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("修订编号与已关闭清册申请编号相同时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "APP-D") || !strings.Contains(err.Error(), "D-1") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出 APP-D/D-1，得到 %v", err)
	}

	if _, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("查询无关档案 A-1 也应返回 ErrCorruptState: found=%v err=%v", found, err)
	}
	if _, found, err := s.GetManifest("APP-D"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("查询冲突清册也应失败: found=%v err=%v", found, err)
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

// 合法记录不受新校验影响：不同档案使用各自不同的修订编号、修订与销毁
// 申请编号互不占用、重开后同编号同内容重试仍取回首次记录（期限后来再变、
// 档案已销毁也一样），不增加历史，也不能被误判成保存记录重复。
func TestValidRevisionIDsKeepWorkingAfterReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	first := revise(t, s, "R-1", "A-1", "2025-01-10", "2026-01-10", "2024-12-01", "延长")
	revise(t, s, "R-2", "A-2", "2025-01-10", "2025-06-01", "2024-12-01", "缩短")
	// A-2 销毁后，A-1 又改过一次期限再销毁：同编号重试即使期限再变、
	// 档案已销毁也必须取回首次记录。
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-06-01"), ArchiveIDs: []string{"A-2"},
	}); err != nil {
		t.Fatal(err)
	}
	revise(t, s, "R-3", "A-1", "2026-01-10", "2027-01-10", "2025-01-01", "再延长")
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2027-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("编号互不冲突的合法历史重开后应能打开: %v", err)
	}
	defer s2.Close()

	h1, found, err := s2.History("A-1")
	if err != nil || !found || len(h1.Revisions) != 2 {
		t.Fatalf("A-1 重开后历史不完整: found=%v err=%v %+v", found, err, h1)
	}
	h2, found, err := s2.History("A-2")
	if err != nil || !found || len(h2.Revisions) != 1 {
		t.Fatalf("A-2 重开后历史不完整: found=%v err=%v %+v", found, err, h2)
	}

	// 同编号同内容重试取回首次记录，不增加历史。
	again, err := s2.Revise(ReviseInput{
		RevisionID:  "R-1",
		ArchiveID:   "A-1",
		OriginalEnd: MustParseDate("2025-01-10"),
		NewEnd:      MustParseDate("2026-01-10"),
		RevisedOn:   MustParseDate("2024-12-01"),
		Reason:      "延长",
	})
	if err != nil || again != first {
		t.Fatalf("重开且档案已销毁后，同编号重试仍应取回首次记录: %+v err=%v", again, err)
	}
	h1, _, _ = s2.History("A-1")
	if len(h1.Revisions) != 2 {
		t.Fatalf("重试不应增加历史，得到 %d 条", len(h1.Revisions))
	}

	// 已关闭清册仍可按原申请编号取回。
	m, found, err := s2.GetManifest("APP-2")
	if err != nil || !found || m.ApplicationID != "APP-2" || len(m.Entries) != 1 || m.Entries[0].ID != "A-2" {
		t.Fatalf("清册重开后应完整可查: found=%v err=%v %+v", found, err, m)
	}
}
