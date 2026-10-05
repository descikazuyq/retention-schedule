package retention

import (
	"errors"
	"strings"
	"testing"
)

// 保存的清册集合中同一申请编号出现两次时，即使 JSON 本身能解析、两份清册
// 各自都符合档案归属、条目快照、期限与处理日期规则，Open 也必须按保存记录
// 损坏失败（ErrCorruptState）：错误指出重复的申请编号并说明该编号对应多份
// 清册，原文件保持原样。普通解码会让后一份清册静默覆盖前一份，取回结果随
// 保存顺序改变——已成功的销毁申请只能对应一份已关闭清册，这种重复保存不
// 是正常的申请重试。两份内容完全一致，或收录相同档案但处理日期不同（即使
// 两个日期都满足到期规则），都不是合并、忽略或挑选其中一份的理由，且拒绝
// 结果与两份记录的保存顺序无关。
func TestOpenRejectsDuplicateManifestApplicationID(t *testing.T) {
	// escapedOne / escapedTwo 在 JSON 文本里分别是字符 1、2 的 Unicode
	// 转义写法；拆成两段拼接，避免转义序列在源码里以连续文本出现。
	escapedOne := "\\u00" + "31"
	escapedTwo := "\\u00" + "32"
	// arc 构造一份登记记录；extra 给出销毁状态与清册归属等附加字段。
	arc := func(id, category, end, extra string) string {
		return `{"id":"` + id + `","category":"` + category + `","start":"2020-01-01","end":"` + end +
			`","initial_end":"` + end + `"` + extra + `,"freezes":[]}`
	}
	// man 构造一份清册记录；键由外层 entry 给出（可含转义），记录内部仍带
	// application_id、processed_on、entries 等同名字段。
	man := func(app, processed, entries string) string {
		return `{"application_id":"` + app + `","processed_on":"` + processed + `","entries":[` + entries + `]}`
	}
	entry := func(key, recordJSON string) string { return `"` + key + `":` + recordJSON }
	state := func(archives, manifests string) string {
		return `{"version":1,"archives":{` + archives + `},"manifests":{` + manifests + `}}`
	}
	a1Destroyed := entry("A-1", arc("A-1", "合同", "2025-01-10", `,"destroyed":true,"manifest_id":"APP-1"`))
	a2Destroyed := entry("A-2", arc("A-2", "凭证", "2025-06-30", `,"destroyed":true,"manifest_id":"APP-2"`))
	entryA1 := `{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}`
	entryA2 := `{"id":"A-2","category":"凭证","start":"2020-01-01","end":"2025-06-30"}`
	manA1Day10 := man("APP-1", "2025-01-10", entryA1)
	manA1Day11 := man("APP-1", "2025-01-11", entryA1)

	corrupt := map[string]struct {
		state  string
		wantID string
	}{
		"两份清册完全一致": {
			state(a1Destroyed, entry("APP-1", manA1Day10)+","+entry("APP-1", manA1Day10)),
			"APP-1",
		},
		"相同档案不同处理日期当天在前次日在后": {
			// 档案截止日为 2025-01-10，两个处理日期都满足到期规则，
			// 仍不能选取其中一份。
			state(a1Destroyed, entry("APP-1", manA1Day10)+","+entry("APP-1", manA1Day11)),
			"APP-1",
		},
		"交换保存顺序次日在前当天在后": {
			state(a1Destroyed, entry("APP-1", manA1Day11)+","+entry("APP-1", manA1Day10)),
			"APP-1",
		},
		"Unicode转义键与直接键同申请编号": {
			// 第二个键以字符 1 的 Unicode 转义形式写出，JSON 解码后与直接
			// 写出的 APP-1 是同一个申请编号。
			`{"version":1,"archives":{` + a1Destroyed + `},"manifests":{"APP-1":` + manA1Day10 +
				`,"APP-` + escapedOne + `":` + manA1Day11 + `}}`,
			"APP-1",
		},
		"多份清册中另一申请编号重复": {
			state(a1Destroyed+","+a2Destroyed,
				entry("APP-1", man("APP-1", "2025-01-10", entryA1))+","+
					entry("APP-2", man("APP-2", "2025-06-30", entryA2))+","+
					entry("APP-2", man("APP-2", "2025-06-30", entryA2))),
			"APP-2",
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("同一申请编号对应两份清册时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.wantID) {
				t.Fatalf("错误信息应指出重复的申请编号 %q，得到 %v", tc.wantID, err)
			}
			if !strings.Contains(msg, "多份清册") {
				t.Fatalf("错误信息应说明该编号对应多份清册，得到 %v", err)
			}
			if got := readStateFile(t, dir); got != tc.state {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：不同申请编号各自对应一份合法清册不受影响——即使每份清册记录
	// 内部都出现相同的字段名（application_id、processed_on、entries、条目
	// 的 id），那也只是清册记录的内部字段，不是清册集合的重复键；manifests
	// 为 null 与尚无清册的合法空库继续可用。
	t.Run("合法记录对照组", func(t *testing.T) {
		// 转义键 APP-<esc-2> 解码后是 APP-2，与直接写出的 APP-1 不同编号，
		// 必须正常打开。
		escapedA2Archive := entry("A-2", arc("A-2", "凭证", "2025-06-30", `,"destroyed":true,"manifest_id":"APP-2"`))
		valid := map[string]string{
			"不同申请编号各对应一份清册且内部字段同名": state(
				a1Destroyed+","+a2Destroyed,
				entry("APP-1", man("APP-1", "2025-01-10", entryA1))+","+
					entry("APP-2", man("APP-2", "2025-06-30", entryA2))),
			"转义键解码后是另一个申请编号": `{"version":1,"archives":{` + escapedA2Archive +
				`},"manifests":{"APP-` + escapedTwo + `":` + man("APP-2", "2025-06-30", entryA2) + `}}`,
			"manifests为null按空集合": `{"version":1,"archives":{},"manifests":null}`,
			"尚无清册的合法空库":          `{"version":1,"archives":{},"manifests":{}}`,
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

// 保管库打开后保存内容才出现同一申请编号的重复清册：下一次查询历史、取回
// 清册（含已成功申请的幂等重放）、销毁前核对与各项办理都必须按整库记录
// 损坏失败——即使操作的是另一份正常档案或另一份正常清册——不返回正常历史、
// 清册或部分核对报告，不销毁、不生成清册或产生其他业务变更，原保存内容
// 保持原样。重复的两份清册收录相同档案、处理日期分别为截止日当天与次日
// （均满足到期规则），结果不得随保存顺序改变。
func TestDuplicateManifestAfterOpenFailsAllOperations(t *testing.T) {
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

	// 在清册集合中 APP-1 记录之后再写入一份 APP-1：收录相同档案、条目快照
	// 一致，处理日期改为次日 2025-01-11（同样满足到期规则）。普通解码会只剩
	// 后一份，取回的清册随保存顺序变成次日的那份。
	good := readStateFile(t, dir)
	dup := `"APP-1": {
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
    }`
	// 锚点是 APP-1 记录闭合与 APP-3 键之间：在闭合之后、APP-3 之前追加一份
	// 同申请编号记录（补逗号分隔），成为清册集合顶层的第二个 APP-1 键。
	corrupt := strings.Replace(good,
		"\n    },\n    \"APP-3\":",
		"\n    },\n    "+dup+",\n    \"APP-3\":", 1)
	if corrupt == good {
		t.Fatal("未找到清册集合中 APP-1 记录的结束位置，测试夹具失效")
	}
	writeStateFile(t, dir, corrupt)
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误指出重复申请编号 APP-1 并说明对应多份清册。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("清册集合出现同申请编号的两份记录时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "APP-1") ||
		!strings.Contains(err.Error(), "多份清册") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出重复的 APP-1 对应多份清册，得到 %v", err)
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
		t.Fatalf("损坏后 GetManifest 必须失败且不得返回任一份重复清册: found=%v err=%v %+v", found, err, m)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出可以办理等结论或部分报告: %+v err=%v", r, err)
	}
	// 关键回归点：损坏状态下绝不能按后一份（次日处理）清册或前一份给出
	// “可以取回原清册”的结论。
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || r.Manifest != nil {
		t.Fatalf("损坏后对 APP-1 的核对也必须失败，不能附任一份重复清册: %+v err=%v", r, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-11"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || r.Manifest != nil {
		t.Fatalf("损坏后即使与后一份清册一致也不得取回: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
	}
	// 已成功申请的幂等重放（无论对应重复编号还是无关清册）都不能取回原清册。
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

	// 失败不得触发修复：不合并重复清册，也不删除、改号或重新生成清册，
	// 原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 去掉重复清册后，合法记录恢复可用：APP-1 仍是唯一的原清册（当天处理），
	// 幂等重放取回该份；改变日期仍按已有编号冲突规则处理；其他业务不受影响。
	writeStateFile(t, dir, good)
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
