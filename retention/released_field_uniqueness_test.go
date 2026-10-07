package retention

import (
	"errors"
	"strings"
	"testing"
)

// 同一条冻结记录自己的保存内容中，解除标记 released 最多只能出现一次：普通解码
// 对同名字段只保留最后一个值，一条冻结若先写 released:false、后写 released:true
// 并带上合法的解除原因和解除日期，读取会采用后一个标记——档案到期后，销毁前
// 核对可能显示可以办理，正式销毁也会忽略这条冻结。Open 必须按保存记录损坏失败
// （ErrCorruptState），错误指出档案编号、冻结编号，并说明重复的是解除标记
// released 本身，不能误报为冻结编号或冻结列表重复；两处取值相反、完全相同或
// 其中一处为 null 都按同一规则拒绝，不选取其中一个值继续使用，交换字段顺序也
// 不改变拒绝结果，原文件保持原样。
func TestOpenRejectsDuplicateReleasedField(t *testing.T) {
	// freeze 构造一条冻结记录，releasedFields 是原样拼接进记录的解除标记字段
	// （可以写一个或多个，字段名写法与取值任意）；releaseInfo 在需要时补上
	// 合法的解除原因与解除日期。
	freeze := func(releaseInfo bool, releasedFields ...string) string {
		fields := `"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05"`
		for _, f := range releasedFields {
			fields += "," + f
		}
		if releaseInfo {
			fields += `,"release_reason":"结案","released_on":"2025-01-08"`
		}
		return `{` + fields + `}`
	}
	state := func(freezeRec string) string {
		return `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[` + freezeRec + `]}},"manifests":{}}`
	}

	corrupt := map[string]string{
		// 任务场景：先写未解除、后写已解除并带合法解除原因和日期，
		// 普通解码会采用后一个标记，把仍有效的冻结当成已解除。
		"先false后true带合法解除信息": state(freeze(true, `"released":false`, `"released":true`)),
		"先true后false带合法解除信息": state(freeze(true, `"released":true`, `"released":false`)),
		"两处完全相同都是false":      state(freeze(false, `"released":false`, `"released":false`)),
		"两处完全相同都是true":       state(freeze(true, `"released":true`, `"released":true`)),
		"第二处为null":           state(freeze(false, `"released":false`, `"released":null`)),
		"第一处为null":           state(freeze(false, `"released":null`, `"released":false`)),
		"两处都为null":           state(freeze(false, `"released":null`, `"released":null`)),
		"大小写写法混用重复":          state(freeze(true, `"Released":false`, `"released":true`)),
		"大小写写法混用顺序相反":        state(freeze(true, `"released":false`, `"Released":true`)),
		"解除标记出现三次":           state(freeze(true, `"released":false`, `"released":true`, `"released":false`)),
	}
	// 转义写法需要字面反斜杠，单独构造：反引号字符串里的 \u0072 是字面的
	// 反斜杠+u0072，JSON 解码后才是字符 r，与直接写出的 released 是同一个字段名。
	corrupt["Unicode转义字段名与直接字段名重复"] = state(freeze(true, `"released":false`, `"\u0072eleased":true`))
	corrupt["Unicode转义字段名与直接字段名重复顺序相反"] = state(freeze(true, `"\u0072eleased":false`, `"released":true`))

	for name, content := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, content)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("同一条冻结记录的保存内容中出现两次解除标记时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "A-1") || !strings.Contains(msg, "F-1") {
				t.Fatalf("错误信息应指出档案编号 A-1 与冻结编号 F-1，得到 %v", err)
			}
			if !strings.Contains(msg, "released") || !strings.Contains(msg, "解除标记") {
				t.Fatalf("错误信息应说明重复的是解除标记 released，得到 %v", err)
			}
			// 不得误报为冻结编号重复或冻结列表重复：重复的是解除标记本身。
			if strings.Contains(msg, "冻结编号") || strings.Contains(msg, "冻结列表") {
				t.Fatalf("错误信息不应误报为冻结编号或冻结列表重复，得到 %v", err)
			}
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：解除标记缺省仍表示未解除；单次 false、单次 true 且解除信息合法、
	// 单次 null 都继续可读；现有能识别的大小写写法与转义写法单独出现时继续可读；
	// 不同冻结、不同档案各自的解除标记不合在一起计数。
	t.Run("合法记录对照组", func(t *testing.T) {
		valid := map[string]string{
			"解除标记缺省":            state(freeze(false)),
			"released单次false":   state(freeze(false, `"released":false`)),
			"released单次true":    state(freeze(true, `"released":true`)),
			"released单次null":    state(freeze(false, `"released":null`)),
			"大小写写法Released单独出现": state(freeze(true, `"Released":true`)),
			"大小写写法RELEASED单独出现": state(freeze(false, `"RELEASED":false`)),
			// 同一档案的两条冻结各自带自己的解除标记，不合在一起计数。
			"不同冻结各自一个解除标记": `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[` +
				`{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false},` +
				`{"id":"F-2","reason":"保全","frozen_on":"2025-01-06","released":true,"release_reason":"结案","released_on":"2025-01-08"}` +
				`]}},"manifests":{}}`,
			// 不同档案各自的解除标记也不合在一起计数。
			"不同档案各自一个解除标记": `{"version":1,"archives":{` +
				`"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[` +
				`{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false}]},` +
				`"A-2":{"id":"A-2","category":"凭证","start":"2020-01-01","end":"2025-06-30","initial_end":"2025-06-30","destroyed":false,"freezes":[` +
				`{"id":"F-2","reason":"保全","frozen_on":"2025-01-06","released":true,"release_reason":"结案","released_on":"2025-01-08"}]}` +
				`},"manifests":{}}`,
		}
		// 转义写法需要字面反斜杠，单独构造。
		valid["Unicode转义字段名单独出现"] = state(freeze(true, `"\u0072eleased":true`))
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

	// 正常冻结和解除功能继续沿用现有规则：解除标记缺省表示未解除，未解除冻结
	// 仍阻止销毁；合法解除保留原因和日期，不再构成阻碍。
	t.Run("正常冻结与解除行为不变", func(t *testing.T) {
		dir := t.TempDir()
		writeStateFile(t, dir, `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[`+
			`{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false},`+
			`{"id":"F-2","reason":"保全","frozen_on":"2025-01-06","released":true,"release_reason":"结案","released_on":"2025-01-08"}`+
			`]}},"manifests":{}}`)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("合法记录应能打开: %v", err)
		}
		defer s.Close()
		h, found, err := s.History("A-1")
		if err != nil || !found {
			t.Fatalf("查询应成功: found=%v err=%v", found, err)
		}
		if len(h.Freezes) != 2 || h.Freezes[0].Released || !h.Freezes[1].Released ||
			h.Freezes[1].ReleaseReason != "结案" || !h.Freezes[1].ReleasedOn.Equal(MustParseDate("2025-01-08")) {
			t.Fatalf("冻结与解除历史应完整保留: %+v", h.Freezes)
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

	// 已销毁档案的冻结历史同样受这项限制：重复解除标记按损坏拒绝；
	// 合法历史与已关闭清册仍完整可查。
	t.Run("已销毁档案同样适用", func(t *testing.T) {
		manifest := `{"application_id":"APP-1","processed_on":"2025-01-10","entries":[{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}]}`
		stateWithManifest := func(freezeRec string) string {
			return `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1","freezes":[` + freezeRec + `]}},"manifests":{"APP-1":` + manifest + `}}`
		}

		dir := t.TempDir()
		content := stateWithManifest(`{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false,"released":true,"release_reason":"结案","released_on":"2025-01-08"}`)
		writeStateFile(t, dir, content)
		s, err := Open(dir)
		if err == nil {
			s.Close()
			t.Fatal("已销毁档案的冻结记录中出现两次解除标记时不应打开成功")
		}
		if !errors.Is(err, ErrCorruptState) ||
			!strings.Contains(err.Error(), "A-1") ||
			!strings.Contains(err.Error(), "F-1") ||
			!strings.Contains(err.Error(), "released") {
			t.Fatalf("打开错误应为 ErrCorruptState 并指出档案编号、冻结编号与解除标记 released，得到 %v", err)
		}
		if got := readStateFile(t, dir); got != content {
			t.Fatalf("打开失败后原文件被改动:\n%q", got)
		}

		// 对照：已销毁档案的冻结只保存一个解除标记（合法解除）时继续可读，
		// 合法历史与已关闭清册完整可查。
		dir2 := t.TempDir()
		writeStateFile(t, dir2, stateWithManifest(`{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-08"}`))
		s2, err := Open(dir2)
		if err != nil {
			t.Fatalf("合法的已销毁档案应能打开: %v", err)
		}
		defer s2.Close()
		h, found, err := s2.History("A-1")
		if err != nil || !found || !h.Destroyed || len(h.Freezes) != 1 || !h.Freezes[0].Released {
			t.Fatalf("已销毁档案的历史与冻结记录应完整可查: %+v found=%v err=%v", h, found, err)
		}
		if m, found, err := s2.GetManifest("APP-1"); err != nil || !found || m.ApplicationID != "APP-1" {
			t.Fatalf("已关闭清册应完整可查: %+v found=%v err=%v", m, found, err)
		}
	})
}

// 保管库打开后保存内容才出现重复的解除标记：下一次使用有效输入查询历史、取回清册、
// 销毁前核对与各项办理都必须按整库记录损坏失败——即使操作的是另一份没有问题的
// 档案——不返回正常历史、清册或部分核对报告，不改变档案状态或生成清册，也不重新
// 保存来消除重复，原保存内容保持原样。
func TestDuplicateReleasedFieldAfterOpenFailsAllOperations(t *testing.T) {
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

	// 在 A-1 的冻结 F-1 里再写入一个解除标记：先保留原来的 released:false，
	// 后面再写 released:true 并带上合法的解除原因和解除日期——普通解码会采用
	// 后一个标记，把仍有效的冻结当成已解除。档案编号按排序保存，A-1 的冻结
	// 记录是文件中第一个 released 字段。
	good := readStateFile(t, dir)
	corrupt := strings.Replace(good, `"released": false`,
		`"released": false, "released": true, "release_reason": "结案", "released_on": "2025-01-08"`, 1)
	if corrupt == good {
		t.Fatal("未找到 A-1 冻结记录的解除标记位置，测试夹具失效")
	}
	writeStateFile(t, dir, corrupt)
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误指出档案编号、冻结编号，并说明重复的是解除标记 released。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("同一条冻结记录的保存内容中出现两个解除标记时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "A-1") ||
		!strings.Contains(err.Error(), "F-1") ||
		!strings.Contains(err.Error(), "released") ||
		!strings.Contains(err.Error(), "解除标记") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出档案 A-1 的冻结 F-1 的解除标记 released 重复，得到 %v", err)
	}
	if strings.Contains(err.Error(), "冻结编号") || strings.Contains(err.Error(), "冻结列表") {
		t.Fatalf("错误信息不应误报为冻结编号或冻结列表重复，得到 %v", err)
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
	// 关键回归点：损坏状态下绝不能把重复保存的解除标记当成冻结已解除的依据，
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
		t.Fatalf("损坏后不得凭重复保存的解除标记销毁 A-1: %+v err=%v", m, err)
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

	// 失败不得触发修复：不选取其中一个标记、不重新保存来消除重复，
	// 原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 去掉重复的解除标记后，合法记录恢复可用：A-1 的冻结仍是损坏前那条未解除
	// 冻结，不会被重复保存的 released:true 顶替；无关档案业务不受影响。
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
