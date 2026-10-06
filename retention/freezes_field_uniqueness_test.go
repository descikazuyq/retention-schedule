package retention

import (
	"errors"
	"strings"
	"testing"
)

// 同一份档案自己的保存内容中，冻结列表 freezes 最多只能出现一次：普通解码对
// 同名字段只保留最后一个值，同一份档案记录中若先保存了包含未解除诉讼冻结的
// freezes、后面又保存一个空的 freezes，读取后历史里原冻结消失，到期后的销毁
// 前核对可能误报可以办理。Open 必须按保存记录损坏失败（ErrCorruptState），
// 错误指出档案编号，并说明重复的是冻结列表 freezes 本身，不能误报为冻结编号
// 重复；两处列表完全相同、分别保存不同冻结、其中一处为空列表或 null 都按同一
// 规则拒绝，不合并、不挑选其中一份，调整保存顺序也不能改变拒绝结果，原文件
// 保持原样。
func TestOpenRejectsDuplicateFreezesField(t *testing.T) {
	activeFreeze := `{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false}`
	releasedFreeze := `{"id":"F-2","reason":"保全","frozen_on":"2025-01-06","released":true,"release_reason":"结案","released_on":"2025-01-08"}`
	// rec 构造一份登记记录，freezesFields 是原样拼接进记录的冻结列表字段
	// （可以写零个、一个或多个，字段名写法与列表内容任意）。
	rec := func(id string, freezesFields ...string) string {
		fields := `"id":"` + id + `","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false`
		for _, f := range freezesFields {
			fields += "," + f
		}
		return `{` + fields + `}`
	}
	state := func(archiveRec string) string {
		return `{"version":1,"archives":{"A-1":` + archiveRec + `},"manifests":{}}`
	}

	corrupt := map[string]string{
		"先保存未解除冻结后保存空列表": state(rec("A-1", `"freezes":[`+activeFreeze+`]`, `"freezes":[]`)),
		"保存顺序相反同样拒绝":     state(rec("A-1", `"freezes":[]`, `"freezes":[`+activeFreeze+`]`)),
		"两处列表完全相同":       state(rec("A-1", `"freezes":[`+activeFreeze+`]`, `"freezes":[`+activeFreeze+`]`)),
		"两处分别保存不同冻结":     state(rec("A-1", `"freezes":[`+activeFreeze+`]`, `"freezes":[`+releasedFreeze+`]`)),
		"第二处为空列表":        state(rec("A-1", `"freezes":[`+activeFreeze+`]`, `"freezes":[]`)),
		"第一处为空列表":        state(rec("A-1", `"freezes":[]`, `"freezes":[`+activeFreeze+`]`)),
		"第二处为null":       state(rec("A-1", `"freezes":[`+activeFreeze+`]`, `"freezes":null`)),
		"第一处为null":       state(rec("A-1", `"freezes":null`, `"freezes":[`+activeFreeze+`]`)),
		"两处都为null":       state(rec("A-1", `"freezes":null`, `"freezes":null`)),
		// 反引号字符串里的 \u0066 是字面的反斜杠+u0066，JSON 解码后才是字符 f，
		// 与直接写出的 freezes 是同一个字段名。
		"大小写写法混用重复":   state(rec("A-1", `"Freezes":[`+activeFreeze+`]`, `"freezes":[]`)),
		"大小写写法混用顺序相反": state(rec("A-1", `"freezes":[`+activeFreeze+`]`, `"Freezes":[]`)),
		"冻结列表出现三次":    state(rec("A-1", `"freezes":[`+activeFreeze+`]`, `"freezes":[]`, `"freezes":null`)),
	}
	// 转义写法需要字面反斜杠，单独构造。
	corrupt["Unicode转义字段名与直接字段名重复"] = state(rec("A-1", `"freezes":[`+activeFreeze+`]`, `"\u0066reezes":[]`))
	corrupt["Unicode转义字段名与直接字段名重复顺序相反"] = state(rec("A-1", `"\u0066reezes":[]`, `"freezes":[`+activeFreeze+`]`))

	for name, content := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, content)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("单份档案的保存内容中出现两次冻结列表时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "A-1") {
				t.Fatalf("错误信息应指出档案编号 A-1，得到 %v", err)
			}
			if !strings.Contains(msg, "freezes") || !strings.Contains(msg, "冻结列表") {
				t.Fatalf("错误信息应说明重复的是冻结列表 freezes，得到 %v", err)
			}
			// 不得误报为冻结编号重复：重复的是列表字段本身，与冻结编号无关。
			if strings.Contains(msg, "冻结编号") {
				t.Fatalf("错误信息不应误报为冻结编号重复，得到 %v", err)
			}
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：冻结列表缺省、仅一次为 null 或仅有一个空列表时继续表示没有冻结；
	// 现有能识别为冻结列表的大小写写法与转义写法单独出现时继续可读；不同档案各自
	// 的冻结列表不合在一起计数；合法列表里的全部冻结和解除历史按原顺序保留。
	t.Run("合法记录对照组", func(t *testing.T) {
		valid := map[string]string{
			"冻结列表缺省":           state(rec("A-1")),
			"freezes仅一次为null":  state(rec("A-1", `"freezes":null`)),
			"仅有一个空列表":          state(rec("A-1", `"freezes":[]`)),
			"大小写写法Freezes单独出现": state(rec("A-1", `"Freezes":[`+activeFreeze+`]`)),
			"大小写写法FREEZES单独出现": state(rec("A-1", `"FREEZES":[`+activeFreeze+`]`)),
			// 不同档案各自带自己的冻结列表是正常保存格式，不合在一起计数。
			"不同档案各自一个冻结列表": `{"version":1,"archives":{"A-1":` + rec("A-1", `"freezes":[`+activeFreeze+`]`) +
				`,"A-2":{"id":"A-2","category":"凭证","start":"2020-01-01","end":"2025-06-30","initial_end":"2025-06-30","destroyed":false,"freezes":[` + releasedFreeze + `]}},"manifests":{}}`,
			// 合法列表里的全部冻结和解除历史按原顺序保留。
			"合法列表保留全部冻结与解除历史": state(rec("A-1", `"freezes":[`+activeFreeze+`,`+releasedFreeze+`]`)),
		}
		// 转义写法需要字面反斜杠，单独构造。
		valid["Unicode转义字段名单独出现"] = state(rec("A-1", `"\u0066reezes":[`+activeFreeze+`]`))
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

	// 合法列表里的全部冻结和解除历史仍按原顺序保留，未解除冻结继续阻止销毁。
	t.Run("合法列表内容完整保留", func(t *testing.T) {
		dir := t.TempDir()
		writeStateFile(t, dir, state(rec("A-1", `"freezes":[`+activeFreeze+`,`+releasedFreeze+`]`)))
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("合法记录应能打开: %v", err)
		}
		defer s.Close()
		h, found, err := s.History("A-1")
		if err != nil || !found {
			t.Fatalf("查询应成功: found=%v err=%v", found, err)
		}
		if len(h.Freezes) != 2 || h.Freezes[0].ID != "F-1" || h.Freezes[1].ID != "F-2" {
			t.Fatalf("冻结历史应按原顺序保留 F-1、F-2: %+v", h.Freezes)
		}
		if h.Freezes[0].Released || !h.Freezes[1].Released || h.Freezes[1].ReleaseReason != "结案" {
			t.Fatalf("解除历史应完整保留: %+v", h.Freezes)
		}
		if len(h.ActiveFreezes) != 1 || h.ActiveFreezes[0].ID != "F-1" {
			t.Fatalf("未解除冻结应只剩 F-1: %+v", h.ActiveFreezes)
		}
		r, err := s.Check(CheckRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
		})
		if err != nil || r.Status != CheckBlocked {
			t.Fatalf("未解除冻结仍应阻止销毁: %+v err=%v", r, err)
		}
	})

	// 已销毁档案同样适用：已销毁档案的保存内容中出现两次冻结列表也按损坏拒绝；
	// 只出现一次的合法已销毁档案（冻结均已解除）继续可读。
	t.Run("已销毁档案同样适用", func(t *testing.T) {
		manifest := `{"application_id":"APP-1","processed_on":"2025-01-10","entries":[{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}]}`
		destroyedRec := func(freezesFields ...string) string {
			fields := `"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1"`
			for _, f := range freezesFields {
				fields += "," + f
			}
			return `{` + fields + `}`
		}
		stateWithManifest := func(archiveRec string) string {
			return `{"version":1,"archives":{"A-1":` + archiveRec + `},"manifests":{"APP-1":` + manifest + `}}`
		}

		dir := t.TempDir()
		content := stateWithManifest(destroyedRec(`"freezes":[`+releasedFreeze+`]`, `"freezes":[]`))
		writeStateFile(t, dir, content)
		s, err := Open(dir)
		if err == nil {
			s.Close()
			t.Fatal("已销毁档案的保存内容中出现两次冻结列表时不应打开成功")
		}
		if !errors.Is(err, ErrCorruptState) ||
			!strings.Contains(err.Error(), "A-1") ||
			!strings.Contains(err.Error(), "freezes") {
			t.Fatalf("打开错误应为 ErrCorruptState 并指出档案编号与冻结列表 freezes，得到 %v", err)
		}
		if got := readStateFile(t, dir); got != content {
			t.Fatalf("打开失败后原文件被改动:\n%q", got)
		}

		// 对照：已销毁档案只保存一个冻结列表（冻结已合法解除）时继续可读。
		dir2 := t.TempDir()
		writeStateFile(t, dir2, stateWithManifest(destroyedRec(`"freezes":[`+releasedFreeze+`]`)))
		s2, err := Open(dir2)
		if err != nil {
			t.Fatalf("合法的已销毁档案应能打开: %v", err)
		}
		defer s2.Close()
		h, found, err := s2.History("A-1")
		if err != nil || !found || !h.Destroyed || len(h.Freezes) != 1 {
			t.Fatalf("已销毁档案的历史与冻结记录应完整可查: %+v found=%v err=%v", h, found, err)
		}
	})
}

// 保管库打开后保存内容才出现重复的冻结列表：下一次使用有效输入查询历史、取回清册、
// 销毁前核对与各项办理都必须按整库记录损坏失败——即使操作的是另一份没有问题的
// 档案——不返回正常历史、清册或部分核对结果，不改变档案状态或生成清册，原保存
// 内容保持原样。
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

	// 在 A-1 的登记记录里再写入一个冻结列表：先一个空列表、再是原来保存着
	// 未解除冻结 F-1 的列表——普通解码会只剩后一个，但两处保存本身已使记录
	// 不可信。档案编号按排序保存，A-1 的记录是文件中第一个 freezes 字段。
	good := readStateFile(t, dir)
	corrupt := strings.Replace(good, `"freezes"`, `"freezes": [],
      "freezes"`, 1)
	if corrupt == good {
		t.Fatal("未找到 A-1 记录的冻结列表位置，测试夹具失效")
	}
	writeStateFile(t, dir, corrupt)
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误指出档案编号并说明重复的是冻结列表 freezes。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("单份档案的保存内容中出现两个冻结列表时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "A-1") ||
		!strings.Contains(err.Error(), "freezes") ||
		!strings.Contains(err.Error(), "冻结列表") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出档案 A-1 的冻结列表 freezes 重复，得到 %v", err)
	}
	if strings.Contains(err.Error(), "冻结编号") {
		t.Fatalf("错误信息不应误报为冻结编号重复，得到 %v", err)
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
	// 关键回归点：损坏状态下绝不能把重复保存的冻结列表当成没有冻结的依据，
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
		t.Fatalf("损坏后不得凭重复保存的冻结列表销毁 A-1: %+v err=%v", m, err)
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

	// 失败不得触发修复：不合并两处列表、不挑选其中一份、不重新保存来消除重复，
	// 原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 去掉重复的冻结列表后，合法记录恢复可用：A-1 的冻结历史仍是损坏前那一条
	// 未解除冻结，不会被重复保存的空列表顶替；无关档案业务不受影响。
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
