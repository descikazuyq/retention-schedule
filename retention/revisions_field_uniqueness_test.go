package retention

import (
	"errors"
	"strings"
	"testing"
)

// 同一份档案自己的保存内容中，修订列表 revisions 最多只能出现一次：普通解码
// 对同名字段只保留最后一个值，同一份档案记录中若先保存了包含两条期限修订的
// revisions（R-1 延长、R-2 又缩短回最初截止日），后面又保存一个空的
// revisions，读取仍会成功，当前期限仍与最初期限一致，两次修订却从历史中消失，
// 已占用的修订编号还可能重新用于业务。Open 必须按保存记录损坏失败
// （ErrCorruptState），错误指出档案编号，并说明重复的是修订列表 revisions
// 本身，不能误报为某个修订编号重复；两处列表完全相同、分别保存不同修订、其中
// 一处为空列表或 null 都按同一规则拒绝，不合并、不挑选其中一份、不删除历史，
// 调整保存顺序也不能改变拒绝结果，原文件保持原样。
func TestOpenRejectsDuplicateRevisionsField(t *testing.T) {
	// R-1 把截止日从 2025-01-10 延长到 2026-01-10，R-2 又缩短回 2025-01-10：
	// 当前期限与最初期限一致，但两条修订的编号与历史都已被占用。
	rev1 := `{"id":"R-1","old_end":"2025-01-10","new_end":"2026-01-10","revised_on":"2024-12-01","reason":"延长"}`
	rev2 := `{"id":"R-2","old_end":"2026-01-10","new_end":"2025-01-10","revised_on":"2025-01-05","reason":"缩短"}`
	// rec 构造一份登记记录，revisionsFields 是原样拼接进记录的修订列表字段
	// （可以写零个、一个或多个，字段名写法与列表内容任意）。
	rec := func(id string, revisionsFields ...string) string {
		fields := `"id":"` + id + `","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":null`
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
		// 反引号字符串里的 \u0072 是字面的反斜杠+u0072，JSON 解码后才是字符 r，
		// 与直接写出的 revisions 是同一个字段名。
		"大小写写法混用重复":   state(rec("A-1", `"Revisions":[`+rev1+`,`+rev2+`]`, `"revisions":[]`)),
		"大小写写法混用顺序相反": state(rec("A-1", `"revisions":[`+rev1+`,`+rev2+`]`, `"Revisions":[]`)),
		"修订列表出现三次":    state(rec("A-1", `"revisions":[`+rev1+`,`+rev2+`]`, `"revisions":[]`, `"revisions":null`)),
	}
	// 转义写法需要字面反斜杠，单独构造。
	corrupt["Unicode转义字段名与直接字段名重复"] = state(rec("A-1", `"\u0072evisions":[`+rev1+`,`+rev2+`]`, `"\u0072evisions":[]`))
	corrupt["Unicode转义字段名与直接字段名重复顺序相反"] = state(rec("A-1", `"\u0072evisions":[]`, `"\u0072evisions":[`+rev1+`,`+rev2+`]`))

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
			// 不得误报为修订编号重复：重复的是列表字段本身，与具体修订编号无关。
			if strings.Contains(msg, "修订编号") {
				t.Fatalf("错误信息不应误报为修订编号重复，得到 %v", err)
			}
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：修订列表缺省、仅一次为 null 或仅有一个空列表时继续表示没有修订；
	// 现有能识别为修订列表的大小写写法与转义写法单独出现时继续可读；不同档案各自
	// 的修订列表不合在一起计数；只保存一次合法列表时全部修订及原顺序保留。
	t.Run("合法记录对照组", func(t *testing.T) {
		valid := map[string]string{
			"修订列表缺省":             state(rec("A-1")),
			"revisions仅一次为null":  state(rec("A-1", `"revisions":null`)),
			"仅有一个空列表":            state(rec("A-1", `"revisions":[]`)),
			"大小写写法Revisions单独出现": state(rec("A-1", `"Revisions":[`+rev1+`,`+rev2+`]`)),
			"大小写写法REVISIONS单独出现": state(rec("A-1", `"REVISIONS":[`+rev1+`,`+rev2+`]`)),
			// 不同档案各自带自己的修订列表是正常保存格式，不合在一起计数；
			// 修订编号全库唯一，两份档案各自使用不同的修订编号。
			"不同档案各自一个修订列表": `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2026-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":null,"revisions":[` + rev1 + `]},` +
				`"A-2":{"id":"A-2","category":"凭证","start":"2020-01-01","end":"2026-06-30","initial_end":"2025-06-30","destroyed":false,"freezes":null,"revisions":[{"id":"R-3","old_end":"2025-06-30","new_end":"2026-06-30","revised_on":"2025-06-01","reason":"延期"}]}},"manifests":{}}`,
			// 合法列表里的全部修订按原顺序保留，当前截止日为末次修订的新截止日。
			"合法列表保留全部修订与原顺序": state(rec("A-1", `"revisions":[`+rev1+`,`+rev2+`]`)),
		}
		// 转义写法需要字面反斜杠，单独构造。
		valid["Unicode转义字段名单独出现"] = state(rec("A-1", `"\u0072evisions":[`+rev1+`,`+rev2+`]`))
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

	// 只保存一次合法列表时，全部修订及原顺序、当前截止日、编号占用和相同提交
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
		if !h.InitialEnd.Equal(MustParseDate("2025-01-10")) {
			t.Fatalf("最初截止日应为 2025-01-10，得到 %s", h.InitialEnd)
		}
		if !h.RetentionEnd.Equal(MustParseDate("2025-01-10")) {
			t.Fatalf("当前截止日应已被 R-2 缩短回 2025-01-10，得到 %s", h.RetentionEnd)
		}
		if len(h.Revisions) != 2 || h.Revisions[0].RevisionID != "R-1" || h.Revisions[1].RevisionID != "R-2" {
			t.Fatalf("修订历史应按原顺序保留 R-1、R-2: %+v", h.Revisions)
		}
		if !h.Revisions[0].OldEnd.Equal(MustParseDate("2025-01-10")) ||
			!h.Revisions[0].NewEnd.Equal(MustParseDate("2026-01-10")) ||
			!h.Revisions[1].OldEnd.Equal(MustParseDate("2026-01-10")) ||
			!h.Revisions[1].NewEnd.Equal(MustParseDate("2025-01-10")) {
			t.Fatalf("两条修订的期限衔接应完整保留: %+v", h.Revisions)
		}
		// 编号占用保持不变：相同提交取回原记录，改动任一项即编号冲突。
		got, err := s.Revise(ReviseInput{
			RevisionID: "R-1", ArchiveID: "A-1",
			OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
			RevisedOn: MustParseDate("2024-12-01"), Reason: "延长",
		})
		if err != nil || got.RevisionID != "R-1" || !got.NewEnd.Equal(MustParseDate("2026-01-10")) {
			t.Fatalf("相同提交应取回原修订记录: %+v err=%v", got, err)
		}
		if _, err := s.Revise(ReviseInput{
			RevisionID: "R-1", ArchiveID: "A-1",
			OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2027-01-10"),
			RevisedOn: MustParseDate("2024-12-01"), Reason: "延长",
		}); !errors.Is(err, ErrRevisionConflict) {
			t.Fatalf("占用编号改动内容应返回 ErrRevisionConflict，得到 %v", err)
		}
		// 当前截止日仍是最初期限：截止日当天核对应可以办理并完成销毁。
		r, err := s.Check(CheckRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
		})
		if err != nil || r.Status != CheckReady {
			t.Fatalf("缩短回最初期限后截止日当天应可以办理: %+v err=%v", r, err)
		}
		if _, err := s.Destroy(DestructionRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
		}); err != nil {
			t.Fatalf("截止日当天应能销毁: %v", err)
		}
	})

	// 已销毁档案同样适用：已销毁档案的保存内容中出现两次修订列表也按损坏拒绝，
	// 合法的期限衔接及清册内容不能掩盖列表重复；只出现一次合法列表的已销毁档案
	// （期限最终改回截止日、清册条目与处理日期都正确）继续可读。
	t.Run("已销毁档案同样适用", func(t *testing.T) {
		manifest := `{"application_id":"APP-1","processed_on":"2025-01-10","entries":[{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}]}`
		destroyedRec := func(revisionsFields ...string) string {
			fields := `"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1","freezes":null`
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
		if strings.Contains(err.Error(), "修订编号") {
			t.Fatalf("错误信息不应误报为修订编号重复，得到 %v", err)
		}
		if got := readStateFile(t, dir); got != content {
			t.Fatalf("打开失败后原文件被改动:\n%q", got)
		}

		// 对照：已销毁档案只保存一个修订列表（期限衔接完整、清册正确）时继续可读。
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

// 保管库打开后保存内容才出现重复的修订列表：下一次使用有效输入查询历史、取回
// 清册、销毁前核对与各项办理都必须按整库记录损坏失败——即使操作的是另一份没有
// 问题的档案——不返回正常历史、清册或部分核对结果，不改变档案状态或生成清册，
// 原保存内容保持原样。
func TestDuplicateRevisionsFieldAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")
	reg(t, s, "A-3", "单据", "2020-01-01", "2025-01-10")
	// A-1 先延长到 2026-01-10、又缩短回 2025-01-10：当前期限与最初期限一致，
	// 但 R-1、R-2 两条修订及其编号占用必须保留。
	revise(t, s, "R-1", "A-1", "2025-01-10", "2026-01-10", "2024-12-01", "延长")
	revise(t, s, "R-2", "A-1", "2026-01-10", "2025-01-10", "2025-01-05", "缩短")
	// A-2 保留一条未解除冻结，供损坏后验证解除办理同样整库失败。
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-2", FreezeID: "F-2", Reason: "诉讼", FrozenOn: MustParseDate("2025-06-01"),
	}); err != nil {
		t.Fatal(err)
	}
	// A-3 先冻结再解除、随后合法销毁，供损坏后验证“取回清册”同样整库失败。
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-3", FreezeID: "F-3", Reason: "保全", FrozenOn: MustParseDate("2025-01-06"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-3", FreezeID: "F-3", Reason: "结案", ReleasedOn: MustParseDate("2025-01-08"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-3", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-3"},
	}); err != nil {
		t.Fatal(err)
	}

	// 在 A-1 的登记记录里再写入一个修订列表：先一个空列表、再是原来保存着
	// R-1、R-2 两条修订的列表——普通解码会只剩后一个（期限恰好仍衔接），但两处
	// 保存本身已使记录不可信。档案编号按排序保存，且只有 A-1 保存了修订列表，
	// 文件中第一个 revisions 字段就是 A-1 的。
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
	if m, found, err := s.GetManifest("APP-3"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("损坏后 GetManifest 必须失败: found=%v err=%v %+v", found, err, m)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出可以办理等结论或部分报告: %+v err=%v", r, err)
	}
	// 关键回归点：第二处保留着完整的两条修订且期限恰好衔接，普通解码下 A-1 在
	// 截止日当天会显示可以办理；损坏状态下绝不能据此给出结论或完成销毁。
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-8", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后对 A-1 的核对也必须失败，不能因后一处列表恰好完整而误报可以办理: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后不得凭重复保存的修订列表销毁 A-1: %+v err=%v", m, err)
	}
	// 已成功申请的幂等重放同样不能取回原清册。
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-3", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-3"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后已成功申请的重放也必须失败且不得返回原清册: %+v err=%v", m, err)
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
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-2", FreezeID: "F-2", Reason: "结案", ReleasedOn: MustParseDate("2025-06-02"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Release 必须失败: %v", err)
	}
	// 已占用编号 R-1 的相同提交（本应幂等取回原修订）也必须按整库损坏失败。
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-1", ArchiveID: "A-1",
		OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
		RevisedOn: MustParseDate("2024-12-01"), Reason: "延长",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后已占用编号的相同提交也必须失败，不能取回原修订: %v", err)
	}
	// 新编号修订（即使操作另一份正常档案）同样失败，不能占用编号。
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-9", ArchiveID: "A-2",
		OriginalEnd: MustParseDate("2025-06-30"), NewEnd: MustParseDate("2026-06-30"),
		RevisedOn: MustParseDate("2025-06-01"), Reason: "延期",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Revise 必须失败: %v", err)
	}

	// 失败不得触发修复：不合并两处列表、不挑选其中一份、不删除历史或重新保存来
	// 消除重复，原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 去掉重复的修订列表后，合法记录恢复可用：R-1、R-2 两条修订仍按原顺序保留，
	// 当前截止日为 2025-01-10，编号占用与相同提交取回原记录的行为不变；无关档案
	// 业务不受影响。
	writeStateFile(t, dir, good)
	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("恢复后查询应成功: found=%v err=%v", found, err)
	}
	if len(h.Revisions) != 2 || h.Revisions[0].RevisionID != "R-1" || h.Revisions[1].RevisionID != "R-2" {
		t.Fatalf("恢复后 A-1 应仍按原顺序保留 R-1、R-2 两条修订: %+v", h.Revisions)
	}
	if !h.InitialEnd.Equal(MustParseDate("2025-01-10")) || !h.RetentionEnd.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("恢复后最初与当前截止日都应为 2025-01-10: initial=%s current=%s", h.InitialEnd, h.RetentionEnd)
	}
	got, err := s.Revise(ReviseInput{
		RevisionID: "R-1", ArchiveID: "A-1",
		OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
		RevisedOn: MustParseDate("2024-12-01"), Reason: "延长",
	})
	if err != nil || got.RevisionID != "R-1" {
		t.Fatalf("恢复后相同提交应取回原修订 R-1: %+v err=%v", got, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-8", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil || r.Status != CheckReady {
		t.Fatalf("恢复后 A-1 在截止日当天应可以办理: %+v err=%v", r, err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatalf("恢复后 A-1 应能正常办理销毁: %v", err)
	}
	// 无关档案 A-2 的新修订也应能正常办理（先解除 F-2）。
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-2", FreezeID: "F-2", Reason: "结案", ReleasedOn: MustParseDate("2025-06-02"),
	}); err != nil {
		t.Fatalf("恢复后解除 A-2 的冻结应成功: %v", err)
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-9", ArchiveID: "A-2",
		OriginalEnd: MustParseDate("2025-06-30"), NewEnd: MustParseDate("2026-06-30"),
		RevisedOn: MustParseDate("2025-06-01"), Reason: "延期",
	}); err != nil {
		t.Fatalf("恢复后无关档案应能正常修订: %v", err)
	}
}
