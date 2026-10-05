package retention

import (
	"errors"
	"strings"
	"testing"
)

// 保存的清册集合中同一申请编号出现两份清册时，即使 JSON 本身能解析，Open
// 也必须按保存记录损坏失败（ErrCorruptState）：错误指出重复的申请编号并
// 说明该编号对应多份清册，原文件保持原样。普通解码会让后一份清册静默覆盖
// 前一份——只要后一份符合档案归属、期限与处理日期规则，保管库仍能打开，
// 取回的清册随保存顺序改变。已成功的销毁申请只能对应一份已关闭清册，绝不
// 能接受这种覆盖，也不能把重复保存当成正常的申请重试。
func TestOpenRejectsDuplicateManifestApplicationID(t *testing.T) {
	// destroyedArchive 构造一份已销毁并指回指定申请的档案，截止日为 end。
	destroyedArchive := func(id, app, end string) string {
		return `"` + id + `":{"id":"` + id + `","category":"合同","start":"2020-01-01",` +
			`"end":"` + end + `","initial_end":"` + end + `","destroyed":true,` +
			`"manifest_id":"` + app + `","freezes":[]}`
	}
	entry := `{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}`
	// manifest 构造一份已关闭清册：处理日期 processed，收录 A-1。
	manifest := func(app, processed string) string {
		return `{"application_id":"` + app + `","processed_on":"` + processed +
			`","entries":[` + entry + `]}`
	}
	kv := func(key, val string) string { return `"` + key + `":` + val }
	state := func(archives, manifests string) string {
		return `{"version":1,"archives":{` + archives + `},"manifests":{` + manifests + `}}`
	}
	a1 := destroyedArchive("A-1", "APP-1", "2025-01-10")
	appDay10 := manifest("APP-1", "2025-01-10")
	appDay11 := manifest("APP-1", "2025-01-11")
	// escApp1 是 JSON 文本里的字面 Unicode 转义（反斜杠+u0031），解码后
	// 得到字符 1，即与 "APP-1" 完全相同的申请编号。
	escApp1 := "APP-\\u0031"

	corrupt := map[string]string{
		"当天处理在前次日处理在后": state(
			a1, kv("APP-1", appDay10)+","+kv("APP-1", appDay11)),
		"交换保存顺序仍然拒绝": state(
			a1, kv("APP-1", appDay11)+","+kv("APP-1", appDay10)),
		"两份清册完全一致": state(
			a1, kv("APP-1", appDay10)+","+kv("APP-1", appDay10)),
		"Unicode转义键与直接键同编号": state(
			a1, kv("APP-1", appDay10)+","+kv(escApp1, appDay11)),
		"多份清册中第二份编号重复": func() string {
			// A-2 归 APP-2（截止日 2025-06-30），APP-2 出现两次；不同申请
			// 各自一份本属合法，但 APP-2 重复即在解码阶段判整库损坏。
			entry2 := `{"id":"A-2","category":"合同","start":"2020-01-01","end":"2025-06-30"}`
			app2 := func(processed string) string {
				return `{"application_id":"APP-2","processed_on":"` + processed +
					`","entries":[` + entry2 + `]}`
			}
			return state(
				a1+","+destroyedArchive("A-2", "APP-2", "2025-06-30"),
				kv("APP-1", appDay10)+","+
					kv("APP-2", app2("2025-06-30"))+","+
					kv("APP-2", app2("2025-07-01")))
		}(),
	}
	for name, content := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, content)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("同一申请编号对应两份清册时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			wantID := "APP-1"
			if strings.HasPrefix(name, "多份清册") {
				wantID = "APP-2"
			}
			msg := err.Error()
			if !strings.Contains(msg, wantID) {
				t.Fatalf("错误信息应指出重复的申请编号 %q，得到 %v", wantID, err)
			}
			if !strings.Contains(msg, "多份") || !strings.Contains(msg, "清册") {
				t.Fatalf("错误信息应说明该编号对应多份清册，得到 %v", err)
			}
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：不同申请编号各自对应一份合法清册继续正常读取——即使每份清册
	// 内部都带处理日期、条目和档案编号等同名字段，那只是清册记录的内部字段，
	// 不是清册集合的重复键；尚无清册的合法空库与 manifests 为 null 也继续合法。
	t.Run("合法记录对照组", func(t *testing.T) {
		entry2 := `{"id":"A-2","category":"合同","start":"2020-01-01","end":"2025-06-30"}`
		app2 := `{"application_id":"APP-2","processed_on":"2025-06-30","entries":[` + entry2 + `]}`
		valid := map[string]string{
			"不同申请编号各一份清册且内部字段同名": state(
				a1+","+destroyedArchive("A-2", "APP-2", "2025-06-30"),
				kv("APP-1", appDay10)+","+kv("APP-2", app2)),
			// 第二个申请编号以 Unicode 转义写出（反斜杠+u0032 解码为字符 2），
			// 解码后是 APP-2，与 APP-1 不同编号，仍是合法记录。
			"转义键解码后仍是另一个申请编号": state(
				a1+","+destroyedArchive("A-2", "APP-2", "2025-06-30"),
				kv("APP-1", appDay10)+","+kv("APP-\\u0032", app2)),
			"没有清册的合法空库":         `{"version":1,"archives":{},"manifests":{}}`,
			"manifests为null按空库": `{"version":1,"archives":{},"manifests":null}`,
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

// 保管库打开后保存内容才出现同一申请编号的重复清册：下一次使用有效输入
// 查询历史、取回清册（含已成功申请的幂等重放）、销毁前核对与各项办理都
// 必须按整库记录损坏失败——即使操作的是另一份正常档案——不返回正常历史、
// 清册或部分核对报告，不产生销毁或其他业务变更，原保存内容保持原样。
func TestDuplicateManifestAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")
	// A-1 在截止日当天成功销毁，生成 APP-1 清册。
	orig, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if m, found, err := s.GetManifest("APP-1"); err != nil || !found ||
		!m.ProcessedOn.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("损坏前清册应可取回: found=%v err=%v %+v", found, err, m)
	}

	// 在清册集合中为 APP-1 再写入一份次日（2025-01-11）处理的清册：两份收录
	// 相同档案、处理日期不同，且两个日期都满足到期规则。普通解码只会剩后
	// 一份，取回的清册会随保存顺序变成次日处理的版本。
	good := readStateFile(t, dir)
	dupRecord := `,
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
    }`
	// APP-1 是清册集合的最后一条记录，其记录对象结束后紧跟清册集合与根对象
	// 的结束括号；在该处插入第二份同编号清册。
	marker := "    }\n  }\n}"
	if !strings.Contains(good, marker) {
		t.Fatal("未找到清册集合的结束位置，测试夹具失效")
	}
	corrupt := strings.Replace(good, marker, "    }"+dupRecord+"\n  }\n}", 1)
	if corrupt == good {
		t.Fatal("重复清册未写入，测试夹具失效")
	}
	writeStateFile(t, dir, corrupt)
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误指出重复编号 APP-1 并说明对应多份清册。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("清册集合出现同编号重复时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "APP-1") ||
		!strings.Contains(err.Error(), "多份") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出重复的 APP-1 对应多份清册，得到 %v", err)
	}

	// 已打开实例：即使本次只操作与重复清册无关的正常档案 A-2，
	// 也必须按整库损坏失败。
	if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后查询无关档案也必须失败且无结论: found=%v err=%v", found, err)
	}
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History 必须失败且不得给出销毁历史: found=%v err=%v", found, err)
	}
	if m, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("损坏后 GetManifest 必须失败，不能退回次日处理的后一份清册: found=%v err=%v %+v",
			found, err, m)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出结论或部分报告: %+v err=%v", r, err)
	}
	// 关键回归点：损坏状态下已成功申请的“幂等重放”也不能取回任何一份清册。
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || r.Manifest != nil {
		t.Fatalf("损坏后对 APP-1 的核对必须失败，不能误报可以取回原清册: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后已成功申请的重放也必须失败且不得返回任一份重复清册: %+v err=%v", m, err)
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

	// 失败不得触发修复：不合并重复清册，不删除、改号或重新生成清册，
	// 原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 去掉重复清册后，合法记录恢复可用：相同日期和档案集合的成功申请重试
	// 仍返回唯一的原清册（2025-01-10 当天处理的那份），不会受重复版本影响；
	// 无关档案业务不受影响。
	writeStateFile(t, dir, good)
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("恢复后应能重新打开: %v", err)
	}
	defer reopened.Close()
	m, found, err := s.GetManifest("APP-1")
	if err != nil || !found {
		t.Fatalf("恢复后清册应可取回: found=%v err=%v", found, err)
	}
	if !m.ProcessedOn.Equal(MustParseDate("2025-01-10")) || len(m.Entries) != 1 || m.Entries[0].ID != "A-1" {
		t.Fatalf("取回的应是当天处理的唯一原清册: %+v", m)
	}
	replay, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil || !replay.ProcessedOn.Equal(orig.ProcessedOn) || len(replay.Entries) != len(orig.Entries) {
		t.Fatalf("恢复后同日期同集合重试应返回唯一原清册: %+v err=%v", replay, err)
	}
	// 改变日期仍按已有的申请编号冲突规则处理。
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-11"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrApplicationMismatch) {
		t.Fatalf("恢复后改变日期仍应按申请编号冲突处理，得到 %v", err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); err != nil {
		t.Fatalf("恢复后无关档案应能正常办理销毁: %v", err)
	}
}
