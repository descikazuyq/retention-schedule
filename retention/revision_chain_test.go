package retention

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// 最初截止日、按成功顺序保存的修订链与当前截止日必须连续对应。
// 下列记录 JSON 都能解析，但期限关系自相矛盾，Open 必须按损坏处理，
// 返回 ErrCorruptState；错误指出档案编号，能定位到修订时同时指出修订编号，
// 且原文件保持原样。
func TestOpenRejectsBrokenRevisionChains(t *testing.T) {
	rev := func(id, oldEnd, newEnd, on string) string {
		return `{"id":"` + id + `","old_end":"` + oldEnd + `","new_end":"` + newEnd +
			`","revised_on":"` + on + `","reason":"r"}`
	}
	r1 := rev("R-1", "2025-01-10", "2026-01-10", "2025-01-09")
	r2 := rev("R-2", "2026-01-10", "2027-01-10", "2025-02-01")

	// build 构造一份档案的状态；initial 为空时省略 initial_end 字段，
	// 用于模拟“已有修订却缺少最初截止日”的旧/坏记录。
	build := func(end, initial, revisions string) string {
		initialField := ""
		if initial != "" {
			initialField = `,"initial_end":"` + initial + `"`
		}
		return fmt.Sprintf(
			`{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"%s"%s,"destroyed":false,"freezes":[],"revisions":%s}},"manifests":{}}`,
			end, initialField, revisions)
	}

	cases := map[string]struct {
		content      string
		wantRevision string
	}{
		// 任务场景：修订已把期限延长到 2026-01-10，当前截止日却仍是 2025-01-10。
		"当前截止日与末次修订不符": {build("2025-01-10", "2025-01-10", "["+r1+"]"), "R-1"},
		"第一条修订原截止日不等于最初截止日": {
			build("2026-01-10", "2025-01-10",
				"["+rev("R-1", "2025-06-30", "2026-01-10", "2025-01-09")+"]"), "R-1"},
		"后续修订原截止日接不上上一条新截止日": {
			build("2027-01-10", "2025-01-10",
				"["+r1+","+rev("R-2", "2026-06-30", "2027-01-10", "2025-02-01")+"]"), "R-2"},
		"修订链完整但末次新截止日对不上当前值": {
			build("2026-06-30", "2025-01-10", "["+r1+","+r2+"]"), "R-2"},
		"修订列表中存在空记录":        {build("2026-01-10", "2025-01-10", "[null]"), ""},
		"空记录混在合法修订之间":       {build("2027-01-10", "2025-01-10", "["+r1+",null]"), ""},
		"已有修订却缺少最初截止日":      {build("2026-01-10", "", "["+r1+"]"), ""},
		"无修订但最初截止日与当前截止日不符": {build("2025-01-10", "2024-01-01", "[]"), ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			content := tc.content
			dir := t.TempDir()
			writeStateFile(t, dir, content)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("自相矛盾的期限记录不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "A-1") {
				t.Fatalf("错误信息应指出档案编号 A-1，得到 %v", err)
			}
			if tc.wantRevision != "" && !strings.Contains(msg, tc.wantRevision) {
				t.Fatalf("错误信息应指出修订编号 %s，得到 %v", tc.wantRevision, err)
			}
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动: %q -> %q", content, got)
			}
		})
	}
}

// 衔接完整的修订链必须正常读取：延长、缩短、改回早先用过的日期都合法；
// 修订日期只用于记录，乱序也不影响按保存顺序建立的链。
func TestOpenAcceptsContinuousRevisionChains(t *testing.T) {
	rev := func(id, oldEnd, newEnd, on string) string {
		return `{"id":"` + id + `","old_end":"` + oldEnd + `","new_end":"` + newEnd +
			`","revised_on":"` + on + `","reason":"r"}`
	}
	cases := map[string]string{
		"只延长过": `[{"id":"R-1","old_end":"2025-01-10","new_end":"2026-01-10","revised_on":"2025-01-09","reason":"延长"}]`,
		"先延长后缩短": "[" +
			rev("R-1", "2025-01-10", "2026-01-10", "2025-01-09") + "," +
			rev("R-2", "2026-01-10", "2025-06-30", "2025-02-01") + "]",
		"改回早先用过的日期": "[" +
			rev("R-1", "2025-01-10", "2026-01-10", "2025-01-09") + "," +
			rev("R-2", "2026-01-10", "2025-01-10", "2025-02-01") + "]",
		"修订日期早于上一条仍按保存顺序衔接": "[" +
			rev("R-1", "2025-01-10", "2026-01-10", "2025-03-01") + "," +
			rev("R-2", "2026-01-10", "2027-01-10", "2025-01-01") + "]",
	}
	ends := map[string]string{
		"只延长过":      "2026-01-10",
		"先延长后缩短":    "2025-06-30",
		"改回早先用过的日期": "2025-01-10",
		"修订日期早于上一条仍按保存顺序衔接": "2027-01-10",
	}
	for name, revs := range cases {
		t.Run(name, func(t *testing.T) {
			content := `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"` +
				ends[name] + `","initial_end":"2025-01-10","destroyed":false,"freezes":[],"revisions":` +
				revs + `}},"manifests":{}}`
			dir := t.TempDir()
			writeStateFile(t, dir, content)
			s, err := Open(dir)
			if err != nil {
				t.Fatalf("衔接完整的修订链应正常打开: %v", err)
			}
			defer s.Close()
			h, found, err := s.History("A-1")
			if err != nil || !found {
				t.Fatalf("合法档案应能查询历史: found=%v err=%v", found, err)
			}
			if !h.InitialEnd.Equal(MustParseDate("2025-01-10")) ||
				!h.RetentionEnd.Equal(MustParseDate(ends[name])) {
				t.Fatalf("最初/当前截止日不符: %+v", h)
			}
		})
	}
}

// 通过正常办理把期限改回早先用过的日期，重开后链仍完整；
// 合法档案始终按当前生效截止日核对，截止日当天到期、前一天未到期。
func TestReviseBackToEarlierDateThenExpireByCurrentEnd(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	revise(t, s, "R-1", "A-1", "2025-01-10", "2026-01-10", "2025-01-05", "延长")
	revise(t, s, "R-2", "A-1", "2026-01-10", "2025-01-10", "2025-01-06", "改回原期限")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("改回早先日期后的记录应能重开: %v", err)
	}
	defer s2.Close()
	h, found, err := s2.History("A-1")
	if err != nil || !found || len(h.Revisions) != 2 {
		t.Fatalf("重开后历史应完整: found=%v err=%v h=%+v", found, err, h)
	}

	blocked, err := s2.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-09"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil || blocked.Status != CheckBlocked {
		t.Fatalf("当前截止日前一天应未到期: status=%s err=%v", blocked.Status, err)
	}
	ready, err := s2.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil || ready.Status != CheckReady {
		t.Fatalf("当前截止日当天应到期可办理: status=%s err=%v", ready.Status, err)
	}
	if _, err := s2.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatalf("按当前截止日到期后销毁应成功: %v", err)
	}
}

// 保管库打开后某份档案的当前截止日才被改成与修订历史矛盾：
// 之后的查询、核对、办理即使只涉及另一份正常档案，也必须整库失败，
// 不返回部分历史、核对结论或清册，不改动任何原有保存内容。
func TestBrokenRevisionChainAfterOpenFailsWholeVault(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "D-1", "凭证", "2020-01-01", "2025-01-10")
	revise(t, s, "R-D", "D-1", "2025-01-10", "2026-01-10", "2025-01-09", "延长")

	// 把 D-1 的当前截止日改回旧值，制造“当前值与末次修订矛盾”的损坏。
	rewriteState(t, dir, func(doc map[string]any) {
		doc["archives"].(map[string]any)["D-1"].(map[string]any)["end"] = "2025-01-10"
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误指出档案 D-1 与修订 R-D。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("当前截止日与修订历史矛盾时打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "D-1") || !strings.Contains(err.Error(), "R-D") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出 D-1/R-D，得到 %v", err)
	}

	// 已打开的实例：只操作与异常记录无关的正常档案 A-1 也必须整库失败。
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("查询无关档案也应 ErrCorruptState 且无部分历史: found=%v err=%v", found, err)
	}
	if h, found, err := s.History("D-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("查询异常档案不得返回相互矛盾的历史: found=%v err=%v", found, err)
	}
	if _, found, err := s.GetManifest("APP-X"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("清册查询也应整库失败: found=%v err=%v", found, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("核对不得给出结论或部分报告: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("不得在损坏状态下生成清册: %+v err=%v", m, err)
	}
	if err := s.Register(RegisterInput{
		ID: "A-2", Category: "凭证", Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-01-10"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Register 必须失败: %v", err)
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Freeze 必须失败: %v", err)
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-A", ArchiveID: "A-1",
		OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
		RevisedOn: MustParseDate("2025-01-05"), Reason: "延长",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Revise 必须失败: %v", err)
	}

	// 失败不得触发任何重写，原有保存内容（包括矛盾本身）原样保留。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}
