package retention

import (
	"errors"
	"strings"
	"testing"
)

// escApp1/escApp2 的源码文本是 APP- 紧接数字 1/2 的 Unicode 转义
// （JSON 解码后分别为 APP-1、APP-2），用于验证身份比较以 JSON 解码后的
// 实际文本为准。
const (
	escApp1 = "APP-\\u0031"
	escApp2 = "APP-\\u0032"
)

// 保存的清册集合中，用于找到每份清册的编号（对象键）与该份清册内容里的
// application_id 必须都是非空白文本，且是同一个编号。两处编号不同，或清册
// 内容的 application_id 缺失、为 null、为空串、只有空白时，即使 JSON 能
// 解析、档案已经销毁、归属关系正确、条目快照与处理日期都合法、期限修订
// 完整，Open 也必须按保存记录损坏失败（ErrCorruptState），错误能区分
// “编号不一致”与“缺少有效编号”，且原文件保持原样。
func TestOpenRejectsManifestApplicationIDMismatch(t *testing.T) {
	// releasedFreeze 是一条完整合法的已解除冻结，供对照“其他校验全部通过
	// 也不能掩盖编号矛盾”。
	releasedFreeze := `{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-06"}`
	// arc 构造一份登记记录；extra 给出销毁状态与清册归属等附加字段。
	arc := func(id, category, end, extra, freezes, revisions string) string {
		return `{"id":"` + id + `","category":"` + category + `","start":"2020-01-01","end":"` + end +
			`","initial_end":"` + end + `"` + extra + `,"freezes":[` + freezes + `],"revisions":[` + revisions + `]}`
	}
	entryA1 := `{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}`
	// man 构造一份清册记录；appIDRaw 给出 application_id 字段值的原始 JSON
	// 文本，允许写出 null、空串、Unicode 转义等特殊形式。
	man := func(appIDRaw, processed, entries string) string {
		return `{"application_id":` + appIDRaw + `,"processed_on":"` + processed + `","entries":[` + entries + `]}`
	}
	// manNoApp 构造一条缺少 application_id 字段的清册记录。
	manNoApp := func(processed, entries string) string {
		return `{"processed_on":"` + processed + `","entries":[` + entries + `]}`
	}
	entry := func(key, recordJSON string) string { return `"` + key + `":` + recordJSON }
	state := func(archives, manifests string) string {
		return `{"version":1,"archives":{` + archives + `},"manifests":{` + manifests + `}}`
	}
	a1Destroyed := entry("A-1", arc("A-1", "合同", "2025-01-10", `,"destroyed":true,"manifest_id":"APP-1"`, "", ""))
	a2Destroyed := entry("A-2", arc("A-2", "凭证", "2025-06-30", `,"destroyed":true,"manifest_id":"APP-2"`, "", ""))

	corrupt := map[string]struct {
		state    string
		wantAll  []string
		wantKind string
	}{
		"挂在APP-1名下application_id写成APP-2": {
			state(a1Destroyed, entry("APP-1", man(`"APP-2"`, "2025-01-10", entryA1))),
			[]string{"APP-1", "APP-2"}, "不一致",
		},
		"application_id字段缺失": {
			state(a1Destroyed, entry("APP-1", manNoApp("2025-01-10", entryA1))),
			[]string{"APP-1", "application_id"}, "缺少",
		},
		"application_id为null": {
			state(a1Destroyed, entry("APP-1", man(`null`, "2025-01-10", entryA1))),
			[]string{"APP-1", "application_id"}, "缺少",
		},
		"application_id为空串": {
			state(a1Destroyed, entry("APP-1", man(`""`, "2025-01-10", entryA1))),
			[]string{"APP-1", "application_id"}, "缺少",
		},
		"application_id只有空白": {
			state(a1Destroyed, entry("APP-1", man(`"   "`, "2025-01-10", entryA1))),
			[]string{"APP-1", "application_id"}, "缺少",
		},
		"application_id只有制表符与换行": {
			state(a1Destroyed, entry("APP-1", man(`"\t\n "`, "2025-01-10", entryA1))),
			[]string{"APP-1", "application_id"}, "缺少",
		},
		"仅大小写不同不算一致": {
			state(a1Destroyed, entry("APP-1", man(`"app-1"`, "2025-01-10", entryA1))),
			[]string{"APP-1", "app-1"}, "不一致",
		},
		"仅首尾空白不同不算一致": {
			state(a1Destroyed, entry("APP-1", man(`" APP-1 "`, "2025-01-10", entryA1))),
			[]string{"APP-1", " APP-1 "}, "不一致",
		},
		"application_id通过Unicode转义写成另一个编号": {
			// 键直接写 APP-1；application_id 的源文本是 APP- 紧接 2 的
			// Unicode 转义，JSON 解码后为 APP-2，按解码后文本比较即不一致。
			state(a1Destroyed, entry("APP-1", man(`"`+escApp2+`"`, "2025-01-10", entryA1))),
			[]string{"APP-1", "APP-2"}, "不一致",
		},
		"已销毁归属正确期限修订完整冻结已解除仍因编号矛盾拒绝": {
			// 除编号矛盾外，这份记录的其余内容全部自洽：A-1 已销毁、归属指向
			// APP-1、清册条目保存的是修订后的最终截止日 2026-01-10、处理日期
			// 2026-01-10 当天、修订链衔接（最初截止日 2025-01-10，经 R-1 延长
			// 到 2026-01-10）且不早于起算日、冻结已合法解除。
			// 合法的清册归属、条目快照、处理日期与期限修订都不能掩盖矛盾。
			`{"version":1,"archives":{"A-1":` +
				`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2026-01-10","initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1","freezes":[` +
				releasedFreeze + `],"revisions":[` +
				`{"id":"R-1","old_end":"2025-01-10","new_end":"2026-01-10","revised_on":"2025-01-01","reason":"延期"}` +
				`]}},"manifests":{"APP-1":` +
				man(`"APP-2"`, "2026-01-10",
					`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2026-01-10"}`) + `}}`,
			[]string{"APP-1", "APP-2"}, "不一致",
		},
		"用于查找的键为纯空白": {
			state("", entry(" ", man(`"APP-1"`, "2025-01-10", ""))),
			[]string{"空白"}, "空白",
		},
		"用于查找的键为空串": {
			state("", entry("", man(`"APP-1"`, "2025-01-10", ""))),
			[]string{"空白"}, "空白",
		},
		"多份清册中第二份身份矛盾": {
			state(a1Destroyed+","+a2Destroyed,
				entry("APP-1", man(`"APP-1"`, "2025-01-10", entryA1))+","+
					entry("APP-2", man(`"APP-3"`, "2025-06-30",
						`{"id":"A-2","category":"凭证","start":"2020-01-01","end":"2025-06-30"}`))),
			[]string{"APP-2", "APP-3"}, "不一致",
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("清册申请编号与查找编号不一致时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			for _, want := range tc.wantAll {
				if !strings.Contains(msg, want) {
					t.Fatalf("错误信息应包含 %q，得到 %v", want, err)
				}
			}
			if !strings.Contains(msg, tc.wantKind) {
				t.Fatalf("错误信息应能识别为 %q 类问题，得到 %v", tc.wantKind, err)
			}
			if got := readStateFile(t, dir); got != tc.state {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：键与 application_id 解码后为同一文本即合法——包括两侧分别使用
	// Unicode 转义、不同申请编号各自对应一份清册、manifests 为 null 或空。
	t.Run("身份一致的记录对照组", func(t *testing.T) {
		valid := map[string]string{
			"键与application_id直接写出且相同": state(
				a1Destroyed, entry("APP-1", man(`"APP-1"`, "2025-01-10", entryA1))),
			"键用Unicode转义application_id直接写出": state(
				a1Destroyed, entry(escApp1, man(`"APP-1"`, "2025-01-10", entryA1))),
			"键直接写出application_id用Unicode转义": state(
				a1Destroyed, entry("APP-1", man(`"`+escApp1+`"`, "2025-01-10", entryA1))),
			"两侧都用Unicode转义解码后相同": state(
				a1Destroyed, entry(escApp1, man(`"`+escApp1+`"`, "2025-01-10", entryA1))),
			"不同申请编号各对应一份清册不受影响": state(
				a1Destroyed+","+a2Destroyed,
				entry("APP-1", man(`"APP-1"`, "2025-01-10", entryA1))+","+
					entry("APP-2", man(`"APP-2"`, "2025-06-30",
						`{"id":"A-2","category":"凭证","start":"2020-01-01","end":"2025-06-30"}`))),
			"manifests为null按空集合": `{"version":1,"archives":{},"manifests":null}`,
			"尚无清册的合法空库":          `{"version":1,"archives":{},"manifests":{}}`,
		}
		for name, content := range valid {
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				writeStateFile(t, dir, content)
				s, err := Open(dir)
				if err != nil {
					t.Fatalf("身份一致的记录应能打开: %v", err)
				}
				defer s.Close()
			})
		}
	})
}

// 保管库打开后保存内容才出现清册申请编号矛盾：下一次使用有效输入查询历史、
// 取回清册（含已成功申请的幂等重放）、销毁前核对或办理业务，即使操作的是
// 另一份正常档案或另一份正常清册，也必须按整库记录损坏失败——不返回正常
// 历史、清册或部分报告，不写入业务变更，不用查找编号补写清册、不以清册
// 内容改号、也不删除冲突记录，原保存内容保持原样；恢复一致后清册继续可用。
func TestManifestApplicationIDMismatchAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")
	reg(t, s, "A-3", "单据", "2020-01-01", "2025-01-10")
	// A-1 的冻结先登记再合法解除：损坏后这份解除记录完整无缺，仍不能掩盖
	// 其所属清册的编号矛盾。
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-06"),
	}); err != nil {
		t.Fatal(err)
	}
	// A-2 保留一条未解除冻结，供损坏后验证 Release 也整库失败。
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-2", FreezeID: "F-X", Reason: "审计", FrozenOn: MustParseDate("2025-06-01"),
	}); err != nil {
		t.Fatal(err)
	}
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
		m.ApplicationID != "APP-1" || !m.ProcessedOn.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("损坏前清册应可取回: found=%v err=%v %+v", found, err, m)
	}

	// 把 APP-1 清册内容里的 application_id 改成 APP-2：清册仍挂在 APP-1
	// 名下，却自称属于 APP-2。
	good := readStateFile(t, dir)
	rewriteState(t, dir, func(doc map[string]any) {
		doc["manifests"].(map[string]any)["APP-1"].(map[string]any)["application_id"] = "APP-2"
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误说明申请编号不一致并给出两处各自保存的编号。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("清册申请编号与查找编号不一致时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "APP-1") || !strings.Contains(err.Error(), "APP-2") ||
		!strings.Contains(err.Error(), "不一致") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出两处编号不一致，得到 %v", err)
	}

	// 已打开实例：即使本次只查询、核对或办理与矛盾清册无关的正常档案 A-2、
	// 正常清册 APP-3，也必须按整库损坏失败。
	if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后查询无关档案也必须失败且无结论: found=%v err=%v", found, err)
	}
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History 必须失败且不得给出标着 APP-2 的清册: found=%v err=%v", found, err)
	}
	if m, found, err := s.GetManifest("APP-3"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("损坏后取回无关清册也必须失败: found=%v err=%v %+v", found, err, m)
	}
	if m, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("损坏后 GetManifest 必须失败且不得返回标着 APP-2 的清册: found=%v err=%v %+v", found, err, m)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出部分报告: %+v err=%v", r, err)
	}
	// 关键回归点：损坏状态下绝不能返回标着 APP-2 的“原清册”。
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || r.Manifest != nil {
		t.Fatalf("损坏后对 APP-1 的核对也必须失败，不能附编号矛盾的清册: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
	}
	// 已成功申请的幂等重放（无论对应矛盾编号还是无关清册）都不能取回清册。
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 APP-1 的幂等重放也必须失败: %+v err=%v", m, err)
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
		ArchiveID: "A-2", FreezeID: "F-Y", Reason: "诉讼", FrozenOn: MustParseDate("2025-06-02"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Freeze 必须失败: %v", err)
	}
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-2", FreezeID: "F-X", Reason: "审计结束", ReleasedOn: MustParseDate("2025-06-02"),
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

	// 失败不得触发修复：不用查找编号补写、不以清册内容改号、不删除冲突记录，
	// 原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 恢复一致后整库恢复可用：APP-1 仍是唯一的原清册（2025-01-10 处理），
	// 幂等重放取回该份；改变日期仍按已有编号冲突规则处理；其他业务不受影响。
	writeStateFile(t, dir, good)
	m, found, err := s.GetManifest("APP-1")
	if err != nil || !found {
		t.Fatalf("恢复后清册应可取回: found=%v err=%v", found, err)
	}
	if m.ApplicationID != "APP-1" || !m.ProcessedOn.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("恢复后取回的应是编号一致的原清册: %+v", m)
	}
	replay, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil || replay.ApplicationID != "APP-1" || !replay.ProcessedOn.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("恢复后相同日期与集合的重放应取回唯一原清册: %+v err=%v", replay, err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-11"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrApplicationMismatch) {
		t.Fatalf("恢复后沿用编号改变日期仍应按编号冲突失败，得到 %v", err)
	}
	// A-2 解除冻结后可正常以 APP-2 办理销毁。
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-2", FreezeID: "F-X", Reason: "审计结束", ReleasedOn: MustParseDate("2025-06-02"),
	}); err != nil {
		t.Fatalf("恢复后解除应成功: %v", err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); err != nil {
		t.Fatalf("恢复后无关档案应能正常办理销毁: %v", err)
	}
	if _, found, err := s.GetManifest("APP-3"); err != nil || !found {
		t.Fatalf("恢复后无关清册 APP-3 仍应可查: found=%v err=%v", found, err)
	}
}

// 保管库打开后清册内容的 application_id 才缺失：与编号不一致一样按整库
// 记录损坏失败，下一次有效输入的任何操作都不返回正常结果，即使操作的是
// 另一份正常档案或另一份正常清册；错误指出对应的清册记录与缺失位置。
func TestManifestApplicationIDMissingAfterOpenFailsWholeVault(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-3", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); err != nil {
		t.Fatal(err)
	}

	good := readStateFile(t, dir)
	rewriteState(t, dir, func(doc map[string]any) {
		delete(doc["manifests"].(map[string]any)["APP-1"].(map[string]any), "application_id")
	})
	corruptContent := readStateFile(t, dir)

	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("清册内容缺少 application_id 时打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "APP-1") ||
		!strings.Contains(err.Error(), "application_id") ||
		!strings.Contains(err.Error(), "缺少") {
		t.Fatalf("应报缺少有效申请编号并指出清册记录 APP-1 与缺失位置，得到 %v", err)
	}
	if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("编号缺失后查询另一份正常档案也必须失败: found=%v err=%v", found, err)
	}
	if m, found, err := s.GetManifest("APP-3"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("编号缺失后取回另一份正常清册也必须失败: found=%v err=%v %+v", found, err, m)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" {
		t.Fatalf("编号缺失后核对必须整次失败: %+v err=%v", r, err)
	}
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 恢复后清册与历史继续完整可查。
	writeStateFile(t, dir, good)
	if m, found, err := s.GetManifest("APP-1"); err != nil || !found || m.ApplicationID != "APP-1" {
		t.Fatalf("恢复后 APP-1 清册应可取回: found=%v err=%v %+v", found, err, m)
	}
	h, found, err := s.History("A-1")
	if err != nil || !found || h.Manifest == nil || h.Manifest.ApplicationID != "APP-1" {
		t.Fatalf("恢复后销毁历史与清册应完整: found=%v err=%v %+v", found, err, h.Manifest)
	}
}
