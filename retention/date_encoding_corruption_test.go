package retention

import (
	"errors"
	"strings"
	"testing"
)

// 保存记录中的日期字符串按 JSON 解码后的实际文本判断：合法日期写成普通
// 字符与 \uXXXX Unicode 转义混用的形式（题目例：\u0032025-01-10 与 2025-01-10）
// 必须与普通写法等价——保管库正常打开，历史与清册可查，到期判断（截止日
// 当天即到期）不因转义写法变化；本库重新保存后输出仍为规范普通写法。
func TestUnicodeEscapedDatesInSavedState(t *testing.T) {
	// 起算日、最初截止日与当前截止日均用转义混写，解码后分别为
	// 2020-01-01 与 2025-01-10。
	content := `{"version":1,"archives":{"A-1":{` +
		`"id":"A-1","category":"合同",` +
		`"start":"` + u2 + `0` + u2 + `0-01-01",` +
		`"end":"2025-01-1` + u0 + `",` +
		`"initial_end":"2025` + uDash + `01` + uDash + `10",` +
		`"destroyed":false,"freezes":[]}},"manifests":{}}`

	dir := t.TempDir()
	writeStateFile(t, dir, content)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Unicode 转义写出的合法日期应能打开: %v", err)
	}
	defer s.Close()

	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("转义保存的档案历史应可查: found=%v err=%v", found, err)
	}
	if !h.Start.Equal(MustParseDate("2020-01-01")) ||
		!h.RetentionEnd.Equal(MustParseDate("2025-01-10")) ||
		!h.InitialEnd.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("转义写法解析出的日期不符: %+v", h)
	}

	// 到期判断与普通写法一致：截止日当天即到期，前一天未到期。
	r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-09"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil {
		t.Fatalf("核对不应报错: %v", err)
	}
	if r.Status != CheckBlocked || r.Results[0].Obstructions[0].Kind != ObstructionNotExpired {
		t.Fatalf("截止日前一天应判未到期: %+v", r)
	}
	r, err = s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil {
		t.Fatalf("核对不应报错: %v", err)
	}
	if r.Status != CheckReady {
		t.Fatalf("截止日当天应判到期可办理: %+v", r)
	}

	// 正式办理销毁：清册正常生成、可按申请编号取回，处理日期同样用转义保存
	// 也不受影响。
	m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil {
		t.Fatalf("转义保存的合法日期不应阻止销毁: %v", err)
	}
	if len(m.Entries) != 1 || m.Entries[0].End.String() != "2025-01-10" {
		t.Fatalf("清册条目日期应为规范日期: %+v", m.Entries)
	}
	got, found, err := s.GetManifest("APP-1")
	if err != nil || !found || got.ProcessedOn.String() != "2025-01-10" {
		t.Fatalf("清册应可查且处理日期正确: found=%v err=%v %+v", found, err, got)
	}

	// 本库办理后重新保存并再次打开：落盘为普通写法，重新读取结论不变。
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("重新打开应成功: %v", err)
	}
	defer s2.Close()
	h2, found, err := s2.History("A-1")
	if err != nil || !found || !h2.Destroyed || h2.ManifestApplicationID != "APP-1" {
		t.Fatalf("重开后历史应完整: found=%v err=%v %+v", found, err, h2)
	}
	if strings.Contains(readStateFile(t, dir), `\u`) {
		t.Fatalf("本库保存应输出规范普通写法，不应残留 Unicode 转义")
	}
}

// 保存记录中填写了非法日期时（明文或转义后出现加号、数字等非字符串值），
// 打开必须按 ErrCorruptState 失败，原保存内容保持原样；这些文本曾经要么
// 被当成普通日期接受（"+1"），要么让整个合法保管库被判损坏（合法 \u 转义）。
func TestSavedInvalidDatesAreCorrupt(t *testing.T) {
	archive := func(start, end string) string {
		return `{"version":1,"archives":{"A-1":{` +
			`"id":"A-1","category":"合同","start":"` + start + `","end":"` + end +
			`","initial_end":"` + end + `","destroyed":false,"freezes":[]}},"manifests":{}}`
	}
	corrupt := map[string]string{
		"明文月份加号":   archive("2020-01-01", "2025-+1-10"),
		"明文日期加号":   archive("2020-01-01", "2025-01-+1"),
		"转义后月份加号":  archive("2020-01-01", "2025"+uDash+uPlus+"1-10"),
		"转义后日期加号":  archive("2020-01-01", "2025-01-"+uPlus+"1"),
		"转义后首部空白":  archive("2020-01-01", uSpace+"2025-01-10"),
		"起算日明文加号":  archive("2020-+1-01", "2025-01-10"),
		"起算日转义加号":  archive("2020-01"+uDash+uPlus+"01", "2025-01-10"),
		"非闰年2月29日": archive("2020-01-01", "2023-02-29"),
		"1900年闰日":  archive("2020-01-01", "1900-02-29"),
	}
	for name, content := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, content)
			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("保存了非法日期的保管库不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("应可判定为 ErrCorruptState，得到 %v", err)
			}
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 非字符串 JSON 值（数字、null、对象）同样判损坏，且 null 必填日期不会
	// 被零值顶替。
	nonString := map[string]string{
		"截止日为数字":   `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":20250110,"initial_end":20250110,"destroyed":false,"freezes":[]}},"manifests":{}}`,
		"截止日为null": `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":null,"initial_end":"2025-01-10","destroyed":false,"freezes":[]}},"manifests":{}}`,
		"截止日为对象":   `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":{"date":"2025-01-10"},"initial_end":"2025-01-10","destroyed":false,"freezes":[]}},"manifests":{}}`,
	}
	for name, content := range nonString {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, content)
			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("非字符串日期值不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("应可判定为 ErrCorruptState，得到 %v", err)
			}
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}
}

// 修订与冻结记录中的日期同样按解码后实际文本校验：转义写出的合法日期正常
// 可读；其中混入加号等非法文本时整库损坏。
func TestSavedInvalidDatesInRevisionsAndFreezes(t *testing.T) {
	validRev := `{"id":"R-1","old_end":"2025` + uDash + `01-10","new_end":"2026-01-10","revised_on":"2025-01-05","reason":"延期"}`
	good := `{"version":1,"archives":{"A-1":{` +
		`"id":"A-1","category":"合同","start":"2020-01-01","end":"2026-01-10",` +
		`"initial_end":"` + u2 + `025-01-10","destroyed":false,"freezes":[],` +
		`"revisions":[` + validRev + `]}},"manifests":{}}`
	dir := t.TempDir()
	writeStateFile(t, dir, good)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("转义合法日期的修订记录应能打开: %v", err)
	}
	h, found, err := s.History("A-1")
	if err != nil || !found || len(h.Revisions) != 1 ||
		!h.Revisions[0].OldEnd.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("转义保存的修订历史应完整可查: found=%v err=%v %+v", found, err, h)
	}
	s.Close()

	// 冻结日期转义后出现加号：即使其余内容合法，也按整库损坏。
	badFreeze := `{"version":1,"archives":{"A-1":{` +
		`"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10",` +
		`"initial_end":"2025-01-10","destroyed":false,` +
		`"freezes":[{"id":"F-1","reason":"诉讼","frozen_on":"2025-01` + uDash + uPlus + `05","released":false}]}},"manifests":{}}`
	dir2 := t.TempDir()
	writeStateFile(t, dir2, badFreeze)
	s2, err := Open(dir2)
	if err == nil {
		s2.Close()
		t.Fatal("冻结日期非法时不应打开成功")
	}
	if !errors.Is(err, ErrCorruptState) {
		t.Fatalf("应可判定为 ErrCorruptState，得到 %v", err)
	}
}

// 已关闭清册的处理日期、条目快照日期以及已解除冻结的冻结/解除日期同样以
// 解码后的实际文本判断：全部用 Unicode 转义混写的一份已销毁档案记录必须
// 正常打开，历史与清册完整可查，且处理日期仍按“截止日当天即到期”核对。
func TestUnicodeEscapedDatesInManifestAndReleasedFreeze(t *testing.T) {
	content := `{"version":1,"archives":{"A-1":{` +
		`"id":"A-1","category":"合同",` +
		`"start":"` + u2 + `020-01-01",` +
		`"end":"2025-01-10","initial_end":"2025-01-10",` +
		`"destroyed":true,"manifest_id":"APP-1",` +
		`"freezes":[{"id":"F-1","reason":"诉讼",` +
		`"frozen_on":"2025-01-05","released":true,` +
		`"release_reason":"结案","released_on":"2025-01` + uDash + `06"}]}},` +
		`"manifests":{"APP-1":{"application_id":"APP-1",` +
		`"processed_on":"2025` + uDash + `01` + uDash + `10",` +
		`"entries":[{"id":"A-1","category":"合同",` +
		`"start":"2020-01-01","end":"` + u2 + `025-01-10"}]}}}`
	dir := t.TempDir()
	writeStateFile(t, dir, content)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("转义写出的已销毁档案与清册应能打开: %v", err)
	}
	defer s.Close()

	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("历史应可查: found=%v err=%v", found, err)
	}
	if !h.Destroyed || h.ManifestApplicationID != "APP-1" || h.Manifest == nil {
		t.Fatalf("销毁状态与清册归属应完整: %+v", h)
	}
	if !h.Freezes[0].FrozenOn.Equal(MustParseDate("2025-01-05")) ||
		!h.Freezes[0].ReleasedOn.Equal(MustParseDate("2025-01-06")) {
		t.Fatalf("冻结/解除日期应按解码文本解析: %+v", h.Freezes)
	}
	if !h.Manifest.ProcessedOn.Equal(MustParseDate("2025-01-10")) ||
		!h.Manifest.Entries[0].End.Equal(MustParseDate("2025-01-10")) ||
		!h.Manifest.Entries[0].Start.Equal(MustParseDate("2020-01-01")) {
		t.Fatalf("清册日期应按解码文本解析: %+v", h.Manifest)
	}
	m, found, err := s.GetManifest("APP-1")
	if err != nil || !found || m.ApplicationID != "APP-1" {
		t.Fatalf("清册应可按申请编号取回: found=%v err=%v", found, err)
	}
}

// 缺省与 null 的既有含义不变：未解除冻结缺解除日期合法；已解除冻结把解除
// 日期保存为 null 仍按“缺少解除日期”的语义损坏报告，而不是 JSON 解析错误
// 或被零值顶替。必填日期字段缺失仍按既有的缺项损坏规则处理。
func TestOptionalDateNullSemanticsUnchanged(t *testing.T) {
	// 未解除冻结没有 released_on：合法，可正常打开。
	okContent := `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false}]}},"manifests":{}}`
	dir := t.TempDir()
	writeStateFile(t, dir, okContent)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("未解除冻结缺解除日期本就合法: %v", err)
	}
	h, found, err := s.History("A-1")
	if err != nil || !found || len(h.ActiveFreezes) != 1 {
		t.Fatalf("未解除冻结应正常可查: found=%v err=%v", found, err)
	}
	s.Close()

	// 已解除冻结却把 released_on 写成 null：语义校验报缺少解除日期。
	nullContent := `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":null}]}},"manifests":{}}`
	dir2 := t.TempDir()
	writeStateFile(t, dir2, nullContent)
	s2, err := Open(dir2)
	if err == nil {
		s2.Close()
		t.Fatal("已解除冻结解除日期为 null 时应判损坏")
	}
	if !errors.Is(err, ErrCorruptState) || !strings.Contains(err.Error(), "解除日期") {
		t.Fatalf("应按缺少解除日期报 ErrCorruptState，得到 %v", err)
	}
}

// 保管库打开后保存内容才被改成非法日期：下一次查询、核对与办理都必须按
// ErrCorruptState 失败，不返回正常记录、不产生业务变更，原保存内容原样
// 保留——与其他保存损坏的行为一致。
func TestInvalidDateAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "B-1", "凭证", "2020-01-01", "2025-01-10")

	// 直接改写状态文件，把 A-1 的截止日写成曾经会被接受的带加号文本。
	rewriteState(t, dir, func(doc map[string]any) {
		a1 := doc["archives"].(map[string]any)["A-1"].(map[string]any)
		a1["end"] = "2025-+1-10"
	})
	corruptContent := readStateFile(t, dir)

	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("保存非法日期后重新打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) {
		t.Fatalf("应为 ErrCorruptState，得到 %v", err)
	}

	// 已打开实例上的后续读取与办理全部失败，即使操作的是正常的 B-1。
	if h, found, err := s.History("B-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后查询正常档案也必须失败: found=%v err=%v", found, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"B-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" {
		t.Fatalf("损坏后核对必须失败: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"B-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后办理必须失败且不生成清册: %+v err=%v", m, err)
	}
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}
