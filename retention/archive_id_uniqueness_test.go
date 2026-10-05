package retention

import (
	"errors"
	"strings"
	"testing"
)

// 保存的档案集合中同一编号出现两次时，即使 JSON 本身能解析，Open 也必须
// 按保存记录损坏失败（ErrCorruptState）：错误指出重复的档案编号并说明
// 登记重复，原文件保持原样。普通解码会让后一份登记静默覆盖前一份（例如
// 前一份带未解除诉讼冻结、后一份冻结列表为空，只剩后者会被误判为可以
// 销毁），绝不能接受这种覆盖。两份内容不同、或类别/日期/冻结/修订历史
// 完全相同，都不是合并或忽略重复的理由。
func TestOpenRejectsDuplicateArchiveRegistration(t *testing.T) {
	activeFreeze := `{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false}`
	// rec 构造一份登记记录；key 给出它在档案集合对象中的键（可含转义）。
	rec := func(id, category, end, freezes string) string {
		return `{"id":"` + id + `","category":"` + category + `","start":"2020-01-01","end":"` + end +
			`","initial_end":"` + end + `","destroyed":false,"freezes":[` + freezes + `]}`
	}
	entry := func(key, recordJSON string) string { return `"` + key + `":` + recordJSON }
	state := func(entries string) string {
		return `{"version":1,"archives":{` + entries + `},"manifests":{}}`
	}
	a1Frozen := rec("A-1", "合同", "2025-01-10", activeFreeze)
	a1NoFreeze := rec("A-1", "合同", "2025-01-10", "")

	corrupt := map[string]struct {
		state   string
		wantID  string
		wantAll []string
	}{
		"前一份带未解除冻结后一份冻结为空": {
			state(entry("A-1", a1Frozen) + "," + entry("A-1", a1NoFreeze)),
			"A-1", []string{"A-1"},
		},
		"顺序相反同样拒绝不得按冻结挑选": {
			state(entry("A-1", a1NoFreeze) + "," + entry("A-1", a1Frozen)),
			"A-1", []string{"A-1"},
		},
		"两份登记内容完全一致": {
			state(entry("A-1", a1Frozen) + "," + entry("A-1", a1Frozen)),
			"A-1", []string{"A-1"},
		},
		"两份内容不同类别不同": {
			state(entry("A-1", a1NoFreeze) + "," +
				entry("A-1", rec("A-1", "凭证", "2026-12-31", ""))),
			"A-1", []string{"A-1"},
		},
		"后一份期限更长也不得挑选可信记录": {
			state(entry("A-1", a1Frozen) + "," +
				entry("A-1", rec("A-1", "合同", "2099-01-01", ""))),
			"A-1", []string{"A-1"},
		},
		"Unicode转义键与直接键同编号": {
			// Go 源里的 \\u0031 是字面的反斜杠+u0031，JSON 解码后才是字符 1，
			// 与直接写出的 A-1 是同一个编号。
			`{"version":1,"archives":{"A-1":` + a1Frozen +
				`,"A-\u0031":` + a1NoFreeze + `},"manifests":{}}`,
			"A-1", []string{"A-1"},
		},
		"多份档案中第二份编号重复": {
			state(entry("A-1", a1NoFreeze) + "," + entry("B-7", rec("B-7", "凭证", "2025-06-30", "")) +
				"," + entry("B-7", rec("B-7", "凭证", "2025-06-30", ""))),
			"B-7", []string{"B-7"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("同一档案编号登记两次时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			for _, want := range tc.wantAll {
				if !strings.Contains(msg, want) {
					t.Fatalf("错误信息应指出重复编号 %q，得到 %v", want, err)
				}
			}
			if !strings.Contains(msg, "重复") || !strings.Contains(msg, "登记") {
				t.Fatalf("错误信息应说明登记重复，得到 %v", err)
			}
			if got := readStateFile(t, dir); got != tc.state {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：不同编号各自登记一次不受影响——即使每条登记内容里都出现相同的
	// 字段名（id、freezes 内的 id），那也只是登记记录的内部字段，不是档案
	// 集合的重复键；旧档案缺少 initial_end 与 archives 为 null 继续合法。
	t.Run("合法记录对照组", func(t *testing.T) {
		valid := map[string]string{
			"不同编号各登记一次且内部字段同名": state(
				entry("A-1", rec("A-1", "合同", "2025-01-10", activeFreeze)) + "," +
					entry("A-2", rec("A-2", "合同", "2025-01-10", activeFreeze))),
			"转义键解码后仍是另一个编号": `{"version":1,"archives":{"A-1":` + a1NoFreeze +
				`,"A-\u0032":` + rec("A-2", "凭证", "2025-06-30", "") + `},"manifests":{}}`,
			"archives为null按空库": `{"version":1,"archives":null,"manifests":{}}`,
			"旧档案缺少initial_end": `{"version":1,"archives":{"A-1":` +
				`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","destroyed":false,"freezes":[]}` +
				`},"manifests":{}}`,
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

// 保管库打开后保存内容才出现同编号重复登记：下一次使用有效输入查询历史、
// 取回清册（含已成功申请的幂等重放）、销毁前核对与各项办理都必须按整库
// 记录损坏失败——即使操作的是另一份正常档案——不返回正常历史、清册或部分
// 核对报告，不写入新记录、不改变销毁状态或生成清册，原保存内容保持原样。
func TestDuplicateArchiveRegistrationAfterOpenFailsAllOperations(t *testing.T) {
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
	if _, found, err := s.GetManifest("APP-3"); err != nil || !found {
		t.Fatalf("损坏前清册应可取回: found=%v err=%v", found, err)
	}

	// 在档案集合末尾再写入一份 A-1：期限相同但冻结列表为空——正是“先保存
	// 带未解除冻结的 A-1，随后又保存一份无冻结 A-1”的情形。普通解码会只剩
	// 后一份，核对将误报可以办理。
	good := readStateFile(t, dir)
	dup := `,
    "A-1": {"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[]}`
	corrupt := strings.Replace(good, "\n  },\n  \"manifests\"", dup+"\n  },\n  \"manifests\"", 1)
	if corrupt == good {
		t.Fatal("未找到档案集合的结束位置，测试夹具失效")
	}
	writeStateFile(t, dir, corrupt)
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误指出重复编号 A-1 并说明登记重复。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("档案集合出现同编号登记时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "A-1") ||
		!strings.Contains(err.Error(), "重复") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出重复的 A-1，得到 %v", err)
	}

	// 已打开实例：即使本次只操作与重复登记无关的正常档案 A-2，
	// 也必须按整库损坏失败。
	if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后查询无关档案也必须失败且无结论: found=%v err=%v", found, err)
	}
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History 必须失败且不得给出登记/冻结历史: found=%v err=%v", found, err)
	}
	if m, found, err := s.GetManifest("APP-3"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("损坏后 GetManifest 必须失败: found=%v err=%v %+v", found, err, m)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出可以办理等结论或部分报告: %+v err=%v", r, err)
	}
	// 关键回归点：损坏状态下绝不能把“冻结列表为空的后一份 A-1”当作依据，
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
		t.Fatalf("损坏后不得凭后一份无冻结登记销毁 A-1: %+v err=%v", m, err)
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

	// 失败不得触发修复：不删除重复登记、不挑选其中一份，原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 去掉重复登记后，合法记录恢复可用：A-1 的冻结历史仍是损坏前那一条
	// 未解除冻结，不会被“无冻结的第二份”顶替；无关档案业务不受影响。
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
