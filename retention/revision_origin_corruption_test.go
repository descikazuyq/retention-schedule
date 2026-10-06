package retention

import (
	"errors"
	"strings"
	"testing"
)

// 保存的修订缺少修订原因（缺失、为 null、为空串或仅含空白）或修订日期
// （字段缺失或为 null）时，Open 必须按保存记录损坏失败（ErrCorruptState）：
// 错误指出档案编号、修订编号以及缺少的是修订原因还是修订日期，同一条修订
// 两项同时缺少时两项都要说清。要求覆盖全部成功修订（不只最后一条），
// 被冻结、已销毁档案的修订同样适用，清册归属、条目与处理日期正确也不能
// 掩盖；修订日期已填写但不是合法日历日期时继续按损坏处理。原文件保持原样。
func TestOpenRejectsRevisionMissingOriginInfo(t *testing.T) {
	rev := func(id, oldEnd, newEnd, attrs string) string {
		return `{"id":"` + id + `","old_end":"` + oldEnd + `","new_end":"` + newEnd + `"` + attrs + `}`
	}
	archive := func(id, end, initial string, destroyed bool, freezes, revisions string) string {
		s := `{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"` + end +
			`","initial_end":"` + initial + `","destroyed":`
		if destroyed {
			s += `true,"manifest_id":"APP-1"`
		} else {
			s += `false`
		}
		return s + `,"freezes":[` + freezes + `]` + revisions + `}`
	}
	entry := func(end string) string {
		return `{"id":"A-1","category":"合同","start":"2020-01-01","end":"` + end + `"}`
	}
	state := func(archiveJSON string) string {
		return `{"version":1,"archives":{"A-1":` + archiveJSON + `},"manifests":{}}`
	}
	destroyedState := func(archiveJSON, entriesJSON string) string {
		return `{"version":1,"archives":{"A-1":` + archiveJSON +
			`},"manifests":{"APP-1":{"application_id":"APP-1","processed_on":"2026-01-10","entries":[` +
			entriesJSON + `]}}}`
	}

	const goodAttrs = `,"revised_on":"2025-01-05","reason":"延长"`

	corrupt := map[string]struct {
		content string
		wantAll []string // 错误信息必须全部包含
	}{
		"修订缺少原因字段": {
			state(archive("A-1", "2026-01-10", "2025-01-10", false, "",
				`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10", `,"revised_on":"2025-01-05"`)+`]`)),
			[]string{"A-1", "R-1", "修订原因"},
		},
		"修订原因为 null": {
			state(archive("A-1", "2026-01-10", "2025-01-10", false, "",
				`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10",
					`,"revised_on":"2025-01-05","reason":null`)+`]`)),
			[]string{"A-1", "R-1", "修订原因"},
		},
		"修订原因为空串": {
			state(archive("A-1", "2026-01-10", "2025-01-10", false, "",
				`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10",
					`,"revised_on":"2025-01-05","reason":""`)+`]`)),
			[]string{"A-1", "R-1", "修订原因"},
		},
		"修订原因只有空白": {
			state(archive("A-1", "2026-01-10", "2025-01-10", false, "",
				`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10",
					`,"revised_on":"2025-01-05","reason":"  \t "`)+`]`)),
			[]string{"A-1", "R-1", "修订原因"},
		},
		"修订缺少修订日期字段": {
			state(archive("A-1", "2026-01-10", "2025-01-10", false, "",
				`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10", `,"reason":"延长"`)+`]`)),
			[]string{"A-1", "R-1", "修订日期"},
		},
		"修订日期为 null": {
			state(archive("A-1", "2026-01-10", "2025-01-10", false, "",
				`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10",
					`,"revised_on":null,"reason":"延长"`)+`]`)),
			[]string{"A-1", "R-1", "修订日期"},
		},
		"修订原因与修订日期同时缺少": {
			state(archive("A-1", "2026-01-10", "2025-01-10", false, "",
				`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10", "")+`]`)),
			[]string{"A-1", "R-1", "修订原因", "修订日期"},
		},
		"中间那次修订缺少原因不能因最终合法且衔接完整而接受": {
			// 题目主例：先延长到 2026-01-10，随后缩短到 2025-06-30；
			// 第二次（中间历史）修订没有原因，期限完全衔接、最终截止日合法。
			state(archive("A-1", "2025-06-30", "2025-01-10", false, "",
				`,"revisions":[`+
					rev("R-1", "2025-01-10", "2026-01-10", goodAttrs)+`,`+
					rev("R-2", "2026-01-10", "2025-06-30", `,"revised_on":"2025-02-01"`)+`]`)),
			[]string{"A-1", "R-2", "修订原因"},
		},
		"第一条修订缺少日期也要整条历史拒绝": {
			state(archive("A-1", "2025-06-30", "2025-01-10", false, "",
				`,"revisions":[`+
					rev("R-1", "2025-01-10", "2026-01-10", `,"reason":"延长"`)+`,`+
					rev("R-2", "2026-01-10", "2025-06-30", goodAttrs)+`]`)),
			[]string{"A-1", "R-1", "修订日期"},
		},
		"被冻结档案的修订缺少原因": {
			// 档案仍带一条未解除冻结，不影响“修订办理信息必须齐备”的判定。
			state(archive("A-1", "2026-01-10", "2025-01-10", false,
				`{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false}`,
				`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10", `,"revised_on":"2025-01-05"`)+`]`)),
			[]string{"A-1", "R-1", "修订原因"},
		},
		"已销毁档案的修订缺少修订日期": {
			// 清册归属、条目快照与处理日期（不早于最终截止日）全部正确，
			// 仍不能掩盖修订办理信息缺项。
			destroyedState(
				archive("A-1", "2026-01-10", "2025-01-10", true, "",
					`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10", `,"reason":"延长"`)+`]`),
				entry("2026-01-10")),
			[]string{"A-1", "R-1", "修订日期"},
		},
		"已销毁档案的修订原因只有空白": {
			destroyedState(
				archive("A-1", "2026-01-10", "2025-01-10", true, "",
					`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10",
						`,"revised_on":"2025-01-05","reason":" "`)+`]`),
				entry("2026-01-10")),
			[]string{"A-1", "R-1", "修订原因"},
		},
		"修订日期不是合法日历日期": {
			// 2025 年不是闰年，2025-02-29 不存在：日期文本是字符串但不是
			// 合法日历日期，JSON 解码即失败，按整库损坏处理。
			state(archive("A-1", "2026-01-10", "2025-01-10", false, "",
				`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10",
					`,"revised_on":"2025-02-29","reason":"延长"`)+`]`)),
			nil,
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.content)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("修订缺少修订原因或修订日期时不应打开成功")
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
			if got := readStateFile(t, dir); got != tc.content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：修订原因与修订日期齐备的记录（普通、带未解除冻结、已销毁且
	// 清册正确），以及没有修订记录的旧档案都可正常打开、历史可查。
	t.Run("合法记录对照组", func(t *testing.T) {
		valid := map[string]string{
			"没有修订记录的旧档案": state(archive("A-1", "2025-01-10", "2025-01-10", false, "", "")),
			"单条修订信息齐备": state(archive("A-1", "2026-01-10", "2025-01-10", false, "",
				`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10", goodAttrs)+`]`)),
			"多条修订信息齐备": state(archive("A-1", "2025-06-30", "2025-01-10", false, "",
				`,"revisions":[`+
					rev("R-1", "2025-01-10", "2026-01-10", goodAttrs)+`,`+
					rev("R-2", "2026-01-10", "2025-06-30", `,"revised_on":"2025-02-01","reason":"缩短"`)+`]`)),
			"带未解除冻结的合法修订": state(archive("A-1", "2026-01-10", "2025-01-10", false,
				`{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false}`,
				`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10", goodAttrs)+`]`)),
			"已销毁档案的合法修订": destroyedState(
				archive("A-1", "2026-01-10", "2025-01-10", true, "",
					`,"revisions":[`+rev("R-1", "2025-01-10", "2026-01-10", goodAttrs)+`]`),
				entry("2026-01-10")),
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
				if _, _, err := s.History("A-1"); err != nil {
					t.Fatalf("合法记录的历史应可查: %v", err)
				}
			})
		}
	})
}

// 保管库打开后保存内容才出现修订办理信息缺项：下一次使用有效输入查询历史、
// 取回清册、销毁前核对与各项办理都必须按整库记录损坏失败——即使本次操作的
// 是另一份正常档案——不返回正常历史或部分核对结果，不产生业务变更，也不借
// 办理操作把内容重新保存，原保存内容保持原样。
func TestRevisionMissingOriginInfoAfterOpenFailsAllOperations(t *testing.T) {
	cases := map[string]func(map[string]any){
		"中间那次修订原因被改成空白": func(doc map[string]any) {
			revs := doc["archives"].(map[string]any)["A-1"].(map[string]any)["revisions"].([]any)
			revs[1].(map[string]any)["reason"] = "   "
		},
		"修订日期字段被删除": func(doc map[string]any) {
			revs := doc["archives"].(map[string]any)["A-1"].(map[string]any)["revisions"].([]any)
			delete(revs[0].(map[string]any), "revised_on")
		},
		"修订日期被改成 null": func(doc map[string]any) {
			revs := doc["archives"].(map[string]any)["A-1"].(map[string]any)["revisions"].([]any)
			revs[1].(map[string]any)["revised_on"] = nil
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
			// 先延长、后缩短：两条修订期限衔接、最终截止日合法。
			revise(t, s, "R-1", "A-1", "2025-01-10", "2026-01-10", "2025-01-05", "延长")
			revise(t, s, "R-2", "A-1", "2026-01-10", "2025-06-30", "2025-02-01", "缩短")

			rewriteState(t, dir, mutate)
			corruptContent := readStateFile(t, dir)

			// 重新打开必须失败，错误指出档案编号与修订编号。
			s2, err := Open(dir)
			if err == nil {
				s2.Close()
				t.Fatal("修订办理信息缺项时，打开必须失败")
			}
			if !errors.Is(err, ErrCorruptState) ||
				!strings.Contains(err.Error(), "A-1") {
				t.Fatalf("打开错误应为 ErrCorruptState 并指出 A-1，得到 %v", err)
			}

			// 已打开实例：即使本次只操作另一份正常档案 A-2，也必须按整库损坏失败。
			if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
				t.Fatalf("损坏后查询无关档案也必须失败且无结论: found=%v err=%v", found, err)
			}
			if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
				t.Fatalf("损坏后 History 必须失败且不得给出部分历史: found=%v err=%v", found, err)
			}
			if m, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
				t.Fatalf("损坏后 GetManifest 必须失败: found=%v err=%v %+v", found, err, m)
			}
			if r, err := s.Check(CheckRequest{
				ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
			}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
				t.Fatalf("损坏后 Check 必须失败且不得给出结论或部分报告: %+v err=%v", r, err)
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

			// 失败不得触发修复：不补写原因或日期、不借用冻结/销毁日期、
			// 不删除修订，也不借办理操作把内容重新保存。
			if got := readStateFile(t, dir); got != corruptContent {
				t.Fatalf("失败后原保存内容被改动:\n%q", got)
			}
		})
	}
}

// 正常提交修订仍要求原因与日期齐备，原因去除首尾空白后保存，日期只记录
// 办理时间——读取侧的缺项校验不影响办理入口的既有行为。
func TestReviseInputValidationUnchanged(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	base := ReviseInput{
		RevisionID:  "R-1",
		ArchiveID:   "A-1",
		OriginalEnd: MustParseDate("2025-01-10"),
		NewEnd:      MustParseDate("2026-01-10"),
		RevisedOn:   MustParseDate("2025-01-05"),
		Reason:      "业务调整",
	}
	for _, blank := range []string{"", "   ", "\t"} {
		in := base
		in.Reason = blank
		if _, err := s.Revise(in); !errors.Is(err, ErrBlankField) {
			t.Fatalf("空白修订原因 %q 仍应拒绝: %v", blank, err)
		}
	}
	missingDate := base
	missingDate.RevisedOn = Date{}
	if _, err := s.Revise(missingDate); !errors.Is(err, ErrInvalidDate) {
		t.Fatalf("缺少修订日期仍应拒绝: %v", err)
	}

	// 原因带首尾空白时正常办理，保存为去空白后的值；修订日期保留办理时间。
	if _, err := s.Revise(ReviseInput{
		RevisionID:  "R-2",
		ArchiveID:   "A-1",
		OriginalEnd: MustParseDate("2025-01-10"),
		NewEnd:      MustParseDate("2025-06-30"),
		RevisedOn:   MustParseDate("2025-02-01"),
		Reason:      "  业务调整  ",
	}); err != nil {
		t.Fatal(err)
	}
	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("历史应可查: found=%v err=%v", found, err)
	}
	if len(h.Revisions) != 1 {
		t.Fatalf("应有一条成功修订，得到 %d 条", len(h.Revisions))
	}
	r := h.Revisions[0]
	if r.RevisionID != "R-2" || r.Reason != "业务调整" ||
		!r.RevisedOn.Equal(MustParseDate("2025-02-01")) {
		t.Fatalf("原因应去空白保存、修订日期应保留办理时间: %+v", r)
	}
}
