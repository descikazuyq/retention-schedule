package retention

import (
	"errors"
	"strings"
	"testing"
)

// escApp1/escApp2 的源码文本是 APP- 紧接数字的 Unicode 转义（JSON 解码后
// 分别为 APP-1、APP-2），用于验证身份比较以 JSON 解码后的实际文本为准。
const (
	escApp1 = "APP-\\u0031"
	escApp2 = "APP-\\u0032"
)

// 保存的清册集合中，用于找到每份清册的编号（对象键）与该份清册内容里的
// application_id 必须都是非空白文本，且逐字相同。两处编号不同，或清册内容
// 的 application_id 缺失、为 null、为空串、只有空白时，即使 JSON 能解析、
// 档案已销毁且归属正确、条目快照与处理日期都合法，Open 也必须按保存记录
// 损坏失败（ErrCorruptState），错误能区分“编号不一致”与“缺少有效编号”，
// 且原文件保持原样。
func TestOpenRejectsManifestApplicationIdentityMismatch(t *testing.T) {
	// arc 构造一份已销毁档案的登记记录；extra 给出修订等附加字段。
	arc := func(id, category, end, extra string) string {
		return `{"id":"` + id + `","category":"` + category + `","start":"2020-01-01","end":"` + end +
			`","initial_end":"` + end + `"` + extra + `,"freezes":[]}`
	}
	// man 构造一份清册记录；键由外层 entry 给出，appText 是 application_id
	// 字段的原始 JSON 文本，允许写出 null、空串、Unicode 转义等特殊形式。
	man := func(appText, processed, entries string) string {
		return `{"application_id":` + appText + `,"processed_on":"` + processed +
			`","entries":[` + entries + `]}`
	}
	entry := func(key, recordJSON string) string { return `"` + key + `":` + recordJSON }
	state := func(archives, manifests string) string {
		return `{"version":1,"archives":{` + archives + `},"manifests":{` + manifests + `}}`
	}
	a1Destroyed := entry("A-1", arc("A-1", "合同", "2025-01-10",
		`,"destroyed":true,"manifest_id":"APP-1"`))
	entryA1 := `{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}`
	manDay10 := func(appText string) string { return man(appText, "2025-01-10", entryA1) }

	corrupt := map[string]struct {
		state    string
		wantAll  []string
		wantKind string
	}{
		"挂在APP-1名下内容编号写成APP-2": {
			state(a1Destroyed, entry("APP-1", manDay10(`"APP-2"`))),
			[]string{"APP-1", "APP-2"}, "不一致",
		},
		"application_id字段缺失": {
			// 清册记录只以 processed_on 开头，没有 application_id 字段；
			// 档案归属仍指向 APP-1，处理日期与条目快照都合法。
			state(a1Destroyed, entry("APP-1",
				`{"processed_on":"2025-01-10","entries":[`+entryA1+`]}`)),
			[]string{"APP-1", "缺少"}, "缺少",
		},
		"application_id为null": {
			state(a1Destroyed, entry("APP-1", manDay10(`null`))),
			[]string{"APP-1", "缺少"}, "缺少",
		},
		"application_id为空串": {
			state(a1Destroyed, entry("APP-1", manDay10(`""`))),
			[]string{"APP-1", "缺少"}, "缺少",
		},
		"application_id只有空白": {
			state(a1Destroyed, entry("APP-1", manDay10(`"   "`))),
			[]string{"APP-1", "缺少"}, "缺少",
		},
		"application_id只有制表符与换行": {
			state(a1Destroyed, entry("APP-1", manDay10(`"\t\n "`))),
			[]string{"APP-1", "缺少"}, "缺少",
		},
		"仅大小写不同不算一致": {
			state(a1Destroyed, entry("APP-1", manDay10(`"app-1"`))),
			[]string{"APP-1", "app-1"}, "不一致",
		},
		"仅首尾空白不同不算一致": {
			state(a1Destroyed, entry("APP-1", manDay10(`" APP-1 "`))),
			[]string{"APP-1", " APP-1 "}, "不一致",
		},
		"内容编号通过Unicode转义写成另一个编号": {
			// 键直接写 APP-1；application_id 的源文本是 APP- 紧接 2 的 Unicode
			// 转义，JSON 解码后为 APP-2，按解码后文本比较即不一致。
			state(a1Destroyed, entry("APP-1", manDay10(`"`+escApp2+`"`))),
			[]string{"APP-1", "APP-2"}, "不一致",
		},
		"期限修订完整仍因申请编号矛盾拒绝": {
			// A-1 已经过一次合法修订，截止日延长到 2026-01-10，清册条目快照、
			// 处理日期、归属关系与修订链全部合法，只有清册内容编号写成 APP-2。
			state(
				entry("A-1", `{"id":"A-1","category":"合同","start":"2020-01-01","end":"2026-01-10",`+
					`"initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1",`+
					`"freezes":[],"revisions":[{"id":"R-1","old_end":"2025-01-10",`+
					`"new_end":"2026-01-10","revised_on":"2024-12-01","reason":"延期"}]}`),
				entry("APP-1", man(`"APP-2"`, "2026-01-10",
					`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2026-01-10"}`))),
			[]string{"APP-1", "APP-2"}, "不一致",
		},
		"用于查找的键为纯空白": {
			// 归属仍指向 APP-1（该清册合法），另挂一份空白键的清册即拒绝。
			state(a1Destroyed,
				entry("APP-1", manDay10(`"APP-1"`))+","+
					entry(" ", man(`"APP-X"`, "2025-01-10", ""))),
			[]string{"空白"}, "空白",
		},
		"用于查找的键为空串": {
			state(a1Destroyed,
				entry("APP-1", manDay10(`"APP-1"`))+","+
					entry("", man(`"APP-X"`, "2025-01-10", ""))),
			[]string{"空白"}, "空白",
		},
		"多份清册中另一份身份矛盾": {
			state(
				a1Destroyed+","+
					entry("A-2", arc("A-2", "凭证", "2025-06-30",
						`,"destroyed":true,"manifest_id":"APP-2"`)),
				entry("APP-1", manDay10(`"APP-1"`))+","+
					entry("APP-2", man(`"APP-9"`, "2025-06-30",
						`{"id":"A-2","category":"凭证","start":"2020-01-01","end":"2025-06-30"}`))),
			[]string{"APP-2", "APP-9"}, "不一致",
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
	// Unicode 转义、不同申请编号各对应一份清册、尚无清册的空库。
	t.Run("身份一致的清册对照组", func(t *testing.T) {
		a2Destroyed := entry("A-2", arc("A-2", "凭证", "2025-06-30",
			`,"destroyed":true,"manifest_id":"APP-2"`))
		entryA2 := `{"id":"A-2","category":"凭证","start":"2020-01-01","end":"2025-06-30"}`
		valid := map[string]string{
			"键与内容编号直接写出且相同": state(
				a1Destroyed, entry("APP-1", manDay10(`"APP-1"`))),
			"键用Unicode转义内容编号直接写出": state(
				a1Destroyed, entry(escApp1, manDay10(`"APP-1"`))),
			"键直接写出内容编号用Unicode转义": state(
				a1Destroyed, entry("APP-1", manDay10(`"`+escApp1+`"`))),
			"两侧都用Unicode转义解码后相同": state(
				a1Destroyed, entry(escApp1, manDay10(`"`+escApp1+`"`))),
			"不同申请编号各对应一份清册不受影响": state(
				a1Destroyed+","+a2Destroyed,
				entry("APP-1", manDay10(`"APP-1"`))+","+
					entry("APP-2", man(`"APP-2"`, "2025-06-30", entryA2))),
			"manifests为null按空集合": `{"version":1,"archives":{},"manifests":null}`,
			"尚无清册的合法空库":          `{"version":1,"archives":{},"manifests":{}}`,
		}
		for name, content := range valid {
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				writeStateFile(t, dir, content)
				s, err := Open(dir)
				if err != nil {
					t.Fatalf("身份一致的清册应能打开: %v", err)
				}
				defer s.Close()
			})
		}
	})
}

// 保管库打开后保存内容才出现清册申请编号矛盾：下一次使用有效输入查询历史、
// 取回清册、销毁前核对或办理业务，即使操作的是另一份正常档案或正常清册，
// 也必须按整库记录损坏失败——不返回正常历史、清册或部分报告，不写入业务
// 变更，原保存内容保持原样；恢复一致后原清册继续可取回、可重放。
func TestManifestApplicationIdentityMismatchAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")
	reg(t, s, "A-3", "单据", "2020-01-01", "2025-01-10")
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-3", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-3"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetManifest("APP-1"); err != nil || !found {
		t.Fatalf("损坏前清册应可取回: found=%v err=%v", found, err)
	}

	// 把 APP-1 清册内容里的 application_id 改成 APP-2：清册仍挂在 APP-1
	// 名下（档案归属也仍指向 APP-1），内容却自称 APP-2。
	good := readStateFile(t, dir)
	rewriteState(t, dir, func(doc map[string]any) {
		doc["manifests"].(map[string]any)["APP-1"].(map[string]any)["application_id"] = "APP-2"
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误说明清册申请编号不一致并给出两处各自的编号。
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
	// 正常清册 APP-3，也必须整库失败。
	if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后查询无关档案也必须失败且无结论: found=%v err=%v", found, err)
	}
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后查询矛盾档案不得返回标着 APP-2 的清册: found=%v err=%v", found, err)
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
		t.Fatalf("损坏后核对必须失败且不得给出部分报告: %+v err=%v", r, err)
	}
	// 关键回归点：损坏状态下重放 APP-1 绝不能取回标着 APP-2 的清册，
	// 也不能给出“可以取回原清册”的结论。
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || r.Manifest != nil {
		t.Fatalf("损坏后对 APP-1 的核对也必须失败，不能附标着 APP-2 的清册: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 APP-1 的幂等重放也必须失败且不得返回清册: %+v err=%v", m, err)
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
		t.Fatalf("损坏后 Release 必须失败: %v", err)
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-9", ArchiveID: "A-2",
		OriginalEnd: MustParseDate("2025-06-30"), NewEnd: MustParseDate("2026-06-30"),
		RevisedOn: MustParseDate("2025-06-01"), Reason: "延期",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Revise 必须失败: %v", err)
	}

	// 失败不得触发修复：不用查找编号补写清册、不以清册内容改号、不删除冲突
	// 记录，原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 恢复一致后整库恢复可用：APP-1 仍是原来那份清册，历史查询、按编号取回
	// 与相同申请重放都得到申请编号 APP-1 的原内容。
	writeStateFile(t, dir, good)
	m, found, err := s.GetManifest("APP-1")
	if err != nil || !found || m.ApplicationID != "APP-1" {
		t.Fatalf("恢复后清册应可取回且编号为 APP-1: found=%v err=%v %+v", found, err, m)
	}
	h, found, err := s.History("A-1")
	if err != nil || !found || h.Manifest == nil || h.Manifest.ApplicationID != "APP-1" {
		t.Fatalf("恢复后 A-1 历史中的清册应标着 APP-1: found=%v err=%v %+v", found, err, h.Manifest)
	}
	replay, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil || replay.ApplicationID != "APP-1" {
		t.Fatalf("恢复后相同日期与集合的重放应取回原清册 APP-1: %+v err=%v", replay, err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-11"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrApplicationMismatch) {
		t.Fatalf("恢复后沿用编号改变日期仍应按编号冲突失败，得到 %v", err)
	}
}

// 保管库打开后清册内容的 application_id 才缺失：与编号不一致一样按整库记录
// 损坏失败，下一次有效输入的任何操作都不返回正常结果，即使操作的是另一份
// 正常档案或正常清册；错误指出缺的是清册内容中的申请编号及对应清册记录。
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
		!strings.Contains(err.Error(), "APP-1") || !strings.Contains(err.Error(), "缺少") {
		t.Fatalf("应报缺少有效申请编号并指出对应清册记录 APP-1，得到 %v", err)
	}
	if m, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("application_id 缺失后取回该清册必须失败: found=%v err=%v %+v", found, err, m)
	}
	if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("application_id 缺失后查询另一份正常档案也必须失败: found=%v err=%v", found, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" {
		t.Fatalf("application_id 缺失后核对必须整次失败: %+v err=%v", r, err)
	}
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}
