package retention

import (
	"errors"
	"strings"
	"testing"
)

// 已关闭清册必须带有处理日期，且处理日期不能早于任一条目档案销毁时最终生效
// 并保存到清册里的截止日（截止日当天即到期）——与办理销毁时同一条规则。
// 缺少处理日期，或即使清册归属与条目快照都一致却在档案到期前处理，Open 都
// 必须按保存记录损坏失败（ErrCorruptState）：缺日期时指出申请编号，提前销毁
// 时同时指出申请编号、档案编号、处理日期与截止日，原文件保持原样。
func TestOpenRejectsManifestProcessedBeforeExpiry(t *testing.T) {
	archive := func(id, end, initial string, extra string) string {
		return `{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"` + end +
			`","initial_end":"` + initial + `","destroyed":true,"manifest_id":"APP-1","freezes":[]` + extra + `}`
	}
	entry := func(id, end string) string {
		return `{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"` + end + `"}`
	}
	manifest := func(appID, processedOn, entries string) string {
		return `"` + appID + `":{"application_id":"` + appID + `"` + processedOn +
			`,"entries":[` + entries + `]}`
	}
	state := func(archivesJSON, manifestJSON string) string {
		return `{"version":1,"archives":{` + archivesJSON + `},"manifests":{` + manifestJSON + `}}`
	}

	a1End0630 := archive("A-1", "2025-06-30", "2025-06-30", "")
	a2End0701 := archive("A-2", "2025-07-01", "2025-07-01", "")

	corrupt := map[string]struct {
		state   string
		wantAll []string // 错误信息必须全部包含
	}{
		"清册缺少处理日期字段": {
			state(`"A-1":`+a1End0630,
				manifest("APP-1", "", entry("A-1", "2025-06-30"))),
			[]string{"APP-1"},
		},
		"清册处理日期为 null": {
			state(`"A-1":`+a1End0630,
				`"APP-1":{"application_id":"APP-1","processed_on":null,"entries":[`+entry("A-1", "2025-06-30")+`]}`),
			[]string{"APP-1"},
		},
		"处理日期早于截止日一天": {
			state(`"A-1":`+a1End0630,
				manifest("APP-1", `,"processed_on":"2025-06-29"`, entry("A-1", "2025-06-30"))),
			[]string{"APP-1", "A-1", "2025-06-29", "2025-06-30"},
		},
		"多档案清册中第二份未到期": {
			// 题述例子：处理日期 2025-06-30，A-1 当天到期、A-2 截止 2025-07-01。
			// 两份档案都已销毁、指向该清册、条目与档案一致，仍必须整体拒绝，
			// 不能只返回 A-1 的正常记录或把 A-2 略过。
			state(`"A-1":`+a1End0630+`,"A-2":`+a2End0701,
				manifest("APP-1", `,"processed_on":"2025-06-30"`,
					entry("A-1", "2025-06-30")+`,`+entry("A-2", "2025-07-01"))),
			[]string{"APP-1", "A-2", "2025-06-30", "2025-07-01"},
		},
		"延期后按最初截止日提前销毁": {
			// 最终生效截止日为 2025-06-30：处理日期 2025-01-10 对最初期限刚好到期，
			// 对最终期限仍提前；必须按清册保存的最终截止日拒绝，不按最初期限放行。
			state(`"A-1":`+archive("A-1", "2025-06-30", "2025-01-10",
				`,"revisions":[{"id":"R-1","old_end":"2025-01-10","new_end":"2025-06-30","revised_on":"2025-01-09","reason":"延期"}]`),
				manifest("APP-1", `,"processed_on":"2025-01-10"`, entry("A-1", "2025-06-30"))),
			[]string{"APP-1", "A-1", "2025-01-10", "2025-06-30"},
		},
		"缩短后仍早于缩短后的截止日": {
			// 期限从 2025-06-30 缩短到 2025-01-10，处理日期 2025-01-09 仍提前。
			state(`"A-1":`+archive("A-1", "2025-01-10", "2025-06-30",
				`,"revisions":[{"id":"R-1","old_end":"2025-06-30","new_end":"2025-01-10","revised_on":"2024-06-01","reason":"缩短"}]`),
				manifest("APP-1", `,"processed_on":"2025-01-09"`, entry("A-1", "2025-01-10"))),
			[]string{"APP-1", "A-1", "2025-01-09", "2025-01-10"},
		},
		"改回早先用过的日期后仍提前一天": {
			// 2025-01-10 延到 2025-06-30 又改回 2025-01-10，最终生效为 2025-01-10，
			// 2025-01-09 处理仍属提前销毁，不按修订办理日期挑选期限。
			state(`"A-1":`+archive("A-1", "2025-01-10", "2025-01-10",
				`,"revisions":[`+
					`{"id":"R-1","old_end":"2025-01-10","new_end":"2025-06-30","revised_on":"2025-01-02","reason":"延期"},`+
					`{"id":"R-2","old_end":"2025-06-30","new_end":"2025-01-10","revised_on":"2025-01-03","reason":"改回"}]`),
				manifest("APP-1", `,"processed_on":"2025-01-09"`, entry("A-1", "2025-01-10"))),
			[]string{"APP-1", "A-1", "2025-01-09", "2025-01-10"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("清册缺处理日期或提前销毁时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			for _, want := range tc.wantAll {
				if !strings.Contains(msg, want) {
					t.Fatalf("错误信息应指出 %q，得到 %v", want, err)
				}
			}
			if name == "多档案清册中第二份未到期" {
				// 错误必须定位到未到期的第二份，而不是只提第一份。
				if strings.Count(msg, "A-1") > 0 {
					t.Fatalf("不应把已到期的 A-1 当作问题条目，得到 %v", err)
				}
			}
			if got := readStateFile(t, dir); got != tc.state {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}
}

// 对照组：处理日期等于截止日（当天到期）或晚于截止日的清册合法，多档案清册
// 按最晚一份的截止日判断也合法；没有修订历史的旧记录沿用既有兼容规则可打开。
func TestValidManifestProcessingKeepsWorking(t *testing.T) {
	archive := func(id, end string) string {
		return `{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"` + end +
			`","initial_end":"` + end + `","destroyed":true,"manifest_id":"APP-1","freezes":[]}`
	}
	entry := func(id, end string) string {
		return `{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"` + end + `"}`
	}
	valid := map[string]string{
		"截止日当天处理": `{"version":1,"archives":{"A-1":` + archive("A-1", "2025-06-30") +
			`},"manifests":{"APP-1":{"application_id":"APP-1","processed_on":"2025-06-30","entries":[` +
			entry("A-1", "2025-06-30") + `]}}}`,
		"截止日之后处理": `{"version":1,"archives":{"A-1":` + archive("A-1", "2025-06-30") +
			`},"manifests":{"APP-1":{"application_id":"APP-1","processed_on":"2025-07-15","entries":[` +
			entry("A-1", "2025-06-30") + `]}}}`,
		"多档案按最晚截止日当天处理": `{"version":1,"archives":{` +
			`"A-1":` + archive("A-1", "2025-06-30") + `,"A-2":` + archive("A-2", "2025-07-01") +
			`},"manifests":{"APP-1":{"application_id":"APP-1","processed_on":"2025-07-01","entries":[` +
			entry("A-1", "2025-06-30") + `,` + entry("A-2", "2025-07-01") + `]}}}`,
		"旧格式记录无修订字段仍可读取": `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-06-30","destroyed":true,"manifest_id":"APP-1","freezes":[]}},"manifests":{"APP-1":{"application_id":"APP-1","processed_on":"2025-06-30","entries":[{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-06-30"}]}}}`,
	}
	for name, content := range valid {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, content)
			s, err := Open(dir)
			if err != nil {
				t.Fatalf("合法处理日期的清册应能打开: %v", err)
			}
			defer s.Close()
			m, found, err := s.GetManifest("APP-1")
			if err != nil || !found {
				t.Fatalf("合法清册应可按申请编号取回: found=%v err=%v", found, err)
			}
			if m.ProcessedOn.IsZero() {
				t.Fatalf("取回的清册应带有处理日期: %+v", m)
			}
			if h, found, err := s.History("A-1"); err != nil || !found || h.Manifest == nil {
				t.Fatalf("合法记录的历史与清册应完整可查: found=%v err=%v %+v", found, err, h)
			}
		})
	}
}

// 通过正常 API 办理的合法销毁（含修订后于最终截止日当天或之后处理）重开后
// 历史、清册与相同申请取回行为保持不变。
func TestValidAPIFlowsWithProcessingDate(t *testing.T) {
	t.Run("最终截止日当天处理并重开可查", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
		if _, err := s.Revise(ReviseInput{
			RevisionID: "R-1", ArchiveID: "A-1",
			OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2025-06-30"),
			RevisedOn: MustParseDate("2025-01-09"), Reason: "延期",
		}); err != nil {
			t.Fatal(err)
		}
		req := DestructionRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-1"},
		}
		if _, err := s.Destroy(req); err != nil {
			t.Fatalf("最终截止日当天应能销毁: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		s2, err := Open(dir)
		if err != nil {
			t.Fatalf("合法记录重开应成功: %v", err)
		}
		defer s2.Close()
		m, found, err := s2.GetManifest("APP-1")
		if err != nil || !found || !m.ProcessedOn.Equal(MustParseDate("2025-06-30")) {
			t.Fatalf("重开后清册与处理日期应完整: found=%v err=%v %+v", found, err, m)
		}
		replay, err := s2.Destroy(req)
		if err != nil || replay.ApplicationID != "APP-1" ||
			!replay.ProcessedOn.Equal(MustParseDate("2025-06-30")) {
			t.Fatalf("相同申请应取回原清册: %+v err=%v", replay, err)
		}
	})

	t.Run("截止日之后处理并多档案销毁", func(t *testing.T) {
		s := openTestStore(t)
		reg(t, s, "A-1", "合同", "2020-01-01", "2025-06-30")
		reg(t, s, "A-2", "凭证", "2020-01-01", "2025-07-01")
		m, err := s.Destroy(DestructionRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-07-02"), ArchiveIDs: []string{"A-1", "A-2"},
		})
		if err != nil {
			t.Fatalf("两份档案均已到期时应能整体销毁: %v", err)
		}
		if !m.ProcessedOn.Equal(MustParseDate("2025-07-02")) || len(m.Entries) != 2 {
			t.Fatalf("清册内容不正确: %+v", m)
		}
	})
}

// 保管库打开后保存内容才出现清册处理日期问题：缺处理日期或处理日期被改成
// 早于截止日，下一次使用有效输入的查询历史、取回清册、销毁前核对与各项办理
// 都必须按整库记录损坏失败——即使操作的是不在该清册中的无关档案——不返回
// 正常结果或部分核对报告，不产生业务变更，原保存内容保持原样。
func TestManifestProcessingCorruptionAfterOpenFailsAllOperations(t *testing.T) {
	cases := map[string]func(map[string]any){
		"清册处理日期被删除": func(doc map[string]any) {
			delete(doc["manifests"].(map[string]any)["APP-1"].(map[string]any), "processed_on")
		},
		"清册处理日期被改成早于截止日": func(doc map[string]any) {
			doc["manifests"].(map[string]any)["APP-1"].(map[string]any)["processed_on"] = "2025-01-09"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
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
			if _, found, err := s.GetManifest("APP-1"); err != nil || !found {
				t.Fatalf("损坏前清册应可取回: found=%v err=%v", found, err)
			}

			rewriteState(t, dir, mutate)
			corruptContent := readStateFile(t, dir)

			// 重新打开必须失败，错误指出清册申请编号。
			s2, err := Open(dir)
			if err == nil {
				s2.Close()
				t.Fatal("处理日期出现问题时，打开必须失败")
			}
			if !errors.Is(err, ErrCorruptState) || !strings.Contains(err.Error(), "APP-1") {
				t.Fatalf("打开错误应为 ErrCorruptState 并指出 APP-1，得到 %v", err)
			}

			// 已打开实例：即使操作与该清册无关的 A-2，也必须按整库损坏失败。
			if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
				t.Fatalf("损坏后查询无关档案也必须失败且无结论: found=%v err=%v", found, err)
			}
			if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
				t.Fatalf("损坏后 History 必须失败且不得给出正常销毁历史: found=%v err=%v", found, err)
			}
			if m, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
				t.Fatalf("损坏后 GetManifest 必须失败: found=%v err=%v %+v", found, err, m)
			}
			if r, err := s.Check(CheckRequest{
				ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
			}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
				t.Fatalf("损坏后 Check 必须失败且不得给出部分报告: %+v err=%v", r, err)
			}
			if m, err := s.Destroy(DestructionRequest{
				ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
			}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
				t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
			}
			if err := s.Register(RegisterInput{
				ID: "A-3", Category: "凭证", Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-06-30"),
			}); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("损坏后 Register 必须失败: %v", err)
			}
			if err := s.Freeze(FreezeInput{
				ArchiveID: "A-2", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-06-01"),
			}); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("损坏后 Freeze 必须失败: %v", err)
			}
			if err := s.Release(ReleaseInput{
				ArchiveID: "A-2", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-06-02"),
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

			// 失败不得触发修复：不补处理日期、不改期限、销毁标记或清册条目。
			if got := readStateFile(t, dir); got != corruptContent {
				t.Fatalf("失败后原保存内容被改动:\n%q", got)
			}
		})
	}
}

// 提前销毁的损坏恢复为合法内容后，合法记录的登记、冻结解除、期限修订与申请
// 复用行为沿用现有功能，不被新校验误伤。
func TestRestoredValidStateAfterProcessingCorruptionWorks(t *testing.T) {
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

	// 改成提前销毁，确认整库不可用。
	rewriteState(t, dir, func(doc map[string]any) {
		doc["manifests"].(map[string]any)["APP-1"].(map[string]any)["processed_on"] = "2025-01-09"
	})
	if _, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("提前销毁状态下应整库损坏: found=%v err=%v", found, err)
	}

	// 恢复为合法处理日期（截止日当天）后，业务恢复正常。
	rewriteState(t, dir, func(doc map[string]any) {
		doc["manifests"].(map[string]any)["APP-1"].(map[string]any)["processed_on"] = "2025-01-10"
	})
	m, found, err := s.GetManifest("APP-1")
	if err != nil || !found || !m.ProcessedOn.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("恢复后清册应可取回: found=%v err=%v %+v", found, err, m)
	}
	// 相同申请仍按幂等取回原清册。
	replay, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil || replay.ApplicationID != "APP-1" {
		t.Fatalf("恢复后相同申请应取回原清册: %+v err=%v", replay, err)
	}
	// 未销毁档案的冻结解除、期限修订与新申请销毁不受影响。
	if err := s.Freeze(FreezeInput{ArchiveID: "A-2", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-06-01")}); err != nil {
		t.Fatalf("恢复后应能新增冻结: %v", err)
	}
	if err := s.Release(ReleaseInput{ArchiveID: "A-2", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-06-02")}); err != nil {
		t.Fatalf("恢复后应能解除冻结: %v", err)
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-1", ArchiveID: "A-2",
		OriginalEnd: MustParseDate("2025-06-30"), NewEnd: MustParseDate("2026-06-30"),
		RevisedOn: MustParseDate("2025-06-03"), Reason: "延期",
	}); err != nil {
		t.Fatalf("恢复后应能修订期限: %v", err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2026-06-30"), ArchiveIDs: []string{"A-2"},
	}); err != nil {
		t.Fatalf("恢复后应能用新申请办理销毁: %v", err)
	}
}
