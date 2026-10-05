package retention

import (
	"errors"
	"strings"
	"testing"
)

// 只核对当前期限合法、修订前后衔接仍有缺口：最初截止日以及每条修订中的
// 原截止日、新截止日都不得早于同一档案的起算日。中途曾把期限缩短到
// 起算日之前、随后又改回合法日期的历史，即使衔接完整、当前截止日等于
// 末次修订的新值，Open 也必须按记录损坏失败（ErrCorruptState）；最初
// 截止日本身早于起算日、后来通过修订改到合法日期的情形同样如此。
// 错误信息指出档案编号、出错的期限位置、该截止日与起算日，问题在修订
// 记录中时同时指出修订编号；原文件保持原样。已销毁档案即使清册条目、
// 归属和处理日期都正确，也不能掩盖历史中的非法期限。
func TestOpenRejectsHistoryEndsBeforeStart(t *testing.T) {
	archive := func(initial, end string, destroyed bool, revisions string) string {
		s := `{"id":"A-1","category":"合同","start":"2020-01-01","end":"` + end +
			`","initial_end":"` + initial + `","destroyed":`
		if destroyed {
			s += `true,"manifest_id":"APP-1"`
		} else {
			s += `false`
		}
		return s + `,"freezes":[]` + revisions + `}`
	}
	rev := func(id, oldEnd, newEnd string) string {
		return `{"id":"` + id + `","old_end":"` + oldEnd + `","new_end":"` + newEnd +
			`","revised_on":"2025-01-05","reason":"调整"}`
	}
	state := func(archiveJSON string, manifest string) string {
		if manifest == "" {
			manifest = "{}"
		}
		return `{"version":1,"archives":{"A-1":` + archiveJSON + `},"manifests":` + manifest + `}`
	}
	closedManifest := func() string {
		return `{"APP-1":{"application_id":"APP-1","processed_on":"2026-01-10",` +
			`"entries":[{"id":"A-1","category":"合同","start":"2020-01-01","end":"2026-01-10"}]}}`
	}

	corrupt := map[string]struct {
		content string
		want    []string // 错误信息必须包含的片段
	}{
		"缩短到起算日前又改回合法期限": {
			// 题目主例：起算日 2020-01-01，最初 2025-01-10，
			// 第一条缩短到 2019-12-31，第二条延长到 2026-01-10。
			content: state(archive("2025-01-10", "2026-01-10", false, `,"revisions":[`+
				rev("R-1", "2025-01-10", "2019-12-31")+`,`+
				rev("R-2", "2019-12-31", "2026-01-10")+`]`), ""),
			want: []string{"A-1", "R-1", "新截止日", "2019-12-31", "2020-01-01"},
		},
		"非法期限出现在第二条修订": {
			// 第一条合法延长，第二条缩短到起算日之前，第三条改回：
			// 错误必须定位到具体的第二条修订，而不是笼统地只报档案。
			content: state(archive("2025-01-10", "2027-01-10", false, `,"revisions":[`+
				rev("R-1", "2025-01-10", "2026-01-10")+`,`+
				rev("R-2", "2026-01-10", "2019-12-31")+`,`+
				rev("R-3", "2019-12-31", "2027-01-10")+`]`), ""),
			want: []string{"A-1", "R-2", "新截止日", "2019-12-31", "2020-01-01"},
		},
		"最初截止日早于起算日后经修订改回": {
			// 最初登记的截止日就早于起算日，之后修订到合法日期：
			// 即使当前期限合法、修订衔接，最初期限仍不可能由正常登记产生。
			content: state(archive("2019-12-31", "2026-01-10", false, `,"revisions":[`+
				rev("R-1", "2019-12-31", "2026-01-10")+`]`), ""),
			want: []string{"A-1", "最初截止日", "2019-12-31", "2020-01-01"},
		},
		"已销毁档案的历史中曾早于起算日": {
			// 已销毁、清册归属/条目/处理日期全部正确（处理日期不早于
			// 清册条目保存的最终截止日），仍不能掩盖修订历史中的非法期限。
			content: state(archive("2025-01-10", "2026-01-10", true, `,"revisions":[`+
				rev("R-1", "2025-01-10", "2019-12-31")+`,`+
				rev("R-2", "2019-12-31", "2026-01-10")+`]`), closedManifest()),
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
				t.Fatal("期限历史中存在早于起算日的截止日时不应打开成功")
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

// 合法的期限历史不受新校验影响：缩短或延长到不早于起算日的日期、改回
// 早先用过的日期都正常；截止日（含修订的新截止日）等于起算日也合法，
// 截止日当天核对即到期。
func TestValidHistoryEndsKeepWorking(t *testing.T) {
	t.Run("修订新截止日等于起算日合法且当天到期", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
		revise(t, s, "R-1", "A-1", "2025-01-10", "2020-01-01", "2024-01-01", "缩短到起算日")
		h, found, err := s.History("A-1")
		if err != nil || !found {
			t.Fatalf("历史查询失败: found=%v err=%v", found, err)
		}
		if !h.RetentionEnd.Equal(MustParseDate("2020-01-01")) || len(h.Revisions) != 1 {
			t.Fatalf("历史不符: %+v", h)
		}
		r, err := s.Check(CheckRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2020-01-01"), ArchiveIDs: []string{"A-1"},
		})
		if err != nil || r.Status != CheckReady {
			t.Fatalf("截止日等于起算日时当天即到期: status=%s err=%v", r.Status, err)
		}
	})

	t.Run("缩短延长再改回早先用过的日期仍可读", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
		revise(t, s, "R-1", "A-1", "2025-01-10", "2026-01-10", "2024-12-01", "延长")
		revise(t, s, "R-2", "A-1", "2026-01-10", "2024-06-01", "2025-01-02", "缩短")
		revise(t, s, "R-3", "A-1", "2024-06-01", "2025-01-10", "2025-01-03", "改回")
		h, found, err := s.History("A-1")
		if err != nil || !found {
			t.Fatalf("历史查询失败: found=%v err=%v", found, err)
		}
		if len(h.Revisions) != 3 ||
			!h.InitialEnd.Equal(MustParseDate("2025-01-10")) ||
			!h.RetentionEnd.Equal(MustParseDate("2025-01-10")) {
			t.Fatalf("历史应按成功办理顺序保留全部修订: %+v", h)
		}
	})
}

// 保管库打开后保存内容才出现中途早于起算日的期限历史：下一次使用有效
// 输入的查询、销毁前核对与办理都必须返回 ErrCorruptState——即使只操作
// 另一份正常档案，也不得返回正常历史、清册或部分核对报告，不产生业务
// 变更；出错的修订保留原样，历史日期不被改成当前截止日。
func TestHistoryEndBeforeStartAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "B-1", "凭证", "2020-01-01", "2025-01-10")
	revise(t, s, "R-1", "A-1", "2025-01-10", "2026-01-10", "2025-01-05", "延长")
	if _, found, err := s.History("A-1"); err != nil || !found {
		t.Fatalf("损坏前查询应成功: found=%v err=%v", found, err)
	}

	// 把合法延长改写成“先缩短到起算日之前、再延长回 2026-01-10”：
	// 当前截止日仍是合法的 2026-01-10，两条修订也完全衔接，只有中途的
	// 2019-12-31 不可能由正常办理产生。
	rewriteState(t, dir, func(doc map[string]any) {
		a1 := doc["archives"].(map[string]any)["A-1"].(map[string]any)
		revs := a1["revisions"].([]any)
		revs[0].(map[string]any)["new_end"] = "2019-12-31"
		revs = append(revs, map[string]any{
			"id": "R-2", "old_end": "2019-12-31", "new_end": "2026-01-10",
			"revised_on": "2025-01-06", "reason": "延长",
		})
		a1["revisions"] = revs
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开整个位置必须失败，错误指出档案、修订编号、出错的截止日与起算日。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("期限历史中存在早于起算日的截止日时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) {
		t.Fatalf("打开错误应为 ErrCorruptState，得到 %v", err)
	}
	for _, want := range []string{"A-1", "R-1", "2019-12-31", "2020-01-01"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("打开错误信息应包含 %q，得到 %v", want, err)
		}
	}

	// 已打开的实例：即使只操作正常的 B-1，也必须按整库损坏失败。
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History(A-1) 必须返回 ErrCorruptState 且无结论: found=%v err=%v", found, err)
	}
	if _, found, err := s.History("B-1"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("损坏后查询正常档案 B-1 也必须返回 ErrCorruptState: found=%v err=%v", found, err)
	}
	if _, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("损坏后 GetManifest 必须返回 ErrCorruptState: found=%v err=%v", found, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"B-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出部分报告: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"B-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
	}
	if err := s.Register(RegisterInput{
		ID: "A-2", Category: "凭证",
		Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-01-10"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Register 必须失败: %v", err)
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "B-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Freeze 必须失败: %v", err)
	}
	if err := s.Release(ReleaseInput{
		ArchiveID: "B-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-06"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Release 必须失败: %v", err)
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-9", ArchiveID: "B-1",
		OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
		RevisedOn: MustParseDate("2025-01-07"), Reason: "延期",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Revise 必须失败: %v", err)
	}

	// 失败不得触发重新保存：出错的修订保留、历史日期不被改成当前截止日。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}
