package retention

import (
	"errors"
	"strings"
	"testing"
)

// 保存内容中任一档案缺少起算日或当前生效的截止日，或截止日早于起算日时，
// Open 必须按记录损坏处理：返回 ErrCorruptState，错误信息能定位档案编号并
// 说明缺的是哪项日期（顺序错误时给出两项日期），原文件保持原样。
// 没有修订记录的旧档案、已修订的档案、已销毁且已有清册的档案都遵守这条规则。
func TestOpenRejectsArchivesWithInvalidRetentionDates(t *testing.T) {
	corrupt := map[string]struct {
		content string
		want    []string // 错误信息必须包含的片段
	}{
		"缺起算日": {
			content: `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[]}},"manifests":{}}`,
			want:    []string{"A-1", "起算日"},
		},
		"缺当前截止日": {
			content: `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","initial_end":"2025-01-10","destroyed":false,"freezes":[]}},"manifests":{}}`,
			want:    []string{"A-1", "截止日"},
		},
		"起算日与截止日都缺": {
			content: `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","destroyed":false,"freezes":[]}},"manifests":{}}`,
			want:    []string{"A-1", "起算日", "截止日"},
		},
		"截止日早于起算日": {
			content: `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2025-01-10","end":"2025-01-09","initial_end":"2025-01-09","destroyed":false,"freezes":[]}},"manifests":{}}`,
			want:    []string{"A-1", "2025-01-10", "2025-01-09"},
		},
		"旧格式档案缺当前截止日": {
			// 没有修订记录的旧档案可以缺最初截止日与修订列表，
			// 但当前截止日不能靠兼容规则补齐。
			content: `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","destroyed":false,"freezes":[]}},"manifests":{}}`,
			want:    []string{"A-1", "截止日"},
		},
		"已修订档案缺当前截止日": {
			// 修订历史自身衔接完整也不能替代当前截止日的存在。
			content: `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","initial_end":"2025-01-10","destroyed":false,"freezes":[],"revisions":[{"id":"REV-1","old_end":"2025-01-10","new_end":"2026-01-10","revised_on":"2025-01-05","reason":"延期"}]}},"manifests":{}}`,
			want:    []string{"A-1", "截止日"},
		},
		"已修订档案截止日早于起算日": {
			content: `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2025-01-10","end":"2025-01-09","initial_end":"2025-01-10","destroyed":false,"freezes":[],"revisions":[{"id":"REV-1","old_end":"2025-01-10","new_end":"2025-01-09","revised_on":"2025-01-05","reason":"提前"}]}},"manifests":{}}`,
			want:    []string{"A-1", "2025-01-10", "2025-01-09"},
		},
		"已销毁档案缺当前截止日": {
			// 已有清册且归属、条目相互对应，也不能略过当前期限规则。
			content: `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1","freezes":[]}},"manifests":{"APP-1":{"application_id":"APP-1","processed_on":"2025-01-10","entries":[{"id":"A-1","category":"合同","start":"2020-01-01"}]}}}`,
			want:    []string{"A-1", "截止日"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.content)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("档案期限日期缺失或顺序不合法时不应打开成功")
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

// 合法记录不受新校验影响：没有修订记录的旧档案缺少最初截止日与修订列表
// 仍可打开；截止日等于起算日合法，截止日当天核对即到期；经过合法修订的
// 档案按最后成功修订后的当前截止日判断。
func TestValidRetentionDatesKeepWorking(t *testing.T) {
	t.Run("旧档案缺最初截止日仍可打开", func(t *testing.T) {
		dir := t.TempDir()
		writeStateFile(t, dir, `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","destroyed":false,"freezes":[]}},"manifests":{}}`)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("起算日与当前截止日齐备的旧记录应能打开: %v", err)
		}
		defer s.Close()
		h, found, err := s.History("A-1")
		if err != nil || !found {
			t.Fatalf("旧记录应能查询: found=%v err=%v", found, err)
		}
		if !h.InitialEnd.Equal(MustParseDate("2025-01-10")) {
			t.Fatalf("最初截止日应按登记截止日补齐: %+v", h)
		}
	})

	t.Run("截止日等于起算日合法", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		reg(t, s, "A-1", "合同", "2025-01-10", "2025-01-10")
		r, err := s.Check(CheckRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
		})
		if err != nil {
			t.Fatalf("核对不应报错: %v", err)
		}
		if r.Status != CheckReady {
			t.Fatalf("截止日等于起算日时当天即到期: %+v", r)
		}
	})

	t.Run("修订后按当前截止日判断", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
		if _, err := s.Revise(ReviseInput{
			RevisionID: "REV-1", ArchiveID: "A-1",
			OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
			RevisedOn: MustParseDate("2025-01-05"), Reason: "延期",
		}); err != nil {
			t.Fatal(err)
		}
		r, err := s.Check(CheckRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-06-01"), ArchiveIDs: []string{"A-1"},
		})
		if err != nil {
			t.Fatalf("核对不应报错: %v", err)
		}
		if r.Status != CheckBlocked || r.Results[0].Obstructions[0].Kind != ObstructionNotExpired {
			t.Fatalf("应按修订后的当前截止日判未到期: %+v", r)
		}
	})
}

// 保管库打开后保存内容才出现期限日期问题：下一次使用有效输入的查询、核对
// 与办理都必须返回 ErrCorruptState——即使只操作另一份正常档案，也不得返回
// 正常历史、清册或部分核对结果，办理不得产生业务变更，原保存内容保持原样。
func TestInvalidRetentionDatesAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "B-1", "凭证", "2020-01-01", "2025-01-10")
	if _, found, err := s.History("A-1"); err != nil || !found {
		t.Fatalf("损坏前查询应成功: found=%v err=%v", found, err)
	}

	// 把 B-1 的当前截止日删掉：B-1 变成无法确认保管期限的档案。
	rewriteState(t, dir, func(doc map[string]any) {
		delete(doc["archives"].(map[string]any)["B-1"].(map[string]any), "end")
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开整个位置必须失败，错误指出 B-1 缺的是截止日。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("任一档案缺当前截止日时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "B-1") || !strings.Contains(err.Error(), "截止日") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出 B-1 缺截止日，得到 %v", err)
	}

	// 已打开的实例：即使只操作正常的 A-1，也必须按整库损坏失败。
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History 必须返回 ErrCorruptState 且无结论: found=%v err=%v", found, err)
	}
	if _, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("损坏后 GetManifest 必须返回 ErrCorruptState: found=%v err=%v", found, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出部分报告: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
	}
	if err := s.Register(RegisterInput{ID: "A-2", Category: "凭证", Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-01-10")}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Register 必须失败: %v", err)
	}
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Freeze 必须失败: %v", err)
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "REV-1", ArchiveID: "A-1",
		OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
		RevisedOn: MustParseDate("2025-01-07"), Reason: "延期",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Revise 必须失败: %v", err)
	}

	// 失败不得触发重新保存：损坏内容原样保留，不补日期、不删档案。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}

// 缺当前截止日的档案不能被销毁前核对当成已经到期：损坏发生前该档案
// 尚未到期，损坏后核对与正式提交都只能整库失败，绝不能生成清册。
func TestMissingEndNeverYieldsDestroyableConclusion(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2099-01-10")

	// 损坏前：档案未到期，核对给出未到期阻碍而不是可以办理。
	r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != CheckBlocked || r.Results[0].Obstructions[0].Kind != ObstructionNotExpired {
		t.Fatalf("损坏前应判未到期: %+v", r)
	}

	// 把截止日删掉：若按零值截止日判断，任何处理日期都会显得已到期。
	rewriteState(t, dir, func(doc map[string]any) {
		delete(doc["archives"].(map[string]any)["A-1"].(map[string]any), "end")
		delete(doc["archives"].(map[string]any)["A-1"].(map[string]any), "initial_end")
	})
	corruptContent := readStateFile(t, dir)

	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" {
		t.Fatalf("缺截止日的档案不得给出核对结论: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("缺截止日的档案不得生成清册: %+v err=%v", m, err)
	}
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}
