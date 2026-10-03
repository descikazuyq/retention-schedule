package retention

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeState 直接写入状态文件内容，用于构造损坏或历史格式的记录。
func writeState(t *testing.T, dir string, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, stateFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readState(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// 状态文件不存在时允许打开空库并正常登记。
func TestOpenMissingStateFile(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("文件不存在应能打开空库: %v", err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
}

// 正常保存的空库对象仍然有效，不能因没有档案或清册而拒绝打开。
func TestOpenEmptyVaultObject(t *testing.T) {
	for _, content := range []string{
		`{"version":1,"archives":{},"manifests":{}}`,
		// 正常对象前后的空白、换行可以接受。
		"  \n\t {\"version\":1,\"archives\":{},\"manifests\":{}} \n\n",
	} {
		dir := t.TempDir()
		writeState(t, dir, content)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("合法空库对象应能打开: %q: %v", content, err)
		}
		reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
		s.Close()
	}
}

// 已存在但内容损坏的状态文件：打开必须失败，且能用 ErrStateCorrupt 识别。
func TestOpenCorruptStateFile(t *testing.T) {
	valid := `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","destroyed":false,"freezes":null}},"manifests":{}}`
	cases := map[string]string{
		"零字节文件":         "",
		"只有空白":          "  \n\t \n",
		"内容为 null":      "null",
		"非对象 JSON":      `["not","an","object"]`,
		"无法解析的文字":       "这不是 JSON",
		"对象后拼接第二份 JSON": valid + ` {"version":1,"archives":{},"manifests":{}}`,
		"对象后夹有不能解析的文字":  valid + " 后半段不是 JSON",
		"对象被截断":         valid[:len(valid)/2],
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeState(t, dir, content)
			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatalf("损坏内容不应打开成功")
			}
			if !errors.Is(err, ErrStateCorrupt) {
				t.Fatalf("错误应可识别为状态损坏: %v", err)
			}
			// 打开失败后原文件内容必须保持原样。
			if got := readState(t, dir); got != content {
				t.Fatalf("打开失败不应改动原文件: 原为 %q，现为 %q", content, got)
			}
		})
	}
}

// 保管库打开后文件变成损坏内容：后续查询与办理都必须明确失败，
// 且不得返回部分结果，原文件保持原样。
func TestCorruptionAfterOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-2"},
	}); err != nil {
		t.Fatal(err)
	}

	// 损坏前查询成功，用于确认之后不会拿旧结果充当本次结果。
	if _, found, err := s.History("A-1"); err != nil || !found {
		t.Fatalf("损坏前 A-1 应可查: %v %v", found, err)
	}

	// 在合法对象后面拼接第二份内容，前一段本身仍可解析。
	corrupt := readState(t, dir) + "\n" + `{"version":1,"archives":{},"manifests":{}}`
	writeState(t, dir, corrupt)

	// 查询不得给出档案不存在、空历史或可以销毁之类的正常结论。
	if _, found, err := s.History("A-1"); !errors.Is(err, ErrStateCorrupt) || found {
		t.Fatalf("损坏后 History 应明确失败: found=%v err=%v", found, err)
	}
	if _, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrStateCorrupt) || found {
		t.Fatalf("损坏后 GetManifest 应明确失败: found=%v err=%v", found, err)
	}
	r, err := s.Check(CheckRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	})
	if !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("损坏后 Check 应明确失败: %+v err=%v", r, err)
	}
	if r.Status != "" || r.Manifest != nil || len(r.Results) != 0 {
		t.Fatalf("失败的核对不得附带部分报告或清册: %+v", r)
	}

	// 办理不得新增档案、改动期限、解除冻结或生成清册。
	if err := s.Register(RegisterInput{
		ID: "A-3", Category: "合同",
		Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-01-10"),
	}); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("损坏后 Register 应明确失败: %v", err)
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-1", FreezeID: "F-2", Reason: "审计", FrozenOn: MustParseDate("2025-01-06"),
	}); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("损坏后 Freeze 应明确失败: %v", err)
	}
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-06"),
	}); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("损坏后 Release 应明确失败: %v", err)
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-1", ArchiveID: "A-1",
		OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
		RevisedOn: MustParseDate("2025-01-06"), Reason: "延期",
	}); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("损坏后 Revise 应明确失败: %v", err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("损坏后 Destroy 应明确失败: %v", err)
	}

	// 原文件内容必须保持原样：没有清空、修补、截尾或重新保存，
	// 失败的销毁申请也没有留下清册或占用编号（文件未变即未占用）。
	if got := readState(t, dir); got != corrupt {
		t.Fatalf("读取失败后原文件不应被改动:\n原为 %q\n现为 %q", corrupt, got)
	}
}

// 引入期限修订之前保存的有效记录（没有 initial_end 字段）仍可使用，
// 缺少当时尚未提供的修订信息不能被当作损坏。
func TestOpenPreRevisionStateFile(t *testing.T) {
	dir := t.TempDir()
	writeState(t, dir, `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","destroyed":false,"freezes":null}},"manifests":{}}`)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("修订功能前的记录应能打开: %v", err)
	}
	defer s.Close()
	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("旧记录应可查: %v %v", found, err)
	}
	if !h.InitialEnd.Equal(MustParseDate("2025-01-10")) || !h.RetentionEnd.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("旧记录的最初截止日应取登记截止日: %+v", h)
	}
}
