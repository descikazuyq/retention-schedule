package retention

import (
	"errors"
	"strings"
	"testing"
)

// 已关闭清册记录的是每份档案销毁成功那一刻的登记内容：即使清册归属关系
// （已销毁档案与清册双向对应）完全正确，只要清册条目中的类别、起算日或
// 保管截止日与档案记录任一项不一致，Open 也必须按保存记录损坏失败
// （ErrCorruptState），错误信息指出清册申请编号、档案编号与不一致的项目，
// 绝不挑选其中一份记录继续使用，原文件保持原样。
func TestOpenRejectsManifestEntryContentMismatch(t *testing.T) {
	archive := func(id, category, start, end, initial string, destroyed bool, manifestID, revisions string) string {
		destroyedFields := `,"destroyed":false`
		if destroyed {
			destroyedFields = `,"destroyed":true,"manifest_id":"` + manifestID + `"`
		}
		return `{"id":"` + id + `","category":"` + category + `","start":"` + start +
			`","end":"` + end + `","initial_end":"` + initial + `","freezes":[]` +
			destroyedFields + revisions + `}`
	}
	rev := func(id, oldEnd, newEnd string) string {
		return `,"revisions":[{"id":"` + id + `","old_end":"` + oldEnd +
			`","new_end":"` + newEnd + `","revised_on":"2025-06-01","reason":"调整"}]`
	}
	entry := func(id, category, start, end string) string {
		return `{"id":"` + id + `","category":"` + category +
			`","start":"` + start + `","end":"` + end + `"}`
	}
	manifest := func(appID, processedOn, entries string) string {
		return `"` + appID + `":{"application_id":"` + appID +
			`","processed_on":"` + processedOn + `","entries":[` + entries + `]}`
	}
	state := func(archives, manifests string) string {
		return `{"version":1,"archives":{` + archives + `},"manifests":{` + manifests + `}}`
	}

	// 清册保存最终生效截止日时合法的对照组，与下面“保存最初截止日”的
	// 损坏用例只差条目的 end 一个字段。
	extendedArchive := archive("A-1", "合同", "2020-01-01", "2025-06-30", "2025-01-10",
		true, "APP-1", rev("R-1", "2025-01-10", "2025-06-30"))

	corrupt := map[string]struct {
		state     string
		appID     string
		archiveID string
		wantItems []string
	}{
		"类别不一致": {
			state(
				`"A-1":`+archive("A-1", "合同", "2020-01-01", "2025-01-10", "2025-01-10", true, "APP-1", ""),
				manifest("APP-1", "2025-01-10", entry("A-1", "凭证", "2020-01-01", "2025-01-10")),
			),
			"APP-1", "A-1", []string{"类别"},
		},
		"起算日不一致": {
			state(
				`"A-1":`+archive("A-1", "合同", "2020-01-01", "2025-01-10", "2025-01-10", true, "APP-1", ""),
				manifest("APP-1", "2025-01-10", entry("A-1", "合同", "2020-02-01", "2025-01-10")),
			),
			"APP-1", "A-1", []string{"起算日"},
		},
		"截止日不一致": {
			state(
				`"A-1":`+archive("A-1", "合同", "2020-01-01", "2025-01-10", "2025-01-10", true, "APP-1", ""),
				manifest("APP-1", "2025-01-10", entry("A-1", "合同", "2020-01-01", "2024-01-10")),
			),
			"APP-1", "A-1", []string{"保管截止日"},
		},
		"延长后销毁却保存最初截止日": {
			// 最初截止日 2025-01-10，成功延长到 2025-06-30 后完成销毁：
			// 修订衔接完整、清册归属正确，但条目保存 2025-01-10 仍属损坏。
			state(
				`"A-1":`+extendedArchive,
				manifest("APP-1", "2025-06-30", entry("A-1", "合同", "2020-01-01", "2025-01-10")),
			),
			"APP-1", "A-1", []string{"保管截止日"},
		},
		"缩短后销毁却保存最初截止日": {
			state(
				`"A-1":`+archive("A-1", "合同", "2020-01-01", "2025-01-10", "2025-06-30",
					true, "APP-1", rev("R-1", "2025-06-30", "2025-01-10")),
				manifest("APP-1", "2025-01-10", entry("A-1", "合同", "2020-01-01", "2025-06-30")),
			),
			"APP-1", "A-1", []string{"保管截止日"},
		},
		"改回早先用过的日期后销毁却保存最初截止日": {
			// 2025-01-10 → 2025-06-30 → 2025-01-10，最终生效值是
			// 2025-01-10；条目保存中间用过的 2025-06-30 同样不合法。
			state(
				`"A-1":`+archive("A-1", "合同", "2020-01-01", "2025-01-10", "2025-01-10",
					true, "APP-1",
					`,"revisions":[{"id":"R-1","old_end":"2025-01-10","new_end":"2025-06-30","revised_on":"2025-05-01","reason":"延长"},`+
						`{"id":"R-2","old_end":"2025-06-30","new_end":"2025-01-10","revised_on":"2025-06-01","reason":"改回"}]`),
				manifest("APP-1", "2025-01-10", entry("A-1", "合同", "2020-01-01", "2025-06-30")),
			),
			"APP-1", "A-1", []string{"保管截止日"},
		},
		"三项同时不一致": {
			state(
				`"A-1":`+archive("A-1", "合同", "2020-01-01", "2025-01-10", "2025-01-10", true, "APP-1", ""),
				manifest("APP-1", "2025-01-10", entry("A-1", "凭证", "2020-02-01", "2024-01-10")),
			),
			"APP-1", "A-1", []string{"类别", "起算日", "保管截止日"},
		},
		"清册收录多份档案时一份矛盾": {
			state(
				`"A-1":`+archive("A-1", "合同", "2020-01-01", "2025-01-10", "2025-01-10", true, "APP-1", "")+
					`,"A-2":`+archive("A-2", "凭证", "2020-01-01", "2025-01-10", "2025-01-10", true, "APP-1", ""),
				manifest("APP-1", "2025-01-10",
					entry("A-1", "合同", "2020-01-01", "2025-01-10")+`,`+
						entry("A-2", "账簿", "2020-01-01", "2025-01-10")),
			),
			"APP-1", "A-2", []string{"类别"},
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
			msg := err.Error()
			for _, want := range append(tc.wantItems, tc.appID, tc.archiveID) {
				if !strings.Contains(msg, want) {
					t.Fatalf("错误信息应指出 %s，得到 %v", want, err)
				}
			}
			if got := readStateFile(t, dir); got != tc.state {
				t.Fatalf("打开失败后原文件被改动: %q -> %q", tc.state, got)
			}
		})
	}

	// 对照组：同样经过延长，清册保存销毁时最终生效的截止日即合法，
	// 历史与清册完整可查。
	t.Run("保存最终生效截止日合法", func(t *testing.T) {
		dir := t.TempDir()
		valid := state(
			`"A-1":`+extendedArchive,
			manifest("APP-1", "2025-06-30", entry("A-1", "合同", "2020-01-01", "2025-06-30")),
		)
		writeStateFile(t, dir, valid)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("清册条目与档案一致时应能打开: %v", err)
		}
		defer s.Close()
		m, found, err := s.GetManifest("APP-1")
		if err != nil || !found || len(m.Entries) != 1 ||
			!m.Entries[0].End.Equal(MustParseDate("2025-06-30")) {
			t.Fatalf("合法清册应完整可查: found=%v err=%v %+v", found, err, m)
		}
	})
}

// 保管库打开后保存内容才出现清册条目与档案不一致（模拟为延长销毁后条目
// 被改成最初截止日）：下一次使用有效输入的查询、销毁前核对与各项办理都
// 必须返回 ErrCorruptState——不给出正常历史、清册或部分报告，不产生任何
// 业务变更，即使本次操作的档案不在那份清册中也一样；原保存内容保持原样。
func TestManifestEntryMismatchAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	// A-1 成功延长到 2025-06-30，并在最终截止日当天完成销毁。
	revise(t, s, "R-1", "A-1", "2025-01-10", "2025-06-30", "2025-06-01", "延长")
	m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Entries[0].End.Equal(MustParseDate("2025-06-30")) {
		t.Fatalf("正常销毁的清册应保存最终生效截止日: %+v", m)
	}

	// 把清册条目截止日改回最初登记的 2025-01-10：归属关系不变，
	// 但两处对销毁时期限的记录相互矛盾。
	rewriteState(t, dir, func(doc map[string]any) {
		entries := doc["manifests"].(map[string]any)["APP-1"].(map[string]any)["entries"].([]any)
		entries[0].(map[string]any)["end"] = "2025-01-10"
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误指出清册 APP-1、档案 A-1 与保管截止日。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("清册保存最初截止日而档案为最终截止日时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "APP-1") ||
		!strings.Contains(err.Error(), "A-1") ||
		!strings.Contains(err.Error(), "保管截止日") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出 APP-1/A-1/保管截止日，得到 %v", err)
	}

	// 已打开的实例：异常档案本身查不到正常历史与原清册。
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History 必须返回 ErrCorruptState 且无结论: found=%v err=%v", found, err)
	}
	if _, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("损坏后 GetManifest 必须返回 ErrCorruptState: found=%v err=%v", found, err)
	}
	// 即使只操作不在该清册中的正常档案 A-2，也按整库损坏失败。
	if _, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("查询无关档案 A-2 也应返回 ErrCorruptState: found=%v err=%v", found, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出部分报告: %+v err=%v", r, err)
	}
	if out, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || out.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", out, err)
	}
	if err := s.Register(RegisterInput{
		ID: "A-3", Category: "凭证",
		Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-01-10"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Register 必须失败: %v", err)
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-2", FreezeID: "F-1", Reason: "审计", FrozenOn: MustParseDate("2025-06-10"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Freeze 必须失败: %v", err)
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-2", ArchiveID: "A-2",
		OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
		RevisedOn: MustParseDate("2025-06-10"), Reason: "延期",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Revise 必须失败: %v", err)
	}

	// 失败不得触发修复：不覆盖清册、不改档案、不删除条目，矛盾内容原样保留。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}

// 正常销毁生成的清册必须保存销毁时最终生效的截止日：延长、缩短或改回
// 早先用过的日期都按最后一次成功修订的新截止日落盘，相同申请重放取回
// 同一份原清册，重新打开后历史仍完整可查。
func TestValidDestroyManifestKeepsFinalEndSnapshot(t *testing.T) {
	cases := map[string]struct {
		initial string
		steps   [][2]string // 每次修订：原截止日 → 新截止日
	}{
		"延长": {"2025-01-10", [][2]string{{"2025-01-10", "2025-06-30"}}},
		"缩短": {"2025-06-30", [][2]string{{"2025-06-30", "2025-01-10"}}},
		"改回早先用过的日期": {
			"2025-01-10",
			[][2]string{{"2025-01-10", "2025-06-30"}, {"2025-06-30", "2025-01-10"}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			reg(t, s, "A-1", "合同", "2020-01-01", tc.initial)
			finalEnd := tc.initial
			for i, step := range tc.steps {
				revise(t, s, revisionID(i), "A-1", step[0], step[1], "2025-01-0"+string(rune('1'+i)), "调整")
				finalEnd = step[1]
			}
			m1, err := s.Destroy(DestructionRequest{
				ApplicationID: "APP-1", ProcessedOn: MustParseDate(finalEnd), ArchiveIDs: []string{"A-1"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if !m1.Entries[0].End.Equal(MustParseDate(finalEnd)) {
				t.Fatalf("清册应保存最终生效截止日 %s，得到 %s", finalEnd, m1.Entries[0].End)
			}
			// 相同申请重放取回同一份原清册，内容不变。
			m2, err := s.Destroy(DestructionRequest{
				ApplicationID: "APP-1", ProcessedOn: MustParseDate(finalEnd), ArchiveIDs: []string{"A-1"},
			})
			if err != nil || !m2.Entries[0].End.Equal(MustParseDate(finalEnd)) {
				t.Fatalf("重放应取回原清册: %+v err=%v", m2, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}

			// 重新打开：合法记录不受新校验影响，历史中的清册快照仍是最终截止日。
			s2, err := Open(dir)
			if err != nil {
				t.Fatalf("合法销毁记录应能重新打开: %v", err)
			}
			defer s2.Close()
			h, found, err := s2.History("A-1")
			if err != nil || !found || h.Manifest == nil {
				t.Fatalf("重新打开后历史与清册应完整可查: found=%v err=%v", found, err)
			}
			if !h.Manifest.Entries[0].End.Equal(MustParseDate(finalEnd)) {
				t.Fatalf("历史中清册应保存最终生效截止日 %s，得到 %s",
					finalEnd, h.Manifest.Entries[0].End)
			}
		})
	}
}

// revisionID 生成测试用修订编号 R-1、R-2……
func revisionID(i int) string {
	return "R-" + string(rune('1'+i))
}
