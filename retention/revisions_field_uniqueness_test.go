package retention

import (
	"errors"
	"strings"
	"testing"
)

// 同一份档案自己的保存内容中，修订列表 revisions 最多只能出现一次：普通解码对
// 同名字段只保留最后一个值，同一份档案记录中若第一处保存着先延长、后缩短回
// 最初期限的两条修订（最初截止日 2025-01-10，R-1 延长到 2026-01-10，R-2 又
// 缩短回 2025-01-10），第二处又保存一个空列表，读取后当前截止日仍与最初期限
// 一致、保管库看似能正常打开，但两次修订已经消失，已占用的修订编号也可能被
// 重新用于新业务。Open 必须按保存记录损坏失败（ErrCorruptState），错误指出
// 档案编号，并说明重复的是修订列表 revisions 本身，不能误报为某个修订编号
// 重复；两处列表完全相同、分别保存不同修订、其中一处为空列表或 null 都按同一
// 规则拒绝，不合并、不挑选其中一份，互换两处的保存顺序也不能改变拒绝结果，
// 原文件保持原样。
func TestOpenRejectsDuplicateRevisionsField(t *testing.T) {
	// 与任务场景一致的两条修订：先延长、后缩短回最初期限。
	rev1 := `{"id":"R-1","old_end":"2025-01-10","new_end":"2026-01-10","revised_on":"2024-12-01","reason":"业务需要"}`
	rev2 := `{"id":"R-2","old_end":"2026-01-10","new_end":"2025-01-10","revised_on":"2025-01-05","reason":"重新评估"}`
	// rec 构造一份登记记录，revisionsFields 是原样拼接进记录的修订列表字段
	// （可以写零个、一个或多个，字段名写法与列表内容任意）。当前截止日与
	// 最初截止日都是 2025-01-10：若重复列表被静默覆盖，保管库看似正常。
	rec := func(id string, revisionsFields ...string) string {
		fields := `"id":"` + id + `","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false`
		for _, f := range revisionsFields {
			fields += "," + f
		}
		return `{` + fields + `}`
	}
	state := func(archiveRec string) string {
		return `{"version":1,"archives":{"A-1":` + archiveRec + `},"manifests":{}}`
	}

	corrupt := map[string]string{
		"先保存两条修订后保存空列表": state(rec("A-1", `"revisions":[`+rev1+`,`+rev2+`]`, `"revisions":[]`)),
		"保存顺序相反同样拒绝":    state(rec("A-1", `"revisions":[]`, `"revisions":[`+rev1+`,`+rev2+`]`)),
		"两处列表完全相同":      state(rec("A-1", `"revisions":[`+rev1+`,`+rev2+`]`, `"revisions":[`+rev1+`,`+rev2+`]`)),
		"两处分别保存不同修订":    state(rec("A-1", `"revisions":[`+rev1+`]`, `"revisions":[`+rev2+`]`)),
		"第二处为空列表":       state(rec("A-1", `"revisions":[`+rev1+`,`+rev2+`]`, `"revisions":[]`)),
		"第一处为空列表":       state(rec("A-1", `"revisions":[]`, `"revisions":[`+rev1+`,`+rev2+`]`)),
		"第二处为null":      state(rec("A-1", `"revisions":[`+rev1+`,`+rev2+`]`, `"revisions":null`)),
		"第一处为null":      state(rec("A-1", `"revisions":null`, `"revisions":[`+rev1+`,`+rev2+`]`)),
		"两处都为null":      state(rec("A-1", `"revisions":null`, `"revisions":null`)),
		// 大小写写法（Revisions）与标准写法混用也算同一字段重复。
		"大小写写法混用重复":   state(rec("A-1", `"Revisions":[`+rev1+`,`+rev2+`]`, `"revisions":[]`)),
		"大小写写法混用顺序相反": state(rec("A-1", `"revisions":[`+rev1+`,`+rev2+`]`, `"Revisions":[]`)),
		"修订列表出现三次":    state(rec("A-1", `"revisions":[`+rev1+`]`, `"revisions":[`+rev2+`]`, `"revisions":null`)),
	}
	// 转义写法需要在 JSON 文本中出现字面的反斜杠加 u0072（解码后即为字符 r，
	// 字段名 evisions 前拼上它即为 revisions），用 Go 字符串拼接构造。
	bs := "\\"
	corrupt["Unicode转义字段名与直接字段名重复"] = state(rec("A-1", `"revisions":[`+rev1+`,`+rev2+`]`, `"`+bs+`u0072evisions":[]`))
	corrupt["Unicode转义字段名与直接字段名重复顺序相反"] = state(rec("A-1", `"`+bs+`u0072evisions":[]`, `"revisions":[`+rev1+`,`+rev2+`]`))

	for name, content := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, content)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("单份档案的保存内容中出现两次修订列表时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "A-1") {
				t.Fatalf("错误信息应指出档案编号 A-1，得到 %v", err)
			}
			if !strings.Contains(msg, "revisions") || !strings.Contains(msg, "修订列表") {
				t.Fatalf("错误信息应说明重复的是修订列表 revisions，得到 %v", err)
			}
			// 不得误报为修订编号重复：重复的是列表字段本身，与修订编号无关。
			if strings.Contains(msg, "修订编号") {
				t.Fatalf("错误信息不应误报为修订编号重复，得到 %v", err)
			}
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：没有修订的旧档案继续缺省修订列表；单次 null 或空列表继续表示没有
	// 修订；现有能识别为修订列表的大小写写法与转义写法单独出现时继续可读；不同
	// 档案各自的修订列表不合在一起计数。
	t.Run("合法记录对照组", func(t *testing.T) {
		legacy := func(id string) string {
			return `{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"2025-01-10","destroyed":false}`
		}
		valid := map[string]string{
			"修订列表缺省":             `{"version":1,"archives":{"A-1":` + legacy("A-1") + `},"manifests":{}}`,
			"revisions仅一次为null":  state(rec("A-1", `"revisions":null`)),
			"仅有一个空列表":            state(rec("A-1", `"revisions":[]`)),
			"大小写写法Revisions单独出现": state(rec("A-1", `"Revisions":[`+rev1+`,`+rev2+`]`)),
			"大小写写法REVISIONS单独出现": state(rec("A-1", `"REVISIONS":[`+rev1+`,`+rev2+`]`)),
			// 不同档案各自带自己的修订列表是正常保存格式，不合在一起计数。
			"不同档案各自一个修订列表": `{"version":1,"archives":{"A-1":` + rec("A-1", `"revisions":[`+rev1+`,`+rev2+`]`) +
				`,"A-2":{"id":"A-2","category":"凭证","start":"2020-01-01","end":"2026-06-30","initial_end":"2025-06-30","destroyed":false,"revisions":[{"id":"R-9","old_end":"2025-06-30","new_end":"2026-06-30","revised_on":"2025-01-02","reason":"延期"}]}},"manifests":{}}`,
		}
		// 转义写法单独出现：字段名以反斜杠加 u0072 转义写出，解码后仍是 revisions。
		valid["Unicode转义字段名单独出现"] = state(rec("A-1", `"`+bs+`u0072evisions":[`+rev1+`,`+rev2+`]`))
		for name, content := range valid {
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				writeStateFile(t, dir, content)
				s, err := Open(dir)
				if err != nil {
					t.Fatalf("合法记录应能打开: %v", err)
				}
				defer s.Close()
			})
		}
	})

	// 只保存一次合法列表时，全部修订及原顺序、当前截止日、编号占用与相同提交
	// 取回原记录的行为保持不变。
	t.Run("合法列表内容完整保留", func(t *testing.T) {
		dir := t.TempDir()
		writeStateFile(t, dir, state(rec("A-1", `"revisions":[`+rev1+`,`+rev2+`]`)))
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("合法记录应能打开: %v", err)
		}
		defer s.Close()
		h, found, err := s.History("A-1")
		if err != nil || !found {
			t.Fatalf("查询应成功: found=%v err=%v", found, err)
		}
		if len(h.Revisions) != 2 || h.Revisions[0].RevisionID != "R-1" || h.Revisions[1].RevisionID != "R-2" {
			t.Fatalf("修订历史应按原顺序保留 R-1、R-2: %+v", h.Revisions)
		}
		if !h.InitialEnd.Equal(MustParseDate("2025-01-10")) || !h.RetentionEnd.Equal(MustParseDate("2025-01-10")) {
			t.Fatalf("最初截止日与当前截止日都应为 2025-01-10: %+v", h)
		}
		// 编号占用不变：R-1 已被占用，改变内容的重提必须冲突。
		if _, err := s.Revise(ReviseInput{
			RevisionID: "R-1", ArchiveID: "A-1",
			OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2027-01-10"),
			RevisedOn: MustParseDate("2024-12-01"), Reason: "业务需要",
		}); !errors.Is(err, ErrRevisionConflict) {
			t.Fatalf("已占用的修订编号改内容重提应返回 ErrRevisionConflict，得到 %v", err)
		}
		// 相同提交取回原记录：内容完全相同的重试返回第一次成功的记录，不新增修订。
		rec, err := s.Revise(ReviseInput{
			RevisionID: "R-1", ArchiveID: "A-1",
			OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
			RevisedOn: MustParseDate("2024-12-01"), Reason: "业务需要",
		})
		if err != nil || rec.RevisionID != "R-1" || !rec.NewEnd.Equal(MustParseDate("2026-01-10")) {
			t.Fatalf("相同提交应取回原修订记录: %+v err=%v", rec, err)
		}
		h, _, _ = s.History("A-1")
		if len(h.Revisions) != 2 {
			t.Fatalf("幂等重试不应新增修订记录: %+v", h.Revisions)
		}
	})

	// 已销毁档案同样适用：已销毁档案的保存内容中出现两次修订列表也按损坏拒绝；
	// 只出现一次的合法已销毁档案继续可读。
	t.Run("已销毁档案同样适用", func(t *testing.T) {
		manifest := `{"application_id":"APP-1","processed_on":"2025-01-10","entries":[{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}]}`
		destroyedRec := func(revisionsFields ...string) string {
			fields := `"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1"`
			for _, f := range revisionsFields {
				fields += "," + f
			}
			return `{` + fields + `}`
		}
		stateWithManifest := func(archiveRec string) string {
			return `{"version":1,"archives":{"A-1":` + archiveRec + `},"manifests":{"APP-1":` + manifest + `}}`
		}

		dir := t.TempDir()
		content := stateWithManifest(destroyedRec(`"revisions":[`+rev1+`,`+rev2+`]`, `"revisions":[]`))
		writeStateFile(t, dir, content)
		s, err := Open(dir)
		if err == nil {
			s.Close()
			t.Fatal("已销毁档案的保存内容中出现两次修订列表时不应打开成功")
		}
		if !errors.Is(err, ErrCorruptState) ||
			!strings.Contains(err.Error(), "A-1") ||
			!strings.Contains(err.Error(), "revisions") {
			t.Fatalf("打开错误应为 ErrCorruptState 并指出档案编号与修订列表 revisions，得到 %v", err)
		}
		if got := readStateFile(t, dir); got != content {
			t.Fatalf("打开失败后原文件被改动:\n%q", got)
		}

		// 对照：已销毁档案只保存一个修订列表时继续可读，清册归属不受影响。
		dir2 := t.TempDir()
		writeStateFile(t, dir2, stateWithManifest(destroyedRec(`"revisions":[`+rev1+`,`+rev2+`]`)))
		s2, err := Open(dir2)
		if err != nil {
			t.Fatalf("合法的已销毁档案应能打开: %v", err)
		}
		defer s2.Close()
		h, found, err := s2.History("A-1")
		if err != nil || !found || !h.Destroyed || len(h.Revisions) != 2 {
			t.Fatalf("已销毁档案的历史与修订记录应完整可查: %+v found=%v err=%v", h, found, err)
		}
	})
}

// 保管库打开后保存内容才出现重复的修订列表：下一次使用有效输入查询、核对或办理
// 都必须按整库记录损坏失败——即使操作的是另一份正常档案——不返回部分结果、
// 不写入业务变更，原保存内容保持原样。
func TestDuplicateRevisionsFieldAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")
	// A-1 先延长、后缩短回最初期限：当前截止日与最初截止日相同，若重复保存的
	// 空列表顶替了真实修订历史，保管库看似完全正常。
	revise(t, s, "R-1", "A-1", "2025-01-10", "2026-01-10", "2024-12-01", "业务需要")
	revise(t, s, "R-2", "A-1", "2026-01-10", "2025-01-10", "2025-01-05", "重新评估")

	// 在 A-1 的登记记录里再写入一个修订列表：先一个空列表、再是原来保存着
	// 两条修订的列表——普通解码会只剩后一个，但两处保存本身已使记录不可信。
	// 档案编号按排序保存，A-1 的记录是文件中第一个 revisions 字段。
	good := readStateFile(t, dir)
	corrupt := strings.Replace(good, `"revisions"`, `"revisions": [],
      "revisions"`, 1)
	if corrupt == good {
		t.Fatal("未找到 A-1 记录的修订列表位置，测试夹具失效")
	}
	writeStateFile(t, dir, corrupt)
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误指出档案编号并说明重复的是修订列表 revisions。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("单份档案的保存内容中出现两个修订列表时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "A-1") ||
		!strings.Contains(err.Error(), "revisions") ||
		!strings.Contains(err.Error(), "修订列表") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出档案 A-1 的修订列表 revisions 重复，得到 %v", err)
	}
	if strings.Contains(err.Error(), "修订编号") {
		t.Fatalf("错误信息不应误报为修订编号重复，得到 %v", err)
	}

	// 已打开实例：即使本次只操作没有问题的正常档案 A-2，也必须按整库损坏失败。
	if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后查询无关档案也必须失败且无结论: found=%v err=%v", found, err)
	}
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History 必须失败且不得给出部分历史: found=%v err=%v", found, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出可以办理等结论或部分报告: %+v err=%v", r, err)
	}
	// 关键回归点：损坏状态下绝不能把被顶替的修订历史当成没有修订的依据，
	// 在截止日当天给出可以办理甚至完成销毁。
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-8", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后对 A-1 的核对也必须失败，不能误报可以办理: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后不得凭被顶替的修订历史销毁 A-1: %+v err=%v", m, err)
	}
	if err := s.Register(RegisterInput{
		ID: "A-4", Category: "凭证", Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-06-30"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Register 必须失败: %v", err)
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-2", FreezeID: "F-X", Reason: "诉讼", FrozenOn: MustParseDate("2025-06-01"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Freeze 必须失败: %v", err)
	}
	// 损坏状态下已消失的修订编号绝不能被重新用于新业务。
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-1", ArchiveID: "A-2",
		OriginalEnd: MustParseDate("2025-06-30"), NewEnd: MustParseDate("2026-06-30"),
		RevisedOn: MustParseDate("2025-06-01"), Reason: "延期",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Revise 必须失败，已占用的修订编号不得重新使用: %v", err)
	}

	// 失败不得触发修复：不合并两处列表、不挑选其中一份、不重新保存来消除重复，
	// 原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 去掉重复的修订列表后，合法记录恢复可用：A-1 的两条修订仍按原顺序保留，
	// 编号占用与幂等重放行为不变；无关档案业务不受影响。
	writeStateFile(t, dir, good)
	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("恢复后查询应成功: found=%v err=%v", found, err)
	}
	if len(h.Revisions) != 2 || h.Revisions[0].RevisionID != "R-1" || h.Revisions[1].RevisionID != "R-2" {
		t.Fatalf("恢复后 A-1 应仍保留两条修订 R-1、R-2: %+v", h.Revisions)
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-1", ArchiveID: "A-2",
		OriginalEnd: MustParseDate("2025-06-30"), NewEnd: MustParseDate("2026-06-30"),
		RevisedOn: MustParseDate("2025-06-01"), Reason: "延期",
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("恢复后修订编号 R-1 仍应被占用: %v", err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); err != nil {
		t.Fatalf("恢复后无关档案应能正常办理销毁: %v", err)
	}
}
