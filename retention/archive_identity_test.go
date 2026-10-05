package retention

import (
	"errors"
	"strings"
	"testing"
)

// esc1/esc2 的源码文本是 A- 紧接数字的 Unicode 转义（JSON 解码后分别为
// A-1、A-2），用于验证身份比较以 JSON 解码后的实际文本为准。
const (
	esc1 = "A-\\u0031"
	esc2 = "A-\\u0032"
)

// 保存的档案集合中，用于查找每份档案的编号（对象键）与该份登记内容里的 id
// 必须都是非空白文本，且是同一个编号。两处编号不同，或登记内容的 id 缺失、
// 为 null、为空串、只有空白时，即使 JSON 能解析、其余期限/冻结信息都合法，
// Open 也必须按保存记录损坏失败（ErrCorruptState），错误能区分“编号不一致”
// 与“缺少有效编号”，且原文件保持原样。
func TestOpenRejectsArchiveIdentityMismatch(t *testing.T) {
	// active 与 released 两条冻结记录都合法，供对照“合法冻结不能掩盖编号矛盾”。
	activeFreeze := `{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false}`
	releasedFreeze := `{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-06"}`
	// rec 构造一份登记记录；idText 给出记录内容里 id 字段的原始 JSON 文本，
	// 允许写出 null、空串、Unicode 转义等特殊形式。
	rec := func(idText, category, end, freezes string) string {
		return `{"id":` + idText + `,"category":"` + category + `","start":"2020-01-01","end":"` + end +
			`","initial_end":"` + end + `","destroyed":false,"freezes":[` + freezes + `]}`
	}
	entry := func(key, recordJSON string) string { return `"` + key + `":` + recordJSON }
	state := func(entries string) string {
		return `{"version":1,"archives":{` + entries + `},"manifests":{}}`
	}

	corrupt := map[string]struct {
		state    string
		wantAll  []string
		wantKind string
	}{
		"挂在A-1名下id写成A-2": {
			state(entry("A-1", rec(`"A-2"`, "合同", "2025-01-10", ""))),
			[]string{"A-1", "A-2"}, "不一致",
		},
		"id字段缺失": {
			// 去掉 id 字段：记录直接以 category 开头。
			`{"version":1,"archives":{"A-1":` +
				`{"category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[]}` +
				`},"manifests":{}}`,
			[]string{"A-1", "id"}, "缺少",
		},
		"id为null": {
			state(entry("A-1", rec(`null`, "合同", "2025-01-10", ""))),
			[]string{"A-1", "id"}, "缺少",
		},
		"id为空串": {
			state(entry("A-1", rec(`""`, "合同", "2025-01-10", ""))),
			[]string{"A-1", "id"}, "缺少",
		},
		"id只有空白": {
			state(entry("A-1", rec(`"   "`, "合同", "2025-01-10", ""))),
			[]string{"A-1", "id"}, "缺少",
		},
		"id只有制表符与换行": {
			state(entry("A-1", rec(`"\t\n "`, "合同", "2025-01-10", ""))),
			[]string{"A-1", "id"}, "缺少",
		},
		"仅大小写不同不算一致": {
			state(entry("A-1", rec(`"a-1"`, "合同", "2025-01-10", ""))),
			[]string{"A-1", "a-1"}, "不一致",
		},
		"仅首尾空白不同不算一致": {
			state(entry("A-1", rec(`" A-1 "`, "合同", "2025-01-10", ""))),
			[]string{"A-1", " A-1 "}, "不一致",
		},
		"id通过Unicode转义写成另一个编号": {
			// 键直接写 A-1；id 的源文本是 A- 紧接 2 的 Unicode 转义，
			// JSON 解码后为 A-2，按解码后文本比较即不一致。
			state(entry("A-1", rec(`"`+esc2+`"`, "合同", "2025-01-10", ""))),
			[]string{"A-1", "A-2"}, "不一致",
		},
		"带未解除冻结且期限合法仍因编号矛盾拒绝": {
			state(entry("A-1", rec(`"A-2"`, "合同", "2025-01-10", activeFreeze))),
			[]string{"A-1", "A-2"}, "不一致",
		},
		"用于查找的键为纯空白": {
			state(entry(" ", rec(`"A-1"`, "合同", "2025-01-10", ""))),
			[]string{"空白"}, "空白",
		},
		"用于查找的键为空串": {
			`{"version":1,"archives":{"":` + rec(`"A-1"`, "合同", "2025-01-10", "") +
				`},"manifests":{}}`,
			[]string{"空白"}, "空白",
		},
		"多份档案中第二份身份矛盾": {
			state(entry("A-1", rec(`"A-1"`, "合同", "2025-01-10", "")) + "," +
				entry("B-7", rec(`"B-8"`, "凭证", "2025-06-30", ""))),
			[]string{"B-7", "B-8"}, "不一致",
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("档案编号与查找编号不一致时不应打开成功")
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

	// 已销毁档案：清册归属、条目快照、处理日期、解除信息都合法，只有登记内容
	// 的 id 与查找编号不一致——合法的清册归属不能掩盖编号矛盾。
	t.Run("已销毁档案身份矛盾同样拒绝", func(t *testing.T) {
		content := `{"version":1,"archives":{"A-1":` +
			`{"id":"A-2","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1","freezes":[` +
			releasedFreeze + `]}},"manifests":{"APP-1":` +
			`{"application_id":"APP-1","processed_on":"2025-01-10","entries":[` +
			`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}]}}}`
		dir := t.TempDir()
		writeStateFile(t, dir, content)
		s, err := Open(dir)
		if err == nil {
			s.Close()
			t.Fatal("已销毁档案的编号矛盾不能被合法清册掩盖")
		}
		if !errors.Is(err, ErrCorruptState) ||
			!strings.Contains(err.Error(), "A-1") || !strings.Contains(err.Error(), "A-2") ||
			!strings.Contains(err.Error(), "不一致") {
			t.Fatalf("应报档案编号不一致并给出两处编号，得到 %v", err)
		}
		if got := readStateFile(t, dir); got != content {
			t.Fatalf("打开失败后原文件被改动:\n%q", got)
		}
	})

	// 对照组：键与 id 解码后为同一文本即合法——包括两侧分别使用 Unicode 转义、
	// 正常保存的带冻结档案、没有修订信息的旧档案。
	t.Run("身份一致的记录对照组", func(t *testing.T) {
		valid := map[string]string{
			"键与id直接写出且相同": state(
				entry("A-1", rec(`"A-1"`, "合同", "2025-01-10", activeFreeze))),
			"键用Unicode转义id直接写出": state(
				entry(esc1, rec(`"A-1"`, "合同", "2025-01-10", activeFreeze))),
			"键直接写出id用Unicode转义": state(
				entry("A-1", rec(`"`+esc1+`"`, "合同", "2025-01-10", ""))),
			"两侧都用Unicode转义解码后相同": state(
				entry(esc1, rec(`"`+esc1+`"`, "合同", "2025-01-10", ""))),
			"不同编号各登记一次不受影响": state(
				entry("A-1", rec(`"A-1"`, "合同", "2025-01-10", activeFreeze)) + "," +
					entry("A-2", rec(`"A-2"`, "合同", "2025-01-10", activeFreeze))),
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
					t.Fatalf("身份一致的记录应能打开: %v", err)
				}
				defer s.Close()
			})
		}
	})
}

// 保管库打开后保存内容才出现档案编号矛盾：下一次使用有效输入查询历史、
// 取回清册、销毁前核对或办理业务，即使操作的是另一份正常档案，也必须按
// 整库记录损坏失败——不返回正常历史、清册或部分报告，不改变档案状态或
// 生成清册，原保存内容保持原样；恢复一致后记录继续可用。
func TestArchiveIdentityMismatchAfterOpenFailsAllOperations(t *testing.T) {
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
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-3", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-3"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetManifest("APP-3"); err != nil || !found {
		t.Fatalf("损坏前清册应可取回: found=%v err=%v", found, err)
	}

	// 把 A-1 登记内容里的 id 改成 A-2：记录仍挂在 A-1 名下，却自称 A-2。
	good := readStateFile(t, dir)
	rewriteState(t, dir, func(doc map[string]any) {
		doc["archives"].(map[string]any)["A-1"].(map[string]any)["id"] = "A-2"
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误说明档案编号不一致并给出两处编号。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("档案编号与查找编号不一致时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "A-1") || !strings.Contains(err.Error(), "A-2") ||
		!strings.Contains(err.Error(), "不一致") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出两处编号不一致，得到 %v", err)
	}

	// 已打开实例：即使本次只操作与矛盾记录无关的正常档案 A-2，也必须整库失败。
	if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后查询无关档案也必须失败且无结论: found=%v err=%v", found, err)
	}
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后查询矛盾档案不得返回标着 A-2 的历史: found=%v err=%v", found, err)
	}
	if m, found, err := s.GetManifest("APP-3"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("损坏后取回清册必须失败: found=%v err=%v %+v", found, err, m)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后核对必须失败且不得给出部分报告: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后销毁必须失败且不得把错误编号写进清册: %+v err=%v", m, err)
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

	// 失败不得触发修复：不改号、不补写，原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 恢复一致后整库恢复可用：A-1 的身份与冻结历史仍是损坏前的样子。
	writeStateFile(t, dir, good)
	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("恢复后查询应成功: found=%v err=%v", found, err)
	}
	if h.ID != "A-1" || len(h.Freezes) != 1 || h.Freezes[0].ID != "F-1" || h.Freezes[0].Released {
		t.Fatalf("恢复后 A-1 身份与冻结历史应保持原样: %+v", h)
	}
	if _, found, err := s.GetManifest("APP-3"); err != nil || !found {
		t.Fatalf("恢复后已销毁档案的清册应保持可查: found=%v err=%v", found, err)
	}
}

// 保管库打开后登记内容的 id 才缺失：与编号不一致一样按整库记录损坏失败，
// 下一次有效输入的任何操作都不返回正常结果，即使操作的是另一份正常档案。
func TestArchiveIdentityMissingIDAfterOpenFailsWholeVault(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")

	rewriteState(t, dir, func(doc map[string]any) {
		delete(doc["archives"].(map[string]any)["A-1"].(map[string]any), "id")
	})
	corruptContent := readStateFile(t, dir)

	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("登记内容缺少 id 时打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "A-1") || !strings.Contains(err.Error(), "缺少") {
		t.Fatalf("应报缺少有效编号并指出查找编号 A-1，得到 %v", err)
	}
	if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("id 缺失后查询另一份正常档案也必须失败: found=%v err=%v", found, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" {
		t.Fatalf("id 缺失后核对必须整次失败: %+v err=%v", r, err)
	}
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}
