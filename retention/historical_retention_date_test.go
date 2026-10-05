package retention

import (
	"errors"
	"strings"
	"testing"
)

// 保存内容中任一档案的历史期限日期（最初截止日、每条修订的原截止日与新
// 截止日）早于该档案起算日时，Open 必须按记录损坏处理：返回
// ErrCorruptState，错误信息指出档案编号、出错的期限位置、该截止日与
// 起算日（修订记录中同时指出修订编号），原文件保持原样。当前截止日合法、
// 修订前后完全衔接，或已关闭清册的归属、条目与处理日期都正确，都不能
// 掩盖历史中途出现过的非法期限。
func TestOpenRejectsHistoricalRetentionDatesBeforeStart(t *testing.T) {
	archive := func(initial, end, revisions string) string {
		return `{"id":"A-1","category":"合同","start":"2020-01-01","end":"` + end +
			`","initial_end":"` + initial + `","destroyed":false,"freezes":[]` + revisions + `}`
	}
	rev := func(id, oldEnd, newEnd string) string {
		return `{"id":"` + id + `","old_end":"` + oldEnd + `","new_end":"` + newEnd +
			`","revised_on":"2024-12-01","reason":"调整"}`
	}
	state := func(archiveJSON string) string {
		return `{"version":1,"archives":{"A-1":` + archiveJSON + `},"manifests":{}}`
	}

	corrupt := map[string]struct {
		content string
		want    []string // 错误信息必须包含的片段
	}{
		"最初截止日早于起算日": {
			// 最初期限本身非法，后来通过修订改到合法日期也不能接受。
			content: state(archive("2019-12-31", "2025-01-10",
				`,"revisions":[`+rev("R-1", "2019-12-31", "2025-01-10")+`]`)),
			want: []string{"A-1", "最初", "2019-12-31", "2020-01-01"},
		},
		"首条修订新截止日早于起算日": {
			// 两条修订前后完全衔接，当前截止日等于末次修订的新值，
			// 但中途曾缩短到起算日之前。
			content: state(archive("2025-01-10", "2026-01-10",
				`,"revisions":[`+rev("R-1", "2025-01-10", "2019-12-31")+`,`+
					rev("R-2", "2019-12-31", "2026-01-10")+`]`)),
			want: []string{"A-1", "R-1", "2019-12-31", "2020-01-01"},
		},
		"中间修订新截止日早于起算日": {
			content: state(archive("2025-01-10", "2027-01-10",
				`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10")+`,`+
					rev("R-2", "2026-01-10", "2019-12-31")+`,`+
					rev("R-3", "2019-12-31", "2027-01-10")+`]`)),
			want: []string{"A-1", "R-2", "2019-12-31", "2020-01-01"},
		},
		"已销毁档案历史期限早于起算日": {
			// 清册归属、条目快照与处理日期全部正确，也不能掩盖历史中的
			// 非法期限。
			content: `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2026-01-10","initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1","freezes":[],"revisions":[` +
				rev("R-1", "2025-01-10", "2019-12-31") + `,` +
				rev("R-2", "2019-12-31", "2026-01-10") +
				`]}},"manifests":{"APP-1":{"application_id":"APP-1","processed_on":"2026-01-10","entries":[{"id":"A-1","category":"合同","start":"2020-01-01","end":"2026-01-10"}]}}}`,
			want: []string{"A-1", "R-1", "2019-12-31", "2020-01-01"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.content)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("历史期限日期早于起算日时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("错误信息应包含 %q，得到 %v", want, err)
				}
			}
			if got := readStateFile(t, dir); got != tc.content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}
}

// 合法的历史期限不受新校验影响：历史中的截止日等于起算日合法；缩短到
// 起算日再延长、改回早先用过的日期，只要衔接完整且不曾早于起算日就应
// 正常读取，核对仍按最终生效的截止日判断，截止日当天即到期。
func TestValidHistoricalRetentionDatesKeepWorking(t *testing.T) {
	t.Run("历史截止日等于起算日合法", func(t *testing.T) {
		dir := t.TempDir()
		writeStateFile(t, dir, `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2020-01-01","destroyed":false,"freezes":[],"revisions":[{"id":"R-1","old_end":"2020-01-01","new_end":"2025-01-10","revised_on":"2024-12-01","reason":"延期"}]}},"manifests":{}}`)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("历史截止日等于起算日应能打开: %v", err)
		}
		defer s.Close()
		h, found, err := s.History("A-1")
		if err != nil || !found {
			t.Fatalf("历史查询失败: found=%v err=%v", found, err)
		}
		if !h.InitialEnd.Equal(MustParseDate("2020-01-01")) ||
			!h.RetentionEnd.Equal(MustParseDate("2025-01-10")) ||
			len(h.Revisions) != 1 {
			t.Fatalf("历史不符: %+v", h)
		}
	})

	t.Run("缩短到起算日再延长合法", func(t *testing.T) {
		dir := t.TempDir()
		writeStateFile(t, dir, `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2026-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[],"revisions":[{"id":"R-1","old_end":"2025-01-10","new_end":"2020-01-01","revised_on":"2024-01-01","reason":"缩短"},{"id":"R-2","old_end":"2020-01-01","new_end":"2026-01-10","revised_on":"2024-12-01","reason":"延长"}]}},"manifests":{}}`)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("缩短到起算日再延长的历史应能打开: %v", err)
		}
		defer s.Close()
		// 销毁是否到期仍依据最终生效的截止日，截止日当天即到期。
		r, err := s.Check(CheckRequest{
			ApplicationID: "APP-1",
			ProcessedOn:   MustParseDate("2026-01-10"),
			ArchiveIDs:    []string{"A-1"},
		})
		if err != nil || r.Status != CheckReady {
			t.Fatalf("最终截止日当天应到期可办: status=%s err=%v", r.Status, err)
		}
	})
}

// 保管库打开后保存内容才出现历史期限日期问题：下一次使用有效输入的查询、
// 核对与办理都必须返回 ErrCorruptState——即使只操作另一份正常档案，也不得
// 返回正常历史、清册或部分核对报告，不产生业务变更，原保存内容保持原样，
// 不删除出错的修订，也不把历史日期改成当前截止日。
func TestHistoricalDateCorruptionAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	revise(t, s, "R-1", "A-1", "2025-01-10", "2026-01-10", "2024-12-01", "延长")
	revise(t, s, "R-2", "A-1", "2026-01-10", "2027-01-10", "2025-06-01", "延长")
	if _, found, err := s.History("A-1"); err != nil || !found {
		t.Fatalf("损坏前查询应成功: found=%v err=%v", found, err)
	}

	// 把首条修订的新截止日改到起算日之前，并同步后续修订的原截止日：
	// 修订前后仍然完全衔接，当前截止日也等于末次修订的新值，但历史中
	// 出现了不可能由正常办理产生的期限。
	rewriteState(t, dir, func(doc map[string]any) {
		revisions := doc["archives"].(map[string]any)["A-1"].(map[string]any)["revisions"].([]any)
		revisions[0].(map[string]any)["new_end"] = "2019-12-31"
		revisions[1].(map[string]any)["old_end"] = "2019-12-31"
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开整个位置必须失败，错误指出 A-1、R-1 与两项日期。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("历史期限日期早于起算日时，打开必须失败")
	}
	for _, want := range []string{"A-1", "R-1", "2019-12-31", "2020-01-01"} {
		if !errors.Is(err, ErrCorruptState) || !strings.Contains(err.Error(), want) {
			t.Fatalf("打开错误应为 ErrCorruptState 并包含 %q，得到 %v", want, err)
		}
	}

	// 已打开的实例：即使只操作正常的 A-2，也必须按整库损坏失败。
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History 必须返回 ErrCorruptState 且无结论: found=%v err=%v", found, err)
	}
	if _, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("查询正常档案 A-2 也应返回 ErrCorruptState: found=%v err=%v", found, err)
	}
	if _, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("损坏后 GetManifest 必须返回 ErrCorruptState: found=%v err=%v", found, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出部分报告: %+v err=%v", r, err)
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
		RevisionID: "R-3", ArchiveID: "A-2",
		OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
		RevisedOn: MustParseDate("2025-01-07"), Reason: "延期",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Revise 必须失败: %v", err)
	}

	// 失败不得触发重新保存：损坏内容原样保留，不删修订、不改历史日期。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}
