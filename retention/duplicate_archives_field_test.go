package retention

import (
	"errors"
	"strings"
	"testing"
)

// 同一份保存记录的最外层最多只能出现一次档案集合 archives：出现两次或更多
// 次时 Open 必须按保存记录损坏失败（ErrCorruptState），错误说明重复的是
// 档案集合 archives 本身，而不是误报某个档案编号重复。整体解码会让后一次
// archives 整体替换前一次（例如第一处保存着未解除诉讼冻结的 A-1，第二处
// 保存成没有冻结的同号档案，读取后只剩后者，销毁资格会被误判为可以办理），
// 绝不能接受这种覆盖。两处内容完全一致、只含不同编号、其中一处为空对象或
// null 都按同一规则拒绝，不能合并、择取最后一份，也不能按哪份保留了更多
// 冻结来选记录；调整两处的保存顺序不能改变拒绝结果。
func TestOpenRejectsDuplicateArchivesField(t *testing.T) {
	activeFreeze := `{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false}`
	// rec 构造一份登记记录。
	rec := func(id, category, end, freezes string) string {
		return `{"id":"` + id + `","category":"` + category + `","start":"2020-01-01","end":"` + end +
			`","initial_end":"` + end + `","destroyed":false,"freezes":[` + freezes + `]}`
	}
	a1Frozen := rec("A-1", "合同", "2025-01-10", activeFreeze)
	a1NoFreeze := rec("A-1", "合同", "2025-01-10", "")
	b2 := rec("B-2", "凭证", "2025-06-30", "")
	// state 按给定的最外层字段拼接完整保存记录。
	state := func(fields ...string) string {
		return `{"version":1,` + strings.Join(fields, ",") + `,"manifests":{}}`
	}

	corrupt := map[string]struct {
		state string
		// 两处只含不同编号时，错误不得误报某个档案编号重复。
		forbidIDReport bool
	}{
		"第一处带未解除冻结第二处同号无冻结": {
			state(`"archives":{"A-1":`+a1Frozen+`}`, `"archives":{"A-1":`+a1NoFreeze+`}`),
			false,
		},
		"顺序相反同样拒绝不得按冻结挑选": {
			state(`"archives":{"A-1":`+a1NoFreeze+`}`, `"archives":{"A-1":`+a1Frozen+`}`),
			false,
		},
		"两处集合内容完全一致": {
			state(`"archives":{"A-1":`+a1Frozen+`}`, `"archives":{"A-1":`+a1Frozen+`}`),
			false,
		},
		"两处只含不同编号": {
			state(`"archives":{"A-1":`+a1NoFreeze+`}`, `"archives":{"B-2":`+b2+`}`),
			true,
		},
		"第二处为空对象": {
			state(`"archives":{"A-1":`+a1Frozen+`}`, `"archives":{}`),
			true,
		},
		"第二处为null": {
			state(`"archives":{"A-1":`+a1Frozen+`}`, `"archives":null`),
			true,
		},
		"第一处为null第二处有内容": {
			state(`"archives":null`, `"archives":{"A-1":`+a1Frozen+`}`),
			true,
		},
		"转义字段名与直接字段名重复": {
			// Go 源里的 \u0061 是字面的反斜杠+u0061，JSON 解码后才是字符 a，
			// 与直接写出的 archives 是同一个字段名。
			state(`"archives":{"A-1":`+a1Frozen+`}`, `"\u0061rchives":{"B-2":`+b2+`}`),
			true,
		},
		"大小写写法混用重复": {
			state(`"Archives":{"A-1":`+a1Frozen+`}`, `"archives":{"B-2":`+b2+`}`),
			true,
		},
		"档案集合出现三次": {
			state(`"archives":{"A-1":`+a1NoFreeze+`}`, `"archives":{"B-2":`+b2+`}`,
				`"archives":{}`),
			true,
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("最外层重复出现档案集合时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "archives") ||
				!strings.Contains(msg, "档案集合") ||
				!strings.Contains(msg, "重复") {
				t.Fatalf("错误信息应说明重复的是档案集合 archives，得到 %v", err)
			}
			if tc.forbidIDReport && strings.Contains(msg, "档案编号") {
				t.Fatalf("两处集合编号不同时不得误报档案编号重复，得到 %v", err)
			}
			if got := readStateFile(t, dir); got != tc.state {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：最外层档案集合至多出现一次时继续可读——能识别为档案集合的
	// 大小写写法单独出现、档案集合缺省、仅一次为 null，以及各份档案登记
	// 内容里各自带有 id、日期、冻结等同名字段，都是正常格式。
	t.Run("合法记录对照组", func(t *testing.T) {
		valid := map[string]string{
			"大小写写法Archives单独出现": state(`"Archives":{"A-1":` + a1Frozen + `}`),
			"大小写写法ARCHIVES单独出现": state(`"ARCHIVES":{"A-1":` + a1Frozen + `}`),
			"转义字段名单独出现":         state(`"\u0061rchives":{"A-1":` + a1Frozen + `}`),
			"档案集合缺省":              `{"version":1,"manifests":{}}`,
			"档案集合仅一次为null":        state(`"archives":null`),
			"登记内容内部同名字段正常": state(`"archives":{"A-1":` + a1Frozen +
				`,"B-2":` + b2 + `}`),
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
			})
		}
	})
}

// 保管库打开后保存内容才出现重复的档案集合：下一次使用有效输入查询历史、
// 销毁前核对与各项办理都必须按整库记录损坏失败——即使操作的是另一份正常
// 档案——不返回部分历史或可以办理的结论，不改变档案状态、生成清册或重新
// 保存来消除重复，原保存内容保持原样。
func TestDuplicateArchivesFieldAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05"),
	}); err != nil {
		t.Fatal(err)
	}
	// A-2 先合法销毁，供损坏后验证“取回清册”同样整库失败。
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); err != nil {
		t.Fatal(err)
	}

	// 在最外层再写入第二个档案集合：其中 A-1 没有冻结——正是“第一处保存着
	// 未解除冻结、第二处保存成无冻结同号档案”的情形。整体解码会只剩后一份
	// 档案集合，核对将误报可以办理。
	good := readStateFile(t, dir)
	trimmed := strings.TrimSpace(good)
	if !strings.HasSuffix(trimmed, "}") {
		t.Fatal("保存记录应以对象结束，测试夹具失效")
	}
	corrupt := trimmed[:len(trimmed)-1] +
		`,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10",` +
		`"initial_end":"2025-01-10","destroyed":false,"freezes":[]}}}`
	writeStateFile(t, dir, corrupt)
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误说明重复的是档案集合 archives。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("最外层重复出现档案集合时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "archives") ||
		!strings.Contains(err.Error(), "档案集合") {
		t.Fatalf("打开错误应为 ErrCorruptState 并说明档案集合 archives 重复，得到 %v", err)
	}

	// 已打开实例：即使本次只操作与重复集合无关的正常记录，也必须按整库损坏失败。
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History 必须失败且不得给出部分历史: found=%v err=%v", found, err)
	}
	if m, found, err := s.GetManifest("APP-2"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("损坏后 GetManifest 必须失败: found=%v err=%v", found, err)
	}
	// 关键回归点：损坏状态下绝不能把“无冻结的第二处 A-1”当作依据，
	// 在截止日当天给出可以办理甚至完成销毁。
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-8", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败，不能误报可以办理: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-8", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后不得凭第二处无冻结登记销毁 A-1: %+v err=%v", m, err)
	}
	if err := s.Register(RegisterInput{
		ID: "A-4", Category: "凭证", Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-06-30"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Register 必须失败: %v", err)
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-1", FreezeID: "F-X", Reason: "诉讼", FrozenOn: MustParseDate("2025-06-01"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Freeze 必须失败: %v", err)
	}
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-08"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Release 必须失败: %v", err)
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-9", ArchiveID: "A-1",
		OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
		RevisedOn: MustParseDate("2025-06-01"), Reason: "延期",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Revise 必须失败: %v", err)
	}

	// 失败不得触发修复：不合并两处集合、不重新保存消除重复，原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 去掉重复的档案集合后，合法记录恢复可用：A-1 仍是带未解除冻结的那份。
	writeStateFile(t, dir, good)
	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("恢复后查询应成功: found=%v err=%v", found, err)
	}
	if len(h.ActiveFreezes) != 1 || h.ActiveFreezes[0].ID != "F-1" {
		t.Fatalf("恢复后 A-1 应仍保留未解除冻结 F-1: %+v", h.ActiveFreezes)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-8", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil || r.Status != CheckBlocked {
		t.Fatalf("恢复后未解除冻结仍应阻止销毁 A-1: %+v err=%v", r, err)
	}
}
