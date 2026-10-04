package retention

import (
	"errors"
	"strings"
	"testing"
)

// 已关闭清册必须遵守办理销毁时的到期规则：处理日期必须存在，且不早于
// 所收录任何一份档案在清册中保存的保管截止日（截止日当天即到期）。
// 处理日期早于任一收录档案的截止日，或清册缺少处理日期时，Open 必须按
// 保存记录损坏失败（ErrCorruptState）：日期缺失时指出申请编号，提前销毁时
// 同时指出申请编号、档案编号、处理日期与截止日；一份清册收录多份档案时
// 任一档案未到期即整库拒绝，不能只返回已到期档案的记录，原文件保持原样。
func TestOpenRejectsManifestProcessedBeforeExpiry(t *testing.T) {
	archive := func(id, end string, extra string) string {
		return `{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"` + end +
			`","initial_end":"` + end + `","destroyed":true,"manifest_id":"APP-1","freezes":[]` + extra + `}`
	}
	entry := func(id, end string) string {
		return `{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"` + end + `"}`
	}
	manifest := func(appID, processedOn, entries string) string {
		return `"` + appID + `":{"application_id":"` + appID + `","processed_on":"` + processedOn +
			`","entries":[` + entries + `]}`
	}

	corrupt := map[string]struct {
		state string
		want  []string
	}{
		"处理日期早于截止日一天": {
			`{"version":1,"archives":{"A-1":` + archive("A-1", "2025-01-10", "") +
				`},"manifests":{` + manifest("APP-1", "2025-01-09", entry("A-1", "2025-01-10")) + `}}`,
			[]string{"APP-1", "A-1", "2025-01-09", "2025-01-10"},
		},
		"多档案清册中第二份未到期": {
			// 任务示例：处理日期 2025-06-30，A-1 截止 2025-06-30（当天到期），
			// A-2 截止 2025-07-01（未到期）——整份保管库必须拒绝，
			// 不能只返回 A-1 的正常记录或略过 A-2 的条目。
			`{"version":1,"archives":{"A-1":` + archive("A-1", "2025-06-30", "") +
				`,"A-2":` + archive("A-2", "2025-07-01", "") +
				`},"manifests":{` + manifest("APP-1", "2025-06-30",
				entry("A-1", "2025-06-30")+`,`+entry("A-2", "2025-07-01")) + `}}`,
			[]string{"APP-1", "A-2", "2025-06-30", "2025-07-01"},
		},
		"清册缺少处理日期": {
			// 缺失的处理日期不能当成已经办理的销毁日期。
			`{"version":1,"archives":{"A-1":` + archive("A-1", "2025-01-10", "") +
				`},"manifests":{"APP-1":{"application_id":"APP-1","entries":[` + entry("A-1", "2025-01-10") + `]}}}`,
			[]string{"APP-1"},
		},
		"延长后期限未到期即处理": {
			// 登记 2025-01-10，成功延长到 2025-06-30 后清册保存最终期限，
			// 但处理日期仍是 2025-01-10：按保存到清册里的最终截止日判断，
			// 不能按修订办理日期或最初截止日放行。
			`{"version":1,"archives":{"A-1":` +
				`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-06-30","initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1","freezes":[],"revisions":[{"id":"R-1","old_end":"2025-01-10","new_end":"2025-06-30","revised_on":"2025-01-09","reason":"延期"}]}` +
				`},"manifests":{` + manifest("APP-1", "2025-01-10", entry("A-1", "2025-06-30")) + `}}`,
			[]string{"APP-1", "A-1", "2025-01-10", "2025-06-30"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("清册处理日期缺失或早于收录档案截止日时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("错误信息应指出 %q，得到 %v", want, err)
				}
			}
			if got := readStateFile(t, dir); got != tc.state {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：截止日当天或之后处理的合法清册必须能打开，历史与清册完整可查。
	valid := map[string]string{
		"截止日当天处理": `{"version":1,"archives":{"A-1":` + archive("A-1", "2025-01-10", "") +
			`},"manifests":{` + manifest("APP-1", "2025-01-10", entry("A-1", "2025-01-10")) + `}}`,
		"截止日之后处理": `{"version":1,"archives":{"A-1":` + archive("A-1", "2025-01-10", "") +
			`},"manifests":{` + manifest("APP-1", "2025-02-01", entry("A-1", "2025-01-10")) + `}}`,
		"多档案清册全部到期": `{"version":1,"archives":{"A-1":` + archive("A-1", "2025-06-30", "") +
			`,"A-2":` + archive("A-2", "2025-06-30", "") +
			`},"manifests":{` + manifest("APP-1", "2025-06-30",
			entry("A-1", "2025-06-30")+`,`+entry("A-2", "2025-06-30")) + `}}`,
		"缩短后期限当天处理": `{"version":1,"archives":{"A-1":` +
			`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2024-06-30","initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1","freezes":[],"revisions":[{"id":"R-1","old_end":"2025-01-10","new_end":"2024-06-30","revised_on":"2024-01-05","reason":"提前到期"}]}` +
			`},"manifests":{` + manifest("APP-1", "2024-06-30", entry("A-1", "2024-06-30")) + `}}`,
	}
	for name, state := range valid {
		t.Run("合法/"+name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, state)
			s, err := Open(dir)
			if err != nil {
				t.Fatalf("合法清册应能打开: %v", err)
			}
			defer s.Close()
			h, found, err := s.History("A-1")
			if err != nil || !found || h.Manifest == nil {
				t.Fatalf("合法记录的历史与清册应完整可查: found=%v err=%v", found, err)
			}
			if m, found, err := s.GetManifest("APP-1"); err != nil || !found || m.ApplicationID != "APP-1" {
				t.Fatalf("合法清册应能按申请编号取回: found=%v err=%v %+v", found, err, m)
			}
		})
	}
}

// 保管库打开后保存内容才出现处理日期缺失或提前销毁：下一次使用有效输入的
// 查询、取回清册、销毁前核对与各项办理都必须返回 ErrCorruptState——即使
// 操作的是不在该清册中的档案——不返回正常历史、清册或部分报告，不产生
// 业务变更，原保存内容保持原样（不自动补处理日期，也不改期限、销毁标记
// 或清册条目）。
func TestManifestProcessedOnCorruptionAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-06-30")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-07-01")
	reg(t, s, "A-3", "合同", "2020-01-01", "2025-06-30")
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-07-01"), ArchiveIDs: []string{"A-1", "A-2"},
	}); err != nil {
		t.Fatal(err)
	}
	if h, found, err := s.History("A-1"); err != nil || !found || h.Manifest == nil {
		t.Fatalf("损坏前查询应成功: found=%v err=%v", found, err)
	}

	// 把清册处理日期提前到 2025-06-30：A-1 当天到期，A-2 尚未到期。
	rewriteState(t, dir, func(doc map[string]any) {
		doc["manifests"].(map[string]any)["APP-1"].(map[string]any)["processed_on"] = "2025-06-30"
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误指出申请编号、未到期档案编号、处理日期与截止日。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("清册处理日期早于收录档案截止日时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) {
		t.Fatalf("打开错误应为 ErrCorruptState，得到 %v", err)
	}
	for _, want := range []string{"APP-1", "A-2", "2025-06-30", "2025-07-01"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("打开错误信息应指出 %q，得到 %v", want, err)
		}
	}

	// 已打开实例上的后续操作全部按整库损坏失败，包括不在该清册中的 A-3。
	if h, found, err := s.History("A-3"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后查询无关档案也必须返回 ErrCorruptState 且无结论: found=%v err=%v", found, err)
	}
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History 必须失败且不得给出部分历史: found=%v err=%v", found, err)
	}
	if m, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("损坏后 GetManifest 必须失败: found=%v err=%v %+v", found, err, m)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-3"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出部分报告: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-3"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
	}
	if err := s.Register(RegisterInput{
		ID: "A-4", Category: "凭证", Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-06-30"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Register 必须失败: %v", err)
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-9", ArchiveID: "A-3",
		OriginalEnd: MustParseDate("2025-06-30"), NewEnd: MustParseDate("2026-06-30"),
		RevisedOn: MustParseDate("2025-06-01"), Reason: "延期",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Revise 必须失败: %v", err)
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-3", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-06-01"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Freeze 必须失败: %v", err)
	}

	// 失败不得触发修复或重新保存：提前的处理日期原样保留。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}

// 打开后才出现的清册处理日期缺失同样按整库损坏处理：不能把缺失日期当成
// 已经办理的销毁日期，错误信息指出申请编号，原保存内容保持原样。
func TestManifestMissingProcessedOnAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatal(err)
	}

	// 删掉清册的处理日期字段。
	rewriteState(t, dir, func(doc map[string]any) {
		delete(doc["manifests"].(map[string]any)["APP-1"].(map[string]any), "processed_on")
	})
	corruptContent := readStateFile(t, dir)

	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("清册缺少处理日期时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) || !strings.Contains(err.Error(), "APP-1") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出 APP-1，得到 %v", err)
	}

	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History 必须失败: found=%v err=%v", found, err)
	}
	if m, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("损坏后 GetManifest 必须失败: found=%v err=%v %+v", found, err, m)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" {
		t.Fatalf("损坏后 Check 必须失败: %+v err=%v", r, err)
	}
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}
