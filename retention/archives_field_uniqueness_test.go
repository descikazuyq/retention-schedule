package retention

import (
	"errors"
	"strings"
	"testing"
)

// 同一份保存记录的最外层最多只能出现一次档案集合：普通解码对同名字段只保留
// 最后一个值，最外层写了两次 archives 时，前一次的档案集合会被后一次整批
// 替换（第一处保存着未解除诉讼冻结的 A-1，第二处保存成没有冻结的同号档案，
// 读取后只剩后者，销毁资格会被误判为可以办理）。Open 必须按保存记录损坏失败
// （ErrCorruptState），错误说明重复的是档案集合 archives 本身，而不是误报
// 某个档案编号重复；两处集合内容完全一致、只含不同编号、其中一处为空对象或
// null 都按同一规则拒绝，不合并、不择取最后一份、不按哪份保留更多冻结挑选，
// 调整两处的保存顺序也不能改变拒绝结果，原文件保持原样。
func TestOpenRejectsDuplicateArchivesField(t *testing.T) {
	activeFreeze := `{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false}`
	// rec 构造一份登记记录。
	rec := func(id, category, end, freezes string) string {
		return `{"id":"` + id + `","category":"` + category + `","start":"2020-01-01","end":"` + end +
			`","initial_end":"` + end + `","destroyed":false,"freezes":[` + freezes + `]}`
	}
	a1Frozen := rec("A-1", "合同", "2025-01-10", activeFreeze)
	a1NoFreeze := rec("A-1", "合同", "2025-01-10", "")
	a2 := rec("A-2", "凭证", "2025-06-30", "")
	// state 用两个字段名写法与两处集合内容拼出最外层含两个档案集合的保存记录；
	// 集合内容可以是任意 JSON 值（对象、空对象、null）。
	state := func(key1, archives1, key2, archives2 string) string {
		return `{"version":1,` + key1 + `:` + archives1 + `,` + key2 + `:` + archives2 + `,"manifests":{}}`
	}
	obj := func(entries string) string { return `{` + entries + `}` }

	corrupt := map[string]string{
		"第一处有未解除冻结第二处同号无冻结": state(`"archives"`, obj(`"A-1":`+a1Frozen), `"archives"`, obj(`"A-1":`+a1NoFreeze)),
		"保存顺序相反同样拒绝":        state(`"archives"`, obj(`"A-1":`+a1NoFreeze), `"archives"`, obj(`"A-1":`+a1Frozen)),
		"两处集合内容完全一致":        state(`"archives"`, obj(`"A-1":`+a1Frozen), `"archives"`, obj(`"A-1":`+a1Frozen)),
		"两处只含不同编号":          state(`"archives"`, obj(`"A-1":`+a1Frozen), `"archives"`, obj(`"A-2":`+a2)),
		"第二处为空对象":           state(`"archives"`, obj(`"A-1":`+a1Frozen), `"archives"`, `{}`),
		"第一处为空对象":           state(`"archives"`, `{}`, `"archives"`, obj(`"A-1":`+a1Frozen)),
		"第二处为null":          state(`"archives"`, obj(`"A-1":`+a1Frozen), `"archives"`, `null`),
		"第一处为null":          state(`"archives"`, `null`, `"archives"`, obj(`"A-1":`+a1Frozen)),
		"两处都为null":          state(`"archives"`, `null`, `"archives"`, `null`),
		// 反引号字符串里的 \u0061 是字面的反斜杠+u0061，JSON 解码后才是字符 a，
		// 与直接写出的 archives 是同一个字段名。
		"Unicode转义字段名与直接字段名重复": state(`"archives"`, obj(`"A-1":`+a1Frozen), `"\u0061rchives"`, obj(`"A-1":`+a1NoFreeze)),
		"大小写写法混用重复":            state(`"archives"`, obj(`"A-1":`+a1Frozen), `"Archives"`, obj(`"A-1":`+a1NoFreeze)),
		"大小写写法混用顺序相反":          state(`"Archives"`, obj(`"A-1":`+a1Frozen), `"archives"`, obj(`"A-1":`+a1NoFreeze)),
		"档案集合出现三次": `{"version":1,"archives":{"A-1":` + a1Frozen + `},"archives":{"A-2":` + a2 +
			`},"archives":{},"manifests":{}}`,
	}
	// 转义写法需要字面反斜杠，单独构造。
	corrupt["Unicode转义字段名表示同一字段"] = `{"version":1,"archives":{"A-1":` + a1Frozen +
		`},"\u0061rchives":{"A-1":` + a1NoFreeze + `},"manifests":{}}`

	for name, content := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, content)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("最外层出现两次档案集合时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "archives") || !strings.Contains(msg, "档案集合") {
				t.Fatalf("错误信息应说明重复的是档案集合 archives，得到 %v", err)
			}
			// 不得误报某个档案编号重复：错误信息不涉及任何具体档案编号。
			for _, id := range []string{"A-1", "A-2"} {
				if strings.Contains(msg, id) {
					t.Fatalf("错误信息不应误报档案编号 %s 重复，得到 %v", id, err)
				}
			}
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：档案集合只出现一次时继续可读——包括现有能识别为档案集合的大小写
	// 写法与转义写法单独出现；档案集合缺省或仅一次为 null 的空库兼容行为不变；
	// 各份档案登记内容里出现同名字段（id、日期、冻结，甚至名为 archives 的
	// 未知内部字段）是正常格式，不属于最外层的档案集合字段，不会被误判。
	t.Run("合法记录对照组", func(t *testing.T) {
		valid := map[string]string{
			"archives只出现一次":     `{"version":1,"archives":{"A-1":` + a1Frozen + `,"A-2":` + a2 + `},"manifests":{}}`,
			"大小写写法Archives单独出现": `{"version":1,"Archives":{"A-1":` + a1Frozen + `},"manifests":{}}`,
			"大小写写法ARCHIVES单独出现": `{"version":1,"ARCHIVES":{"A-1":` + a1Frozen + `},"manifests":{}}`,
			"档案集合缺省":            `{"version":1,"manifests":{}}`,
			"archives仅一次为null":  `{"version":1,"archives":null,"manifests":{}}`,
			"登记内容内部同名字段": `{"version":1,"archives":{"A-1":` + a1Frozen + `,"A-2":` + a2 +
				`},"manifests":{}}`,
			"登记内容里名为archives的内部字段": `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","destroyed":false,"freezes":[],"archives":{"A-9":{}}}},"manifests":{}}`,
		}
		// 转义写法需要字面反斜杠，单独构造。
		valid["Unicode转义字段名单独出现"] = `{"version":1,"\u0061rchives":{"A-1":` + a1Frozen + `},"manifests":{}}`
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
}

// 保管库打开后保存内容才出现重复的档案集合：下一次使用有效输入查询历史、取回
// 清册、销毁前核对与各项办理都必须按整库记录损坏失败——即使操作的是另一份
// 没有问题的档案——不返回部分历史或可以办理的结论，不改变档案状态、生成清册
// 或重新保存来消除重复，原保存内容保持原样。
func TestDuplicateArchivesFieldAfterOpenFailsAllOperations(t *testing.T) {
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

	// 在最外层再写入一个档案集合：其中 A-1 期限相同但冻结列表为空——正是
	// “第一处保存着未解除冻结的 A-1，第二处保存成没有冻结的同号档案”的情形。
	// 普通解码会只剩后一个集合，核对将误报可以办理。
	good := readStateFile(t, dir)
	second := `"archives": {
    "A-1": {"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[]}
  },
  "manifests"`
	corrupt := strings.Replace(good, `"manifests"`, second, 1)
	if corrupt == good {
		t.Fatal("未找到清册集合的位置，测试夹具失效")
	}
	writeStateFile(t, dir, corrupt)
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误说明重复的是档案集合 archives。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("最外层出现两个档案集合时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "archives") ||
		!strings.Contains(err.Error(), "档案集合") {
		t.Fatalf("打开错误应为 ErrCorruptState 并说明档案集合 archives 重复，得到 %v", err)
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
	// 关键回归点：损坏状态下绝不能把“冻结列表为空的后一个集合”当作依据，
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
		t.Fatalf("损坏后不得凭后一个无冻结集合销毁 A-1: %+v err=%v", m, err)
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

	// 失败不得触发修复：不删除重复集合、不挑选其中一份、不重新保存，
	// 原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 去掉重复的档案集合后，合法记录恢复可用：A-1 的冻结历史仍是损坏前那一条
	// 未解除冻结，不会被“无冻结的后一个集合”顶替；无关档案业务不受影响。
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
