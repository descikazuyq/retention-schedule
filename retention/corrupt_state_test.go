package retention

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// 已解除冻结缺少解除日期或原因、解除日期早于冻结日期时，
// 即使 JSON 本身能解析，Open 也必须按记录损坏处理：返回 ErrCorruptState，
// 错误信息能定位到档案与冻结编号，原文件保持原样。
func TestOpenRejectsIncompleteReleaseRecords(t *testing.T) {
	valid := `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","freezes":[{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-06"}]}},"manifests":{}}`
	corrupt := map[string]string{
		"缺解除原因字段":    `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","freezes":[{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":true,"released_on":"2025-01-06"}]}},"manifests":{}}`,
		"解除原因只有空白":   `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","freezes":[{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"  \t ","released_on":"2025-01-06"}]}},"manifests":{}}`,
		"解除原因为空串":    `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","freezes":[{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"","released_on":"2025-01-06"}]}},"manifests":{}}`,
		"缺解除日期字段":    `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","freezes":[{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案"}]}},"manifests":{}}`,
		"解除日期为 null": `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","freezes":[{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":null}]}},"manifests":{}}`,
		"解除日期早于冻结日期": `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","freezes":[{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-04"}]}},"manifests":{}}`,
	}
	for name, content := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, content)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("解除记录不完整时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "A-1") || !strings.Contains(msg, "F-1") {
				t.Fatalf("错误信息应指出档案与冻结编号，得到 %v", err)
			}
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动: %q -> %q", content, got)
			}
		})
	}

	// 对照组：内容完整的已解除记录（含旧格式缺 initial_end）必须能正常打开，
	// 防止把合法记录误判成损坏。
	t.Run("完整解除记录可打开", func(t *testing.T) {
		dir := t.TempDir()
		writeStateFile(t, dir, valid)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("完整的解除记录应能打开: %v", err)
		}
		defer s.Close()
	})
	t.Run("旧格式记录中的完整解除可打开", func(t *testing.T) {
		legacy := `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","freezes":[{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-06"}]}},"manifests":{}}`
		dir := t.TempDir()
		writeStateFile(t, dir, legacy)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("旧格式但解除信息完整的记录应能打开: %v", err)
		}
		defer s.Close()
	})
}

// 未解除冻结没有解除日期和原因是合法状态，仍要继续阻止销毁；
// 已解除冻结的解除日期与冻结日期相同也合法，历史保留完整信息且不再阻碍。
func TestValidFreezeRecordsKeepWorking(t *testing.T) {
	t.Run("未解除冻结合法且阻止销毁", func(t *testing.T) {
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
		r, err := s.Check(CheckRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
		})
		if err != nil {
			t.Fatalf("未解除冻结是合法记录，核对不应报错: %v", err)
		}
		if r.Status != CheckBlocked || len(r.Results[0].Obstructions) != 1 ||
			r.Results[0].Obstructions[0].Kind != ObstructionActiveFreeze {
			t.Fatalf("未解除冻结应给出一条冻结阻碍: %+v", r)
		}
		if _, err := s.Destroy(DestructionRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
		}); !errors.Is(err, ErrActiveFreeze) {
			t.Fatalf("未解除冻结应阻止销毁，得到 %v", err)
		}
	})

	t.Run("解除日期与冻结日期相同合法", func(t *testing.T) {
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
		if err := s.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-05")}); err != nil {
			t.Fatalf("解除日期与冻结日期相同应合法: %v", err)
		}
		h, found, err := s.History("A-1")
		if err != nil || !found {
			t.Fatalf("历史查询失败: found=%v err=%v", found, err)
		}
		if len(h.Freezes) != 1 || !h.Freezes[0].Released ||
			h.Freezes[0].ReleaseReason != "结案" ||
			!h.Freezes[0].ReleasedOn.Equal(MustParseDate("2025-01-05")) ||
			len(h.ActiveFreezes) != 0 {
			t.Fatalf("历史应保留完整解除信息且不再算作未解除: %+v", h.Freezes)
		}
		r, err := s.Check(CheckRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
		})
		if err != nil {
			t.Fatalf("核对不应报错: %v", err)
		}
		if r.Status != CheckReady {
			t.Fatalf("已解除冻结不应阻碍销毁: %+v", r)
		}
		if _, err := s.Destroy(DestructionRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
		}); err != nil {
			t.Fatalf("到期且无未解除冻结时应能销毁: %v", err)
		}
	})
}

// rewriteState 解析已保存状态、按 fn 修改后原样写回（保持 JSON 合法），
// 用于模拟保管库打开之后保存内容被改成语义损坏的记录。
func rewriteState(t *testing.T, dir string, fn func(map[string]any)) {
	t.Helper()
	path := filepath.Join(dir, stateFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	fn(doc)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// 保管库打开后保存内容才出现不完整解除记录：下一次使用有效输入的
// 历史查询、清册查询、销毁前核对与正式销毁都必须返回 ErrCorruptState，
// 不能把档案说成不存在、不能给出可以办理或部分报告、不能生成清册，
// 且失败后原保存内容保持原样。
func TestIncompleteReleaseAfterOpenFailsAllOperations(t *testing.T) {
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
	if _, found, err := s.History("A-1"); err != nil || !found {
		t.Fatalf("损坏前查询应成功: found=%v err=%v", found, err)
	}

	// 把解除原因改空：只剩“已解除”标记。
	rewriteState(t, dir, func(doc map[string]any) {
		freezes := doc["archives"].(map[string]any)["A-1"].(map[string]any)["freezes"].([]any)
		freezes[0].(map[string]any)["release_reason"] = "   "
	})
	corruptContent := readStateFile(t, dir)

	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History 必须返回 ErrCorruptState 且无结论: found=%v err=%v", found, err)
	}
	if _, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("损坏后 GetManifest 必须返回 ErrCorruptState: found=%v err=%v", found, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出部分报告: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
	}
	if err := s.Register(RegisterInput{ID: "A-2", Category: "凭证", Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-01-10")}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Register 必须失败: %v", err)
	}
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-2", Reason: "审计", FrozenOn: MustParseDate("2025-01-07")}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Freeze 必须失败: %v", err)
	}

	// 失败不得触发重新保存：损坏内容原样保留，不补原因、不删冻结。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}

// 损坏判断适用于库内全部已解除冻结，与本次办理名单无关：
// 异常记录挂在另一份已经销毁的档案下时，查询、核对、销毁其他档案
// 同样必须按记录损坏失败，不能把不完整解除信息作为正常历史返回。
func TestCorruptReleaseOnDestroyedArchiveFailsWholeVault(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "D-1", "凭证", "2020-01-01", "2025-01-10")
	if err := s.Freeze(FreezeInput{ArchiveID: "D-1", FreezeID: "F-D", Reason: "协查", FrozenOn: MustParseDate("2025-01-05")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ReleaseInput{ArchiveID: "D-1", FreezeID: "F-D", Reason: "协查结束", ReleasedOn: MustParseDate("2025-01-06")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-D", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"D-1"},
	}); err != nil {
		t.Fatal(err)
	}

	// 损坏发生在已销毁档案 D-1 身上（去掉解除日期）。
	rewriteState(t, dir, func(doc map[string]any) {
		freezes := doc["archives"].(map[string]any)["D-1"].(map[string]any)["freezes"].([]any)
		delete(freezes[0].(map[string]any), "released_on")
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开整个位置必须失败，错误指出 D-1 与 F-D。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("库内任一已解除冻结不完整时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) || !strings.Contains(err.Error(), "D-1") || !strings.Contains(err.Error(), "F-D") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出 D-1/F-D，得到 %v", err)
	}

	// 已打开的实例：即使本次只查询、核对、销毁与异常记录无关的 A-1，
	// 也必须按整库损坏失败。
	if _, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("查询无关档案 A-1 也应返回 ErrCorruptState: found=%v err=%v", found, err)
	}
	if _, found, err := s.History("D-1"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("查询已销毁的异常档案不得返回（不完整的）历史: found=%v err=%v", found, err)
	}
	if _, found, err := s.GetManifest("APP-D"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("查询异常档案所属清册也应失败: found=%v err=%v", found, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" {
		t.Fatalf("核对无关档案也应整次失败: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("不得在损坏状态下生成清册: %+v err=%v", m, err)
	}
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}

// validDestroyedState 是一份合法的“档案已销毁、清册已关闭”状态 JSON，
// 各损坏用例在它的基础上改动。
const validDestroyedState = `{
  "version": 1,
  "archives": {
    "A-1": {
      "id": "A-1",
      "category": "合同",
      "start": "2020-01-01",
      "end": "2025-01-10",
      "initial_end": "2025-01-10",
      "destroyed": true,
      "manifest_id": "APP-1",
      "freezes": []
    }
  },
  "manifests": {
    "APP-1": {
      "application_id": "APP-1",
      "processed_on": "2025-01-10",
      "entries": [
        {"id": "A-1", "category": "合同", "start": "2020-01-01", "end": "2025-01-10"}
      ]
    }
  }
}`

// inconsistentStateCase 描述一种销毁状态与关闭清册不能相互对应的损坏记录，
// want 是错误信息中必须出现的编号（档案编号与相关申请编号）。
type inconsistentStateCase struct {
	name string
	json string
	want []string
}

func inconsistentStateCases() []inconsistentStateCase {
	entryA1 := `{"id": "A-1", "category": "合同", "start": "2020-01-01", "end": "2025-01-10"}`
	manifestAPP1 := `"APP-1": {"application_id": "APP-1", "processed_on": "2025-01-10", "entries": [` + entryA1 + `]}`
	return []inconsistentStateCase{
		{
			name: "清册仍保留但档案被标成未销毁",
			json: `{
  "version": 1,
  "archives": {
    "A-1": {"id": "A-1", "category": "合同", "start": "2020-01-01", "end": "2025-01-10", "initial_end": "2025-01-10", "destroyed": false, "freezes": []}
  },
  "manifests": {` + manifestAPP1 + `}
}`,
			want: []string{"A-1", "APP-1"},
		},
		{
			name: "已销毁档案的所属清册缺失",
			json: `{
  "version": 1,
  "archives": {
    "A-1": {"id": "A-1", "category": "合同", "start": "2020-01-01", "end": "2025-01-10", "initial_end": "2025-01-10", "destroyed": true, "manifest_id": "APP-1", "freezes": []}
  },
  "manifests": {}
}`,
			want: []string{"A-1", "APP-1"},
		},
		{
			name: "已销毁档案没有申请编号",
			json: `{
  "version": 1,
  "archives": {
    "A-1": {"id": "A-1", "category": "合同", "start": "2020-01-01", "end": "2025-01-10", "initial_end": "2025-01-10", "destroyed": true, "freezes": []}
  },
  "manifests": {}
}`,
			want: []string{"A-1"},
		},
		{
			name: "所属清册未收录该档案",
			json: `{
  "version": 1,
  "archives": {
    "A-1": {"id": "A-1", "category": "合同", "start": "2020-01-01", "end": "2025-01-10", "initial_end": "2025-01-10", "destroyed": true, "manifest_id": "APP-1", "freezes": []}
  },
  "manifests": {
    "APP-1": {"application_id": "APP-1", "processed_on": "2025-01-10", "entries": []}
  }
}`,
			want: []string{"A-1", "APP-1"},
		},
		{
			name: "同一档案在一份清册中出现两次",
			json: `{
  "version": 1,
  "archives": {
    "A-1": {"id": "A-1", "category": "合同", "start": "2020-01-01", "end": "2025-01-10", "initial_end": "2025-01-10", "destroyed": true, "manifest_id": "APP-1", "freezes": []}
  },
  "manifests": {
    "APP-1": {"application_id": "APP-1", "processed_on": "2025-01-10", "entries": [` + entryA1 + `, ` + entryA1 + `]}
  }
}`,
			want: []string{"A-1", "APP-1"},
		},
		{
			name: "同一档案出现在两份不同清册中",
			json: `{
  "version": 1,
  "archives": {
    "A-1": {"id": "A-1", "category": "合同", "start": "2020-01-01", "end": "2025-01-10", "initial_end": "2025-01-10", "destroyed": true, "manifest_id": "APP-1", "freezes": []}
  },
  "manifests": {
    "APP-1": {"application_id": "APP-1", "processed_on": "2025-01-10", "entries": [` + entryA1 + `]},
    "APP-2": {"application_id": "APP-2", "processed_on": "2025-01-11", "entries": [` + entryA1 + `]}
  }
}`,
			want: []string{"A-1", "APP-1", "APP-2"},
		},
		{
			name: "档案归属指向别的申请编号",
			json: `{
  "version": 1,
  "archives": {
    "A-1": {"id": "A-1", "category": "合同", "start": "2020-01-01", "end": "2025-01-10", "initial_end": "2025-01-10", "destroyed": true, "manifest_id": "APP-2", "freezes": []}
  },
  "manifests": {` + manifestAPP1 + `}
}`,
			want: []string{"A-1", "APP-2"},
		},
		{
			name: "清册收录了不存在的档案",
			json: `{
  "version": 1,
  "archives": {},
  "manifests": {` + manifestAPP1 + `}
}`,
			want: []string{"A-1", "APP-1"},
		},
		{
			name: "未销毁档案仍挂有清册申请编号",
			json: `{
  "version": 1,
  "archives": {
    "A-1": {"id": "A-1", "category": "合同", "start": "2020-01-01", "end": "2025-01-10", "initial_end": "2025-01-10", "destroyed": false, "manifest_id": "APP-1", "freezes": []}
  },
  "manifests": {` + manifestAPP1 + `}
}`,
			want: []string{"A-1", "APP-1"},
		},
	}
}

// 已保存的销毁状态与关闭清册不能相互对应时，即使 JSON 本身能解析，
// Open 也必须按记录损坏处理：返回 ErrCorruptState，错误信息指出涉及的
// 档案编号与相关申请编号，原文件保持原样，不返回可继续办理的保管库。
func TestOpenRejectsInconsistentDestructionRecords(t *testing.T) {
	for _, tc := range inconsistentStateCases() {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.json)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("销毁状态与清册不一致时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			for _, want := range tc.want {
				if !strings.Contains(msg, want) {
					t.Fatalf("错误信息应包含编号 %q，得到 %v", want, err)
				}
			}
			if got := readStateFile(t, dir); got != tc.json {
				t.Fatalf("打开失败后原文件被改动")
			}
		})
	}

	// 对照组：合法销毁记录必须正常打开，历史与原清册完整可查，
	// 相同申请再次提交仍取回原清册；引入期限修订前的合法记录
	// （无 initial_end、无 revisions）也不能仅因缺这些字段被拒绝。
	t.Run("合法销毁记录可打开且可重放", func(t *testing.T) {
		dir := t.TempDir()
		writeStateFile(t, dir, validDestroyedState)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("合法销毁记录应能打开: %v", err)
		}
		defer s.Close()
		h, found, err := s.History("A-1")
		if err != nil || !found || !h.Destroyed || h.ManifestApplicationID != "APP-1" || h.Manifest == nil {
			t.Fatalf("合法销毁历史应完整可查: found=%v err=%v h=%+v", found, err, h)
		}
		m, found, err := s.GetManifest("APP-1")
		if err != nil || !found || len(m.Entries) != 1 || m.Entries[0].ID != "A-1" {
			t.Fatalf("原清册应可取回: found=%v err=%v m=%+v", found, err, m)
		}
		again, err := s.Destroy(DestructionRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
		})
		if err != nil || again.ApplicationID != "APP-1" {
			t.Fatalf("相同申请再次提交应取回原清册: %+v err=%v", again, err)
		}
	})
	t.Run("旧格式合法销毁记录可打开", func(t *testing.T) {
		legacy := `{
  "version": 1,
  "archives": {
    "A-1": {"id": "A-1", "category": "合同", "start": "2020-01-01", "end": "2025-01-10", "destroyed": true, "manifest_id": "APP-1", "freezes": []}
  },
  "manifests": {
    "APP-1": {"application_id": "APP-1", "processed_on": "2025-01-10", "entries": [{"id": "A-1", "category": "合同", "start": "2020-01-01", "end": "2025-01-10"}]}
  }
}`
		dir := t.TempDir()
		writeStateFile(t, dir, legacy)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("旧格式合法销毁记录应能打开: %v", err)
		}
		defer s.Close()
		h, found, err := s.History("A-1")
		if err != nil || !found || !h.Destroyed || h.Manifest == nil ||
			!h.InitialEnd.Equal(MustParseDate("2025-01-10")) {
			t.Fatalf("旧格式销毁历史应完整: found=%v err=%v h=%+v", found, err, h)
		}
	})
}

// 保管库打开后保存内容才出现销毁状态与清册不一致（清册仍在，档案却被改成
// 未销毁）：下一次使用有效输入的历史查询、清册取回、销毁前核对与正式办理
// 都必须返回 ErrCorruptState，即使本次操作的是另一份无关档案；不能返回
// 正常历史、清册或部分核对报告，不能用另一申请编号生成第二份清册，
// 且失败后原保存内容保持原样。恢复合法内容后原清册仍可重放。
func TestInconsistentDestructionAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatal(err)
	}
	original := readStateFile(t, dir)

	// 损坏：清册 APP-1 仍收录 A-1，但 A-1 被改成未销毁并摘掉归属编号。
	rewriteState(t, dir, func(doc map[string]any) {
		a1 := doc["archives"].(map[string]any)["A-1"].(map[string]any)
		a1["destroyed"] = false
		delete(a1, "manifest_id")
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开整个位置必须失败，错误指出 A-1 与 APP-1。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("销毁关系损坏时打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "A-1") || !strings.Contains(err.Error(), "APP-1") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出 A-1/APP-1，得到 %v", err)
	}

	// 已打开的实例：不得把被改成未销毁的 A-1 重新判为可以销毁。
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History(A-1) 必须失败且无结论: found=%v err=%v", found, err)
	}
	// 即使本次只操作另一份无关档案 A-2，也必须按整库损坏失败。
	if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("查询无关档案 A-2 也应失败: found=%v err=%v", found, err)
	}
	if m, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("取回原清册也必须失败: found=%v m=%+v err=%v", found, m, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏档案不得被判为可以办理: %+v err=%v", r, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("核对无关档案也必须整次失败且无部分报告: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("不得用另一申请编号生成第二份清册: %+v err=%v", m, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("办理无关档案也必须失败且不生成清册: %+v err=%v", m, err)
	}
	if err := s.Register(RegisterInput{ID: "A-3", Category: "凭证", Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-01-10")}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Register 必须失败: %v", err)
	}
	if err := s.Freeze(FreezeInput{ArchiveID: "A-2", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Freeze 必须失败: %v", err)
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "REV-1", ArchiveID: "A-2",
		OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2026-01-10"),
		RevisedOn: MustParseDate("2025-01-07"), Reason: "延长",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Revise 必须失败: %v", err)
	}

	// 所有失败都不得触发重新保存：损坏内容原样保留，
	// 不自动补清册、改销毁标记或删掉冲突记录。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 恢复合法内容：原清册与销毁历史完整可查，相同申请再次提交取回原清册；
	// 失败过的 APP-2 没有留下任何东西，可正常用于销毁 A-2。
	writeStateFile(t, dir, original)
	h, found, err := s.History("A-1")
	if err != nil || !found || !h.Destroyed || h.Manifest == nil {
		t.Fatalf("恢复后 A-1 的销毁历史应完整: found=%v err=%v h=%+v", found, err, h)
	}
	if again, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil || again.ApplicationID != "APP-1" || len(again.Entries) != 1 {
		t.Fatalf("恢复后相同申请应取回原清册: %+v err=%v", again, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-2"},
	}); err != nil || m.ApplicationID != "APP-2" {
		t.Fatalf("恢复后失败过的申请编号应仍可正常办理: %+v err=%v", m, err)
	}
}
