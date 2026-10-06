package retention

import (
	"errors"
	"strings"
	"testing"
)

// 同一份档案记录中的冻结列表字段 freezes 最多只能出现一次：普通解码对同名
// 字段只保留最后一个值，一份档案先保存包含未解除诉讼冻结的 freezes、后面又
// 保存一个空的 freezes 时，读取仍会成功，前一次保存的冻结及解除历史会被后
// 一个空列表静默覆盖，到期后的销毁前核对会误报可以办理。Open 必须按保存
// 记录损坏失败（ErrCorruptState），错误指出档案编号，并说明重复的是冻结列表
// freezes 本身，而不是误报某个冻结编号重复；两处列表完全相同、分别保存不同
// 冻结、其中一处为空列表或 null 都按同一规则拒绝，不合并、不挑选其中一份、
// 不重新保存来消除重复，调整两处的保存顺序也不能改变拒绝结果，原文件保持
// 原样。重复只在单份档案自己的保存内容中计数：不同档案各自带有 freezes 是
// 正常格式，尚未销毁和已销毁档案都适用。
func TestOpenRejectsDuplicateFreezesField(t *testing.T) {
	f1 := `{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false}`
	f2 := `{"id":"F-2","reason":"审计","frozen_on":"2025-01-06","released":false}`
	f1Released := `{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-07"}`
	// rec 构造一份登记记录，extra 原样插入记录内部，用来放置第二个 freezes。
	rec := func(id, end, destroyed, manifestID, freezes, extra string) string {
		m := ""
		if manifestID != "" {
			m = `,"manifest_id":"` + manifestID + `"`
		}
		return `{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"` + end +
			`","initial_end":"` + end + `","destroyed":` + destroyed + m +
			`,"freezes":` + freezes + extra + `,"revisions":null}`
	}
	a1 := func(freezes, extra string) string {
		return rec("A-1", "2025-01-10", "false", "", freezes, extra)
	}
	state := func(archiveRecord string) string {
		return `{"version":1,"archives":{"A-1":` + archiveRecord + `},"manifests":{}}`
	}
	// 已销毁档案 A-1，所属清册 APP-1 归属、条目与处理日期均合法；两处冻结
	// 列表分别看都不与销毁状态矛盾（第一处冻结已解除，第二处为空），重复仍拒绝。
	destroyedState := func(freezes, extra string) string {
		r := rec("A-1", "2025-01-10", "true", "APP-1", freezes, extra)
		return `{"version":1,"archives":{"A-1":` + r + `},"manifests":{"APP-1":` +
			`{"application_id":"APP-1","processed_on":"2025-01-10","entries":[` +
			`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}]}}}`
	}

	corrupt := map[string]string{
		"先有未解除冻结后有空列表": state(a1(`[`+f1+`]`, `,"freezes":[]`)),
		"保存顺序相反仍拒绝":    state(a1(`[]`, `,"freezes":[`+f1+`]`)),
		"两处列表完全相同":     state(a1(`[`+f1+`]`, `,"freezes":[`+f1+`]`)),
		"两处分别保存不同冻结":   state(a1(`[`+f1+`]`, `,"freezes":[`+f2+`]`)),
		"第二处为null":     state(a1(`[`+f1+`]`, `,"freezes":null`)),
		"第一处为null":     state(a1(`null`, `,"freezes":[`+f1+`]`)),
		"两处都为null":     state(a1(`null`, `,"freezes":null`)),
		"两处都是空列表":      state(a1(`[]`, `,"freezes":[]`)),
		"冻结列表出现三次":     state(a1(`[`+f1+`]`, `,"freezes":[],"freezes":[`+f2+`]`)),
		"大小写写法混用重复":    state(a1(`[`+f1+`]`, `,"Freezes":[]`)),
		"大小写写法混用顺序相反":  state(a1(`[]`, `,"Freezes":[`+f1+`]`)),
		"全大写写法混用重复":    state(a1(`[`+f1+`]`, `,"FREEZES":[]`)),
		"已销毁档案重复也拒绝":   destroyedState(`[`+f1Released+`]`, `,"freezes":[]`),
		"已销毁档案保存顺序相反":  destroyedState(`[]`, `,"freezes":[`+f1Released+`]`),
	}
	// escFreezesKey 是 JSON 中用 Unicode 转义写出的键名 "\u0066reezes"
	// （Go 字符串里的 \\u0066 是字面的反斜杠+u0066，JSON 解码后为 freezes）。
	escFreezesKey := "\"\\u0066reezes\""
	corrupt["Unicode转义字段名与直接字段名重复"] = state(a1(`[`+f1+`]`, `,`+escFreezesKey+`:`+`[]`))
	corrupt["Unicode转义字段名顺序相反"] = state(a1(`[]`, `,`+escFreezesKey+`:`+`[`+f1+`]`))

	for name, content := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, content)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("单份档案记录中冻结列表出现两次时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "freezes") || !strings.Contains(msg, "冻结列表") {
				t.Fatalf("错误信息应说明重复的是冻结列表 freezes，得到 %v", err)
			}
			// 必须指出档案编号。
			if !strings.Contains(msg, "A-1") {
				t.Fatalf("错误信息应指出档案编号 A-1，得到 %v", err)
			}
			// 不得误报某个冻结编号重复：错误信息不涉及任何具体冻结编号。
			for _, id := range []string{"F-1", "F-2"} {
				if strings.Contains(msg, id) {
					t.Fatalf("错误信息不应误报冻结编号 %s 重复，得到 %v", id, err)
				}
			}
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：冻结列表缺省、仅一次为 null、仅有一个空列表时继续表示没有冻结；
	// 现有能识别为冻结列表的大小写写法与转义写法单独出现时继续可读；不同档案
	// 各自带有 freezes 是正常保存格式，不被合在一起计数；冻结记录内部出现同名
	// 字段也是正常格式。合法列表里的全部冻结和解除历史按原顺序保留。
	t.Run("合法记录对照组", func(t *testing.T) {
		valid := map[string]string{
			"一个非空freezes":      state(`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[` + f1 + `]}`),
			"仅一个空列表":           state(`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[]}`),
			"仅一次为null":         state(`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":null}`),
			"冻结列表缺省":           state(`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false}`),
			"大小写写法Freezes单独出现": state(`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"Freezes":[` + f1 + `]}`),
			"全大写写法单独出现":        state(`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"FREEZES":[]}`),
			"不同档案各自带freezes不合并计数": `{"version":1,"archives":{` +
				`"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[` + f1 + `]},` +
				`"A-2":{"id":"A-2","category":"凭证","start":"2020-01-01","end":"2025-06-30","initial_end":"2025-06-30","destroyed":false,"freezes":[` + f2 + `]}` +
				`},"manifests":{}}`,
			"冻结记录内部同名字段不影响": state(`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false,"freezes":[]}]}`),
		}
		// 转义写法需要字面反斜杠，单独构造。
		valid["Unicode转义字段名单独出现"] = state(`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,` + escFreezesKey + `:[` + f1 + `]}`)
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

		// 合法列表中的全部冻结与解除历史按原顺序保留：F-1 已解除在前，
		// F-2 未解除在后，历史、解除信息与未解除集合都不因解码方式改变。
		dir := t.TempDir()
		legal := state(`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[` +
			f1Released + `,` + f2 + `]}`)
		writeStateFile(t, dir, legal)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("合法冻结历史应能打开: %v", err)
		}
		defer s.Close()
		h, found, err := s.History("A-1")
		if err != nil || !found {
			t.Fatalf("合法冻结历史应能查询: found=%v err=%v", found, err)
		}
		if len(h.Freezes) != 2 || h.Freezes[0].ID != "F-1" || h.Freezes[1].ID != "F-2" {
			t.Fatalf("全部冻结应按原顺序保留: %+v", h.Freezes)
		}
		if !h.Freezes[0].Released || h.Freezes[0].ReleaseReason != "结案" ||
			!h.Freezes[0].ReleasedOn.Equal(MustParseDate("2025-01-07")) {
			t.Fatalf("已解除冻结的解除历史应完整保留: %+v", h.Freezes[0])
		}
		if len(h.ActiveFreezes) != 1 || h.ActiveFreezes[0].ID != "F-2" {
			t.Fatalf("未解除集合应只含 F-2: %+v", h.ActiveFreezes)
		}
	})
}

// 保管库打开后保存内容才出现重复的冻结列表：下一次使用有效输入查询历史、
// 取回清册、销毁前核对与各项办理都必须按整库记录损坏失败——即使操作的是
// 另一份没有问题的正常档案——不返回正常历史、清册或部分核对结果，不改变
// 档案状态、生成清册或重新保存来消除重复，原保存内容保持原样。
func TestDuplicateFreezesFieldAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")
	reg(t, s, "A-3", "单据", "2020-01-01", "2025-01-10")
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05"),
	}); err != nil {
		t.Fatal(err)
	}
	// A-3 先合法销毁，供损坏后验证“取回清册”同样整库失败。
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-3", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-3"},
	}); err != nil {
		t.Fatal(err)
	}

	// 在 A-1 的记录里再插入一个空的冻结列表：第一处保存着未解除冻结 F-1，
	// 第二处为空列表——普通解码会只剩空列表，核对将误报可以办理。
	// 保存格式中 A-1 是档案集合里第一条记录，其 freezes 是第一个数组形式的
	// 冻结列表（A-3 的冻结列表在后面，count=1 只改 A-1 这一处）。
	good := readStateFile(t, dir)
	if !strings.Contains(good, `"freezes": [`) {
		t.Fatal("测试夹具失效：未找到数组形式的冻结列表")
	}
	corrupt := strings.Replace(good, `"freezes": [`, `"freezes": [],`+"\n      "+`"freezes": [`, 1)
	if corrupt == good {
		t.Fatal("未找到冻结列表的位置，测试夹具失效")
	}
	writeStateFile(t, dir, corrupt)
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误指出档案 A-1 并说明重复的是冻结列表 freezes，
	// 不能误报冻结编号 F-1 重复。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("单份档案记录出现两个冻结列表时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "freezes") ||
		!strings.Contains(err.Error(), "冻结列表") ||
		!strings.Contains(err.Error(), "A-1") ||
		strings.Contains(err.Error(), "F-1") {
		t.Fatalf("打开错误应为 ErrCorruptState，指出 A-1 的冻结列表 freezes 重复且不误报冻结编号，得到 %v", err)
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
	// 关键回归点：损坏状态下绝不能把“后一个空冻结列表”当作依据，
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
		t.Fatalf("损坏后不得凭后一个空冻结列表销毁 A-1: %+v err=%v", m, err)
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
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-08"),
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

	// 失败不得触发修复：不删除重复字段、不挑选其中一份、不重新保存，
	// 原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 去掉重复的冻结列表后，合法记录恢复可用：A-1 的冻结历史仍是损坏前那一条
	// 未解除冻结 F-1，不会被“后一个空列表”顶替；无关档案业务不受影响。
	writeStateFile(t, dir, good)
	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("恢复后查询应成功: found=%v err=%v", found, err)
	}
	if len(h.Freezes) != 1 || h.Freezes[0].ID != "F-1" || h.Freezes[0].Released {
		t.Fatalf("恢复后 A-1 应仍保留唯一一条未解除冻结 F-1: %+v", h.Freezes)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-8", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil || r.Status != CheckBlocked {
		t.Fatalf("恢复后未解除冻结仍应阻止销毁 A-1: %+v err=%v", r, err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); err != nil {
		t.Fatalf("恢复后无关档案应能正常办理销毁: %v", err)
	}
}
