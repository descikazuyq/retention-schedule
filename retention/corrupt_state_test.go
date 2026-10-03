package retention

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeStateFile(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, stateFileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readStateFile(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 已存在的状态文件内容不完整或不合法时，Open 必须明确失败，
// 调用者能借此区分“尚未建立记录”和“记录文件已经损坏”，且原文件保持原样。
func TestOpenRejectsCorruptStateFile(t *testing.T) {
	corrupt := map[string]string{
		"零字节文件":      "",
		"只有空白":       "  \n\t \n",
		"内容为 null":   "null",
		"null 带空白":   " \n null \n",
		"对象后拼接第二个值":  `{"version":1,"archives":{},"manifests":{}} {"version":1}`,
		"对象后拼接 null": `{"version":1,"archives":{},"manifests":{}} null`,
		"对象后夹非法文字":   `{"version":1,"archives":{},"manifests":{}} 这不是合法JSON`,
		"半截 JSON":    `{"version":1,"archives":{`,
		"顶层不是对象":     `[{"version":1}]`,
	}
	for name, content := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, content)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatalf("损坏内容 %q 不应打开成功", content)
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动: %q -> %q", content, got)
			}
		})
	}
}

// 状态文件不存在时按空库打开，可以正常登记。
func TestOpenMissingStateFileOpensEmptyStore(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("状态文件不存在时应能打开空库: %v", err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	if _, found, err := s.History("A-1"); err != nil || !found {
		t.Fatalf("登记后应能查询: found=%v err=%v", found, err)
	}
}

// 正常保存的空库对象（没有档案或清册）仍然有效；对象前后允许空白。
func TestOpenAcceptsEmptyStoreObjectWithWhitespace(t *testing.T) {
	dir := t.TempDir()
	writeStateFile(t, dir, " \n {\"version\":1,\"archives\":{},\"manifests\":{}} \n\n")

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("空库对象应能打开: %v", err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
}

// 保管库打开后文件才损坏：下一次查询与办理都必须明确失败，
// 不能给出正常结论，也不能改动原文件。
func TestCorruptionAfterOpenFailsSubsequentOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-06")}); err != nil {
		t.Fatal(err)
	}
	// 此前查询成功，调用者手里持有正常结果。
	if _, found, err := s.History("A-1"); err != nil || !found {
		t.Fatalf("前置查询应成功: found=%v err=%v", found, err)
	}

	original := readStateFile(t, dir)
	// 合法对象后面拼接无法解析的文字：即使前一段能读出档案，整份文件也判为损坏。
	writeStateFile(t, dir, original+"\n 这不是合法JSON")

	// 查询不得给出档案不存在、空历史或可以销毁之类的正常结论。
	if _, found, err := s.History("A-1"); err == nil || found {
		t.Fatalf("损坏后 History 应失败且不得给出结论: found=%v err=%v", found, err)
	}
	if _, found, err := s.GetManifest("APP-1"); err == nil || found {
		t.Fatalf("损坏后 GetManifest 应失败且不得给出结论: found=%v err=%v", found, err)
	}
	if _, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err == nil {
		t.Fatal("损坏后 Check 应失败，不得给出可以办理等结论")
	}

	// 办理不得新增档案、改动期限、解除冻结或生成清册。
	if err := s.Register(RegisterInput{ID: "A-2", Category: "凭证", Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-01-10")}); err == nil {
		t.Fatal("损坏后 Register 应失败")
	}
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-2", Reason: "审计", FrozenOn: MustParseDate("2025-01-07")}); err == nil {
		t.Fatal("损坏后 Freeze 应失败")
	}
	if err := s.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-07")}); err == nil {
		t.Fatal("损坏后 Release 应失败")
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "REV-1", ArchiveID: "A-1",
		OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
		RevisedOn: MustParseDate("2025-01-07"), Reason: "延期",
	}); err == nil {
		t.Fatal("损坏后 Revise 应失败")
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err == nil {
		t.Fatal("损坏后 Destroy 应失败")
	}

	// 原文件保持原样：不得自动清空、修补、截去尾部或重新保存。
	if got := readStateFile(t, dir); got != original+"\n 这不是合法JSON" {
		t.Fatalf("读取失败后原文件被改动:\n%q", got)
	}

	// 恢复合法内容后：失败的销毁申请没有留下清册，申请编号仍可正常使用。
	writeStateFile(t, dir, original)
	if _, found, err := s.GetManifest("APP-1"); err != nil || found {
		t.Fatalf("失败的销毁申请不应留下清册: found=%v err=%v", found, err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatalf("恢复后同一申请编号应能正常办理: %v", err)
	}
}

// 引入期限修订之前保存的有效记录（没有 initial_end、revisions 字段）
// 不能被当作损坏，最初截止日按登记截止日补齐。
func TestOpenLegacyStateWithoutRevisionFields(t *testing.T) {
	dir := t.TempDir()
	writeStateFile(t, dir, `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","destroyed":false,"freezes":[]}},"manifests":{}}`)

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("修订功能之前保存的记录应能打开: %v", err)
	}
	defer s.Close()
	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("旧记录应能查询: found=%v err=%v", found, err)
	}
	if !h.InitialEnd.Equal(MustParseDate("2025-01-10")) || !h.RetentionEnd.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("缺少修订信息时最初截止日应取登记截止日: %+v", h)
	}
}
