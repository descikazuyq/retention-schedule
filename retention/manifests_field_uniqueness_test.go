package retention

import (
	"errors"
	"strings"
	"testing"
)

// 同一份保存记录的最外层最多只能出现一次清册集合：普通解码对同名字段只保留
// 最后一个值，最外层写了两次 manifests 时，前一处的清册集合会被后一处整批
// 替换（APP-1 的清册收录截止日为 2025-01-10 的已销毁档案，两处集合的条目、
// 归属都正确，处理日期分别为 2025-01-10 和 2025-01-11，两份内容都满足到期
// 规则，普通读取仍能成功，取回的处理日期却随保存顺序变化）。Open 必须按
// 保存记录损坏失败（ErrCorruptState），错误说明重复的是清册集合 manifests
// 本身，而不是误报某个申请编号重复；两处集合内容完全一致、只含不同申请、
// 其中一处为空对象或 null 都按同一规则拒绝，不合并、不择取最后一份，调整
// 两处的保存顺序也不能改变拒绝结果，原文件保持原样。
func TestOpenRejectsDuplicateManifestsField(t *testing.T) {
	// arc 构造一份登记记录；extra 给出销毁状态与清册归属等附加字段。
	arc := func(id, category, end, extra string) string {
		return `{"id":"` + id + `","category":"` + category + `","start":"2020-01-01","end":"` + end +
			`","initial_end":"` + end + `"` + extra + `,"freezes":[]}`
	}
	// man 构造一份清册记录；记录内部仍带 application_id、processed_on、
	// entries 等同名字段——这些是清册内部字段，不是最外层清册集合。
	man := func(app, processed, entries string) string {
		return `{"application_id":"` + app + `","processed_on":"` + processed + `","entries":[` + entries + `]}`
	}
	a1Destroyed := arc("A-1", "合同", "2025-01-10", `,"destroyed":true,"manifest_id":"APP-1"`)
	a2Destroyed := arc("A-2", "凭证", "2025-06-30", `,"destroyed":true,"manifest_id":"APP-2"`)
	entryA1 := `{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}`
	entryA2 := `{"id":"A-2","category":"凭证","start":"2020-01-01","end":"2025-06-30"}`
	manA1Day10 := man("APP-1", "2025-01-10", entryA1)
	manA1Day11 := man("APP-1", "2025-01-11", entryA1)
	manA2 := man("APP-2", "2025-06-30", entryA2)
	// escapedM 在 JSON 文本里是字符 m 的 Unicode 转义写法；拆成两段拼接，
	// 避免转义序列在源码里以连续文本出现。
	escapedM := "\\u00" + "6d"
	obj := func(entries string) string { return `{` + entries + `}` }
	// state 用两个字段名写法与两处集合内容拼出最外层含两个清册集合的保存记录；
	// 集合内容可以是任意 JSON 值（对象、空对象、null）。
	state := func(key1, manifests1, key2, manifests2 string) string {
		return `{"version":1,"archives":{` +
			`"A-1":` + a1Destroyed + `,"A-2":` + a2Destroyed +
			`},` + key1 + `:` + manifests1 + `,` + key2 + `:` + manifests2 + `}`
	}

	corrupt := map[string]string{
		"当天在前次日在后": state(`"manifests"`, obj(`"APP-1":`+manA1Day10), `"manifests"`, obj(`"APP-1":`+manA1Day11)),
		"交换保存顺序次日在前当天在后": state(`"manifests"`, obj(`"APP-1":`+manA1Day11), `"manifests"`, obj(`"APP-1":`+manA1Day10)),
		"两处集合内容完全一致":   state(`"manifests"`, obj(`"APP-1":`+manA1Day10), `"manifests"`, obj(`"APP-1":`+manA1Day10)),
		"两处只含不同申请":    state(`"manifests"`, obj(`"APP-1":`+manA1Day10), `"manifests"`, obj(`"APP-2":`+manA2)),
		"两处只含不同申请顺序相反": state(`"manifests"`, obj(`"APP-2":`+manA2), `"manifests"`, obj(`"APP-1":`+manA1Day10)),
		"第二处为空对象":     state(`"manifests"`, obj(`"APP-1":`+manA1Day10), `"manifests"`, `{}`),
		"第一处为空对象":     state(`"manifests"`, `{}`, `"manifests"`, obj(`"APP-1":`+manA1Day10)),
		"第二处为null":    state(`"manifests"`, obj(`"APP-1":`+manA1Day10), `"manifests"`, `null`),
		"第一处为null":    state(`"manifests"`, `null`, `"manifests"`, obj(`"APP-1":`+manA1Day10)),
		"两处都为null":    state(`"manifests"`, `null`, `"manifests"`, `null`),
		// escapedM 与后续文本拼成 "manifests"，JSON 解码后与直接写出的
		// manifests 是同一个字段名。
		"Unicode转义字段名与直接字段名重复": state(`"manifests"`, obj(`"APP-1":`+manA1Day10), `"`+escapedM+`anifests"`, obj(`"APP-1":`+manA1Day11)),
		"大小写写法混用重复":            state(`"manifests"`, obj(`"APP-1":`+manA1Day10), `"Manifests"`, obj(`"APP-1":`+manA1Day11)),
		"大小写写法混用顺序相反":          state(`"Manifests"`, obj(`"APP-1":`+manA1Day11), `"manifests"`, obj(`"APP-1":`+manA1Day10)),
		"清册集合出现三次": `{"version":1,"archives":{"A-1":` + a1Destroyed + `},` +
			`"manifests":{"APP-1":` + manA1Day10 + `},"manifests":{"APP-2":` + manA2 +
			`},"manifests":{}}`,
	}
	// 转义写法需要字面反斜杠，单独构造。
	corrupt["Unicode转义字段名表示同一字段"] = `{"version":1,"archives":{"A-1":` + a1Destroyed +
		`},"manifests":{"APP-1":` + manA1Day10 +
		`},"` + escapedM + `anifests":{"APP-1":` + manA1Day11 + `}}`

	for name, content := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, content)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("最外层出现两次清册集合时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "manifests") || !strings.Contains(msg, "清册集合") {
				t.Fatalf("错误信息应说明重复的是清册集合 manifests，得到 %v", err)
			}
			// 不得误报某个申请编号重复：错误信息不涉及任何具体申请编号。
			for _, id := range []string{"APP-1", "APP-2"} {
				if strings.Contains(msg, id) {
					t.Fatalf("错误信息不应误报申请编号 %s 重复，得到 %v", id, err)
				}
			}
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：清册集合只出现一次时继续可读——包括现有能识别为清册集合的大小写
	// 写法与转义写法单独出现；清册集合缺省或仅一次为 null 的空库兼容行为不变；
	// 各份清册内部出现同名字段（application_id、处理日期、条目 id，甚至名为
	// manifests 的未知内部字段）是正常格式，不属于最外层的清册集合字段，不会
	// 被误判。
	t.Run("合法记录对照组", func(t *testing.T) {
		valid := map[string]string{
			"manifests只出现一次": `{"version":1,"archives":{` +
				`"A-1":` + a1Destroyed + `,"A-2":` + a2Destroyed + `},` +
				`"manifests":{"APP-1":` + manA1Day10 + `,"APP-2":` + manA2 + `}}`,
			"大小写写法Manifests单独出现": `{"version":1,"archives":{"A-1":` + a1Destroyed +
				`},"Manifests":{"APP-1":` + manA1Day10 + `}}`,
			"大小写写法MANIFESTS单独出现": `{"version":1,"archives":{"A-1":` + a1Destroyed +
				`},"MANIFESTS":{"APP-1":` + manA1Day10 + `}}`,
			"清册集合缺省":             `{"version":1,"archives":{}}`,
			"manifests仅一次为null": `{"version":1,"archives":{},"manifests":null}`,
			"多份清册内部字段同名": `{"version":1,"archives":{` +
				`"A-1":` + a1Destroyed + `,"A-2":` + a2Destroyed + `},` +
				`"manifests":{"APP-1":` + manA1Day10 + `,"APP-2":` + manA2 + `}}`,
			"清册记录里名为manifests的内部字段": `{"version":1,"archives":{"A-1":` + a1Destroyed +
				`},"manifests":{"APP-1":{"application_id":"APP-1","processed_on":"2025-01-10","entries":[` + entryA1 + `],"manifests":{"APP-9":{}}}}}`,
		}
		// 转义写法需要字面反斜杠，单独构造。
		valid["Unicode转义字段名单独出现"] = `{"version":1,"archives":{"A-1":` + a1Destroyed +
			`},"` + escapedM + `anifests":{"APP-1":` + manA1Day10 + `}}`
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

// 保管库打开后保存内容才出现重复的清册集合：下一次使用有效输入查询历史、取回
// 清册、销毁前核对与各项办理都必须按整库记录损坏失败——即使查询、操作的是
// 另一份正常档案或另一份正常清册——不返回正常历史、清册或部分核对报告，不
// 销毁、不生成清册或产生其他业务变更，不合并两处集合或重新保存来消除重复，
// 原保存内容保持原样。两处集合中 APP-1 清册条目与归属一致、处理日期分别为
// 截止日当天与次日（均满足到期规则），结果不得随保存顺序变化。
func TestDuplicateManifestsFieldAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")
	reg(t, s, "A-3", "单据", "2020-01-01", "2025-01-10")
	// A-1 在截止日当天合法销毁，生成 APP-1 清册。
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatal(err)
	}
	// A-3 另以 APP-3 合法销毁，供损坏后验证无关清册的取回与重放也整库失败。
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-3", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-3"},
	}); err != nil {
		t.Fatal(err)
	}
	if m, found, err := s.GetManifest("APP-1"); err != nil || !found ||
		!m.ProcessedOn.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("损坏前清册应可取回且处理日期为当天: found=%v err=%v %+v", found, err, m)
	}

	// 在最外层再写入一个清册集合：其中 APP-1 清册条目快照与归属一致，处理日期
	// 改为次日 2025-01-11（同样满足到期规则）。普通解码会只剩后一个集合（即
	// 原文件位置的当天集合）；交换两处保存顺序时取回结果就会变成次日那份——
	// 已关闭清册的历史不能随保存顺序改变。
	good := readStateFile(t, dir)
	second := `"manifests": {
    "APP-1": {
      "application_id": "APP-1",
      "processed_on": "2025-01-11",
      "entries": [
        {
          "id": "A-1",
          "category": "合同",
          "start": "2020-01-01",
          "end": "2025-01-10"
        }
      ]
    }
  },
  "manifests"`
	corrupt := strings.Replace(good, `"manifests"`, second, 1)
	if corrupt == good {
		t.Fatal("未找到清册集合的位置，测试夹具失效")
	}
	writeStateFile(t, dir, corrupt)
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误说明重复的是清册集合 manifests。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("最外层出现两个清册集合时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "manifests") ||
		!strings.Contains(err.Error(), "清册集合") {
		t.Fatalf("打开错误应为 ErrCorruptState 并说明清册集合 manifests 重复，得到 %v", err)
	}
	// 不得误报成某个申请编号重复。
	if strings.Contains(err.Error(), "APP-1") || strings.Contains(err.Error(), "多份清册") {
		t.Fatalf("错误应说明清册集合字段本身重复，而不是申请编号重复: %v", err)
	}

	// 已打开实例：即使本次只查询、核对或办理与重复清册无关的正常档案 A-2、
	// 正常清册 APP-3，也必须按整库损坏失败。
	if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后查询无关档案也必须失败且无结论: found=%v err=%v", found, err)
	}
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History 必须失败且不得给出销毁历史与清册: found=%v err=%v", found, err)
	}
	if m, found, err := s.GetManifest("APP-3"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("损坏后取回无关清册也必须失败: found=%v err=%v %+v", found, err, m)
	}
	if m, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("损坏后 GetManifest 必须失败且不得返回任一处集合中的清册: found=%v err=%v %+v", found, err, m)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出可以办理等结论或部分报告: %+v err=%v", r, err)
	}
	// 关键回归点：损坏状态下绝不能按任一处集合给出“可以取回原清册”的结论，
	// 无论按当天还是次日重放。
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || r.Manifest != nil {
		t.Fatalf("损坏后对 APP-1 的当天核对也必须失败，不能附任一处清册: %+v err=%v", r, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-11"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || r.Manifest != nil {
		t.Fatalf("损坏后对 APP-1 的次日核对也必须失败，不能附任一处清册: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
	}
	// 已成功申请的幂等重放（无论对应被重复集合覆盖的 APP-1 还是无关的 APP-3）
	// 都不能取回原清册。
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 APP-1 的幂等重放也必须失败且不得返回清册: %+v err=%v", m, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-3", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-3"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后无关申请 APP-3 的重放也必须失败: %+v err=%v", m, err)
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
		ArchiveID: "A-2", FreezeID: "F-X", Reason: "结案", ReleasedOn: MustParseDate("2025-06-02"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Release 必须失败（整库损坏先于业务校验）: %v", err)
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-9", ArchiveID: "A-2",
		OriginalEnd: MustParseDate("2025-06-30"), NewEnd: MustParseDate("2026-06-30"),
		RevisedOn: MustParseDate("2025-06-01"), Reason: "延期",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Revise 必须失败: %v", err)
	}

	// 失败不得触发修复：不合并两处集合、不挑选其中一份、不重新保存来消除重复，
	// 原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 去掉重复的清册集合后，合法记录恢复可用：APP-1 仍是唯一的原清册（当天
	// 处理），历史查询、按编号取回和相同日期及档案集合的重放都取得原内容；
	// 改变日期仍按已有编号冲突规则处理；无关档案业务不受影响。
	writeStateFile(t, dir, good)
	h, found, err := s.History("A-1")
	if err != nil || !found || h.Manifest == nil {
		t.Fatalf("恢复后历史查询应成功并附原清册: found=%v err=%v %+v", found, err, h)
	}
	if !h.Manifest.ProcessedOn.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("恢复后历史中的应是唯一原清册（2025-01-10），得到 %s", h.Manifest.ProcessedOn)
	}
	m, found, err := s.GetManifest("APP-1")
	if err != nil || !found {
		t.Fatalf("恢复后清册应可取回: found=%v err=%v", found, err)
	}
	if !m.ProcessedOn.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("恢复后取回的应是唯一原清册（2025-01-10），得到 %s", m.ProcessedOn)
	}
	replay, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil || !replay.ProcessedOn.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("恢复后相同日期与集合的重放应取回唯一原清册: %+v err=%v", replay, err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-11"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrApplicationMismatch) {
		t.Fatalf("恢复后沿用编号改变日期仍应按编号冲突失败，得到 %v", err)
	}
	if _, err := s.Check(CheckRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); err != nil {
		t.Fatalf("恢复后无关档案的核对应正常: %v", err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); err != nil {
		t.Fatalf("恢复后无关档案应能正常办理销毁: %v", err)
	}
}
