package retention

import (
	"errors"
	"strings"
	"testing"
)

// 已关闭清册中的档案条目是销毁成功那一刻登记内容的快照：即使清册归属正确，
// 条目中的类别、起算日或截止日与对应档案保存的登记内容任一项不一致时，
// Open 也必须按保存记录损坏失败（ErrCorruptState），错误信息指出清册申请
// 编号、档案编号与不一致的项目，原文件保持原样。截止日必须是销毁时最终
// 生效的期限：修订衔接完整但清册保存成最初登记截止日的记录同样判为损坏。
func TestOpenRejectsManifestSnapshotMismatch(t *testing.T) {
	archive := func(id, category, start, end, initial string, extra string) string {
		return `{"id":"` + id + `","category":"` + category + `","start":"` + start + `","end":"` + end +
			`","initial_end":"` + initial + `","destroyed":true,"manifest_id":"APP-1","freezes":[]` + extra + `}`
	}
	entry := func(id, category, start, end string) string {
		return `{"id":"` + id + `","category":"` + category + `","start":"` + start + `","end":"` + end + `"}`
	}
	manifest := func(appID, processedOn, entries string) string {
		return `"` + appID + `":{"application_id":"` + appID + `","processed_on":"` + processedOn +
			`","entries":[` + entries + `]}`
	}
	state := func(archiveJSON, appID, processedOn, entries string) string {
		return `{"version":1,"archives":{"A-1":` + archiveJSON + `},"manifests":{` +
			manifest(appID, processedOn, entries) + `}}`
	}

	a1Simple := archive("A-1", "合同", "2020-01-01", "2025-01-10", "2025-01-10", "")
	// 登记 2025-01-10，成功延长到 2025-06-30 后销毁：修订链衔接完整，
	// 当前（销毁时最终生效）截止日为 2025-06-30。
	a1Extended := archive("A-1", "合同", "2020-01-01", "2025-06-30", "2025-01-10",
		`,"revisions":[{"id":"R-1","old_end":"2025-01-10","new_end":"2025-06-30","revised_on":"2025-01-09","reason":"延期"}]`)

	corrupt := map[string]struct {
		state string
		want  []string
	}{
		"类别不一致": {
			state(a1Simple, "APP-1", "2025-01-10", entry("A-1", "凭证", "2020-01-01", "2025-01-10")),
			[]string{"APP-1", "A-1", "类别", "合同", "凭证"},
		},
		"起算日不一致": {
			state(a1Simple, "APP-1", "2025-01-10", entry("A-1", "合同", "2020-02-01", "2025-01-10")),
			[]string{"APP-1", "A-1", "起算日", "2020-02-01", "2020-01-01"},
		},
		"截止日不一致": {
			state(a1Simple, "APP-1", "2025-01-10", entry("A-1", "合同", "2020-01-01", "2024-12-31")),
			[]string{"APP-1", "A-1", "保管截止日", "2024-12-31", "2025-01-10"},
		},
		"三项同时不一致": {
			state(a1Simple, "APP-1", "2025-01-10", entry("A-1", "凭证", "2020-02-01", "2024-12-31")),
			[]string{"APP-1", "A-1", "类别", "起算日", "保管截止日"},
		},
		"延长后销毁但清册保存最初登记截止日": {
			state(a1Extended, "APP-1", "2025-06-30", entry("A-1", "合同", "2020-01-01", "2025-01-10")),
			[]string{"APP-1", "A-1", "保管截止日", "2025-01-10", "2025-06-30"},
		},
		"档案侧截止日被改动而清册未变": {
			// 清册保存的最终期限 2025-06-30 合法，但档案当前截止日被改成 2026-01-10：
			// 不挑选任一处作为可信记录，方向与上例相反也必须拒绝。
			state(archive("A-1", "合同", "2020-01-01", "2026-01-10", "2025-01-10",
				`,"revisions":[{"id":"R-1","old_end":"2025-01-10","new_end":"2025-06-30","revised_on":"2025-01-09","reason":"延期"}]`),
				"APP-1", "2025-06-30", entry("A-1", "合同", "2020-01-01", "2025-06-30")),
			[]string{"APP-1", "A-1", "保管截止日", "2025-06-30", "2026-01-10"},
		},
		"多档案清册中第二份条目类别矛盾": {
			`{"version":1,"archives":{` + `"A-1":` + a1Simple + `,"A-2":` +
				archive("A-2", "凭证", "2020-01-01", "2025-01-10", "2025-01-10", "") +
				`},"manifests":{` + manifest("APP-1", "2025-01-10",
				entry("A-1", "合同", "2020-01-01", "2025-01-10")+`,`+entry("A-2", "合同", "2020-01-01", "2025-01-10")) + `}}`,
			[]string{"APP-1", "A-2", "类别"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("清册条目与档案登记内容不一致时不应打开成功")
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

	// 对照组：销毁时最终生效期限与清册条目一致（含修订后销毁）必须能打开。
	t.Run("修订后最终期限与清册一致可打开", func(t *testing.T) {
		valid := state(a1Extended, "APP-1", "2025-06-30", entry("A-1", "合同", "2020-01-01", "2025-06-30"))
		dir := t.TempDir()
		writeStateFile(t, dir, valid)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("清册保存销毁时最终生效截止日的记录应能打开: %v", err)
		}
		defer s.Close()
		h, found, err := s.History("A-1")
		if err != nil || !found || h.Manifest == nil {
			t.Fatalf("合法记录的历史与清册应完整可查: found=%v err=%v", found, err)
		}
		if !h.Manifest.Entries[0].End.Equal(MustParseDate("2025-06-30")) {
			t.Fatalf("清册条目应保存最终生效截止日 2025-06-30: %+v", h.Manifest.Entries[0])
		}
	})
}

// 正常销毁生成的清册必须与档案登记内容逐项一致：延长、缩短或改回早先用过的
// 日期后销毁，清册保存的都是销毁时最终生效的截止日；重开后历史、清册查询、
// 相同申请取回原清册与销毁前核对均保持原有行为。
func TestValidDestroyedSnapshotsKeepWorking(t *testing.T) {
	t.Run("延长后销毁保存最终期限", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
		if _, err := s.Revise(ReviseInput{
			RevisionID: "R-1", ArchiveID: "A-1",
			OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2025-06-30"),
			RevisedOn: MustParseDate("2025-01-09"), Reason: "延期",
		}); err != nil {
			t.Fatal(err)
		}
		req := DestructionRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-1"},
		}
		m, err := s.Destroy(req)
		if err != nil {
			t.Fatal(err)
		}
		if !m.Entries[0].End.Equal(MustParseDate("2025-06-30")) {
			t.Fatalf("清册应保存销毁时最终生效截止日 2025-06-30: %+v", m.Entries[0])
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}

		s2, err := Open(dir)
		if err != nil {
			t.Fatalf("合法记录重开应成功: %v", err)
		}
		defer s2.Close()
		h, found, err := s2.History("A-1")
		if err != nil || !found || h.Manifest == nil || len(h.Revisions) != 1 {
			t.Fatalf("重开后历史与清册应完整: found=%v err=%v %+v", found, err, h)
		}
		if !h.Manifest.Entries[0].End.Equal(MustParseDate("2025-06-30")) ||
			!h.InitialEnd.Equal(MustParseDate("2025-01-10")) ||
			!h.RetentionEnd.Equal(MustParseDate("2025-06-30")) {
			t.Fatalf("重开后最初/最终截止日与清册快照不正确: %+v", h)
		}
		got, found, err := s2.GetManifest("APP-1")
		if err != nil || !found || !got.Entries[0].End.Equal(MustParseDate("2025-06-30")) {
			t.Fatalf("按申请编号取回的清册应保存最终期限: found=%v err=%v %+v", found, err, got)
		}
		// 相同申请取回原清册的幂等行为不变。
		replay, err := s2.Destroy(req)
		if err != nil || replay.ApplicationID != "APP-1" || !replay.Entries[0].End.Equal(MustParseDate("2025-06-30")) {
			t.Fatalf("相同申请应取回原清册: %+v err=%v", replay, err)
		}
		r, err := s2.Check(CheckRequest(req))
		if err != nil || r.Status != CheckReplayable || r.Manifest == nil {
			t.Fatalf("相同申请核对应显示可以取回原清册: %+v err=%v", r, err)
		}
	})

	t.Run("缩短后销毁保存缩短后的期限", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
		if _, err := s.Revise(ReviseInput{
			RevisionID: "R-1", ArchiveID: "A-1",
			OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2024-06-30"),
			RevisedOn: MustParseDate("2024-01-05"), Reason: "提前到期",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Destroy(DestructionRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2024-06-30"), ArchiveIDs: []string{"A-1"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		s2, err := Open(dir)
		if err != nil {
			t.Fatalf("缩短后销毁的合法记录重开应成功: %v", err)
		}
		defer s2.Close()
		h, found, err := s2.History("A-1")
		if err != nil || !found || !h.Manifest.Entries[0].End.Equal(MustParseDate("2024-06-30")) {
			t.Fatalf("清册应保存缩短后生效的截止日 2024-06-30: found=%v err=%v %+v", found, err, h)
		}
	})

	t.Run("改回早先用过的日期后销毁仍合法", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
		if _, err := s.Revise(ReviseInput{
			RevisionID: "R-1", ArchiveID: "A-1",
			OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
			RevisedOn: MustParseDate("2025-01-02"), Reason: "延期",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Revise(ReviseInput{
			RevisionID: "R-2", ArchiveID: "A-1",
			OriginalEnd: MustParseDate("2026-01-10"), NewEnd: MustParseDate("2025-01-10"),
			RevisedOn: MustParseDate("2025-01-03"), Reason: "改回",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Destroy(DestructionRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		s2, err := Open(dir)
		if err != nil {
			t.Fatalf("改回日期后销毁的合法记录重开应成功: %v", err)
		}
		defer s2.Close()
		h, found, err := s2.History("A-1")
		if err != nil || !found {
			t.Fatalf("历史查询失败: found=%v err=%v", found, err)
		}
		if len(h.Revisions) != 2 ||
			!h.Manifest.Entries[0].End.Equal(MustParseDate("2025-01-10")) ||
			!h.RetentionEnd.Equal(MustParseDate("2025-01-10")) {
			t.Fatalf("最终生效期限为改回后的 2025-01-10，清册应照此保存: %+v", h)
		}
	})
}

// 保管库打开后保存内容才出现清册条目与档案登记内容不一致：下一次使用有效输入
// 的查询、销毁前核对与各项办理都必须返回 ErrCorruptState——即使操作的是不在
// 该清册中的无关档案——不返回正常历史、清册或部分报告，不产生业务变更，
// 原保存内容保持原样。
func TestManifestSnapshotMismatchAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")
	// A-1 成功延长到 2025-06-30 后于该日销毁，清册合法保存 2025-06-30。
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-1", ArchiveID: "A-1",
		OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2025-06-30"),
		RevisedOn: MustParseDate("2025-01-09"), Reason: "延期",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatal(err)
	}
	if m, found, err := s.GetManifest("APP-1"); err != nil || !found ||
		!m.Entries[0].End.Equal(MustParseDate("2025-06-30")) {
		t.Fatalf("损坏前清册应保存最终期限 2025-06-30: found=%v err=%v %+v", found, err, m)
	}

	// 把清册条目里的截止日改回最初登记的 2025-01-10：
	// 修订链衔接完整、清册归属正确，但两处截止日互相矛盾。
	rewriteState(t, dir, func(doc map[string]any) {
		entries := doc["manifests"].(map[string]any)["APP-1"].(map[string]any)["entries"].([]any)
		entries[0].(map[string]any)["end"] = "2025-01-10"
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误指出清册申请编号、档案编号与不一致的保管截止日。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("清册保存最初登记截止日而非最终生效截止日时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "APP-1") ||
		!strings.Contains(err.Error(), "A-1") ||
		!strings.Contains(err.Error(), "保管截止日") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出 APP-1/A-1/保管截止日，得到 %v", err)
	}

	// 已打开实例上的后续操作全部按整库损坏失败，包括与该清册无关的 A-2。
	if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后查询无关档案也必须返回 ErrCorruptState 且无结论: found=%v err=%v", found, err)
	}
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History 必须失败且不得给出矛盾历史: found=%v err=%v", found, err)
	}
	if m, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("损坏后 GetManifest 必须失败: found=%v err=%v %+v", found, err, m)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出部分报告: %+v err=%v", r, err)
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
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-9", ArchiveID: "A-2",
		OriginalEnd: MustParseDate("2025-06-30"), NewEnd: MustParseDate("2026-06-30"),
		RevisedOn: MustParseDate("2025-06-01"), Reason: "延期",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Revise 必须失败: %v", err)
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-2", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-06-01"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Freeze 必须失败: %v", err)
	}

	// 失败不得触发修复或重新保存：矛盾的清册条目原样保留。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}

// 一份清册收录多份档案时，其中一份条目与档案矛盾即意味着整库读取失败：
// 即使另一份条目完全一致，也不能返回它的正常历史、清册或核对结果。
func TestOneBadManifestEntryFailsReadsOfOtherEntries(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-06-30")
	reg(t, s, "A-3", "凭证", "2020-01-01", "2025-06-30")
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-1", "A-3"},
	}); err != nil {
		t.Fatal(err)
	}

	// 只把 A-1 的类别改坏，A-3 的条目保持一致。
	rewriteState(t, dir, func(doc map[string]any) {
		entries := doc["manifests"].(map[string]any)["APP-2"].(map[string]any)["entries"].([]any)
		for _, e := range entries {
			if e.(map[string]any)["id"] == "A-1" {
				e.(map[string]any)["category"] = "凭证"
			}
		}
	})
	corruptContent := readStateFile(t, dir)

	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("一份清册中任一条目矛盾时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "APP-2") ||
		!strings.Contains(err.Error(), "A-1") ||
		!strings.Contains(err.Error(), "类别") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出 APP-2/A-1/类别，得到 %v", err)
	}

	// 已打开实例：条目完好的 A-3 同样读不到正常结果。
	if h, found, err := s.History("A-3"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("同册另一份条目完好也不得返回正常历史: found=%v err=%v", found, err)
	}
	if m, found, err := s.GetManifest("APP-2"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("同册任一条目矛盾时整份清册都不可取回: found=%v err=%v %+v", found, err, m)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-8", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-3"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" {
		t.Fatalf("核对条目完好的另一份档案也必须整次失败: %+v err=%v", r, err)
	}
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}
