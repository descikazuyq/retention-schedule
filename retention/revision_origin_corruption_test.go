package retention

import (
	"errors"
	"strings"
	"testing"
)

// 保存的修订缺少修订原因（字段缺失、为 null、为空串或仅含空白）或修订日期
// （字段缺失或为 null）时，Open 必须按保存记录损坏失败（ErrCorruptState）：
// 错误指出档案编号、修订编号以及缺少的是修订原因还是修订日期，两项同时
// 缺少时两项都要说清；校验覆盖全部成功修订，被冻结或已销毁档案的修订
// 历史同样适用，原文件保持原样。
func TestOpenRejectsRevisionMissingOriginInfo(t *testing.T) {
	// rev 只给出 id 与两个期限，其余属性由 attrs 决定，便于构造各种缺项。
	rev := func(id, oldEnd, newEnd, attrs string) string {
		return `{"id":"` + id + `","old_end":"` + oldEnd + `","new_end":"` + newEnd + `"` + attrs + `}`
	}
	full := `,"revised_on":"2025-01-05","reason":"调整"`
	archive := func(id, end string, revisions string) string {
		return `"` + id + `":{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"` + end +
			`","initial_end":"2025-01-10","destroyed":false,"freezes":[],"revisions":[` + revisions + `]}`
	}
	destroyedArchive := func(id string, revisions string) string {
		return `"` + id + `":{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"2026-01-10",` +
			`"initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1","freezes":[],` +
			`"revisions":[` + revisions + `]}`
	}
	entry := `{"id":"A-1","category":"合同","start":"2020-01-01","end":"2026-01-10"}`
	state := func(archivesJSON string) string {
		return `{"version":1,"archives":{` + archivesJSON + `},"manifests":{}}`
	}
	destroyedState := func(archivesJSON string) string {
		return `{"version":1,"archives":{` + archivesJSON +
			`},"manifests":{"APP-1":{"application_id":"APP-1","processed_on":"2026-01-10","entries":[` + entry + `]}}}`
	}

	corrupt := map[string]struct {
		state   string
		wantAll []string // 错误信息必须全部包含
	}{
		"修订缺少原因字段": {
			state(archive("A-1", "2026-01-10", rev("R-1", "2025-01-10", "2026-01-10",
				`,"revised_on":"2025-01-05"`))),
			[]string{"A-1", "R-1", "修订原因"},
		},
		"修订原因为 null": {
			state(archive("A-1", "2026-01-10", rev("R-1", "2025-01-10", "2026-01-10",
				`,"revised_on":"2025-01-05","reason":null`))),
			[]string{"A-1", "R-1", "修订原因"},
		},
		"修订原因为空串": {
			state(archive("A-1", "2026-01-10", rev("R-1", "2025-01-10", "2026-01-10",
				`,"revised_on":"2025-01-05","reason":""`))),
			[]string{"A-1", "R-1", "修订原因"},
		},
		"修订原因只有空白": {
			state(archive("A-1", "2026-01-10", rev("R-1", "2025-01-10", "2026-01-10",
				`,"revised_on":"2025-01-05","reason":"   "`))),
			[]string{"A-1", "R-1", "修订原因"},
		},
		"修订缺少修订日期字段": {
			state(archive("A-1", "2026-01-10", rev("R-1", "2025-01-10", "2026-01-10",
				`,"reason":"延长"`))),
			[]string{"A-1", "R-1", "修订日期"},
		},
		"修订日期为 null": {
			state(archive("A-1", "2026-01-10", rev("R-1", "2025-01-10", "2026-01-10",
				`,"revised_on":null,"reason":"延长"`))),
			[]string{"A-1", "R-1", "修订日期"},
		},
		"修订原因与修订日期同时缺少": {
			state(archive("A-1", "2026-01-10", rev("R-1", "2025-01-10", "2026-01-10", ""))),
			[]string{"A-1", "R-1", "修订原因", "修订日期"},
		},
		"空白原因与缺失日期同时报两项": {
			state(archive("A-1", "2026-01-10", rev("R-1", "2025-01-10", "2026-01-10",
				`,"reason":"  "`))),
			[]string{"A-1", "R-1", "修订原因", "修订日期"},
		},
		"中间修订缺少原因不能靠最终期限合法掩盖": {
			// 题目主例：先延长到 2026-01-10（中间那次没有原因），
			// 随后缩短到 2025-06-30，全部期限衔接、最终截止日合法。
			state(archive("A-1", "2025-06-30",
				rev("R-1", "2025-01-10", "2026-01-10", `,"revised_on":"2025-01-02"`)+","+
					rev("R-2", "2026-01-10", "2025-06-30", full))),
			[]string{"A-1", "R-1", "修订原因"},
		},
		"中间修订缺少日期同样拒绝": {
			state(archive("A-1", "2025-06-30",
				rev("R-1", "2025-01-10", "2026-01-10", `,"reason":"延长"`)+","+
					rev("R-2", "2026-01-10", "2025-06-30", full))),
			[]string{"A-1", "R-1", "修订日期"},
		},
		"已销毁档案的修订缺少原因": { // 清册归属、条目与处理日期都正确也不能掩盖修订信息缺项。
			destroyedState(destroyedArchive("A-1",
				rev("R-1", "2025-01-10", "2026-01-10", `,"revised_on":"2025-01-05"`))),
			[]string{"A-1", "R-1", "修订原因"},
		},
		"已销毁档案的修订缺少日期": {
			destroyedState(destroyedArchive("A-1",
				rev("R-1", "2025-01-10", "2026-01-10", `,"reason":"延长"`))),
			[]string{"A-1", "R-1", "修订日期"},
		},
		"多份档案中第二份的修订缺项": {
			state(archive("A-1", "2026-01-10", rev("R-1", "2025-01-10", "2026-01-10", full)) + "," +
				archive("A-2", "2026-01-10", rev("R-2", "2025-01-10", "2026-01-10", `,"reason":"延长"`))),
			[]string{"A-2", "R-2", "修订日期"},
		},
		"被冻结档案的修订缺少日期": {
			// 档案仍带一条未解除冻结：冻结不能掩盖修订信息缺项。
			`{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01",` +
				`"end":"2026-01-10","initial_end":"2025-01-10","destroyed":false,` +
				`"freezes":[{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-04","released":false}],` +
				`"revisions":[` + rev("R-1", "2025-01-10", "2026-01-10", `,"reason":"延长"`) +
				`]}},"manifests":{}}`,
			[]string{"A-1", "R-1", "修订日期"},
		},
		"修订日期已填写但不是合法日历日期仍损坏": {
			// 2025 年不是闰年，没有 2 月 29 日：已填写非法日期继续按损坏处理
			//（在 JSON 解码阶段即失败，同样包装为 ErrCorruptState）。
			state(archive("A-1", "2026-01-10", rev("R-1", "2025-01-10", "2026-01-10",
				`,"revised_on":"2025-02-29","reason":"延长"`))),
			nil,
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

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
			if got := readStateFile(t, dir); got != tc.state {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：修订原因与修订日期齐备的记录可正常打开；没有修订记录的旧
	// 档案（甚至没有 revisions 字段）不因不存在修订原因和修订日期被拒绝。
	t.Run("合法记录对照组", func(t *testing.T) {
		valid := map[string]string{
			"修订信息齐备": state(archive("A-1", "2026-01-10",
				rev("R-1", "2025-01-10", "2026-01-10", full))),
			"没有修订列表的旧档案": `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同",` +
				`"start":"2020-01-01","end":"2025-01-10","destroyed":false,"freezes":[]}},"manifests":{}}`,
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
// 销毁前核对与各项办理都必须按整库记录损坏失败——即使本次操作的是另一份
// 正常档案——不返回正常历史或部分核对结果，不产生业务变更，也不借办理操作
// 把内容重新保存，原保存内容保持原样。
func TestRevisionMissingOriginInfoAfterOpenFailsAllOperations(t *testing.T) {
	cases := map[string]func(map[string]any){
		"第一条修订原因被改成空白": func(doc map[string]any) {
			revs := doc["archives"].(map[string]any)["A-1"].(map[string]any)["revisions"].([]any)
			revs[0].(map[string]any)["reason"] = "   "
		},
		"中间修订原因字段被删除": func(doc map[string]any) {
			revs := doc["archives"].(map[string]any)["A-1"].(map[string]any)["revisions"].([]any)
			delete(revs[0].(map[string]any), "reason")
		},
		"中间修订日期被改成 null": func(doc map[string]any) {
			revs := doc["archives"].(map[string]any)["A-1"].(map[string]any)["revisions"].([]any)
			revs[0].(map[string]any)["revised_on"] = nil
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
			// 先延长、随后缩短：最终截止日 2025-06-30 合法且期限衔接，
			// 缺项的是中间那次延长，不能被最终合法期限掩盖。
			revise(t, s, "R-1", "A-1", "2025-01-10", "2026-01-10", "2025-01-02", "延长")
			revise(t, s, "R-2", "A-1", "2026-01-10", "2025-06-30", "2025-01-03", "缩短")
			if _, found, err := s.History("A-2"); err != nil || !found {
				t.Fatalf("损坏前查询应成功: found=%v err=%v", found, err)
			}

			rewriteState(t, dir, mutate)
			corruptContent := readStateFile(t, dir)

			// 重新打开必须失败，错误指出档案编号、修订编号与缺少的项目。
			s2, err := Open(dir)
			if err == nil {
				s2.Close()
				t.Fatal("修订办理信息缺项时，打开必须失败")
			}
			if !errors.Is(err, ErrCorruptState) ||
				!strings.Contains(err.Error(), "A-1") || !strings.Contains(err.Error(), "R-1") {
				t.Fatalf("打开错误应为 ErrCorruptState 并指出 A-1/R-1，得到 %v", err)
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

			// 失败不得触发修复：不补写原因、不借用其他日期、不删除那次修订，
			// 也不借办理操作把内容重新保存。
			if got := readStateFile(t, dir); got != corruptContent {
				t.Fatalf("失败后原保存内容被改动:\n%q", got)
			}
		})
	}
}

// 正常提交修订仍要求原因与日期齐备，原因去除首尾空白后保存，成功历史按
// 办理顺序保留原因与修订日期——读取侧的缺项校验不影响办理入口的既有行为。
func TestReviseInputValidationAndHistoryUnchanged(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-1", ArchiveID: "A-1",
		OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
		RevisedOn: MustParseDate("2025-01-05"), Reason: "  ",
	}); !errors.Is(err, ErrBlankField) {
		t.Fatalf("空白修订原因仍应拒绝: %v", err)
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-1", ArchiveID: "A-1",
		OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
		Reason: "延长",
	}); !errors.Is(err, ErrInvalidDate) {
		t.Fatalf("缺失修订日期仍应拒绝: %v", err)
	}

	rec := revise(t, s, "R-1", "A-1", "2025-01-10", "2026-01-10", "2025-01-05", "  业务需要延长  ")
	if rec.Reason != "业务需要延长" || !rec.RevisedOn.Equal(MustParseDate("2025-01-05")) {
		t.Fatalf("成功修订应返回去除空白后的原因与修订日期: %+v", rec)
	}
	h, found, err := s.History("A-1")
	if err != nil || !found || len(h.Revisions) != 1 {
		t.Fatalf("历史应包含一条修订: found=%v err=%v %+v", found, err, h)
	}
	if h.Revisions[0].Reason != "业务需要延长" ||
		!h.Revisions[0].RevisedOn.Equal(MustParseDate("2025-01-05")) ||
		!h.RetentionEnd.Equal(MustParseDate("2026-01-10")) {
		t.Fatalf("历史中的修订原因与日期应完整保留，当前期限取新截止日: %+v", h.Revisions[0])
	}

	// 重新打开后原因与修订日期仍完整可查。
	s2, err := Open(s.dir)
	if err != nil {
		t.Fatalf("合法修订历史重开应成功: %v", err)
	}
	defer s2.Close()
	h2, found, err := s2.History("A-1")
	if err != nil || !found || len(h2.Revisions) != 1 {
		t.Fatalf("重开后历史应完整: found=%v err=%v %+v", found, err, h2)
	}
	if h2.Revisions[0].Reason != "业务需要延长" ||
		!h2.Revisions[0].RevisedOn.Equal(MustParseDate("2025-01-05")) {
		t.Fatalf("重开后修订原因与日期不应丢失: %+v", h2.Revisions[0])
	}
}
