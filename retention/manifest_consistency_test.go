package retention

import (
	"errors"
	"strings"
	"testing"
)

// 已保存的销毁状态与已关闭清册必须相互对应：对应关系损坏时，
// 即使 JSON 能解析，Open 也必须按记录损坏失败（ErrCorruptState），
// 错误信息指出涉及的档案编号与相关申请编号，原文件保持原样。
func TestOpenRejectsBrokenManifestLink(t *testing.T) {
	archive := func(extra string) string {
		return `{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","freezes":[]` + extra + `}`
	}
	manifest := func(appID, entries string) string {
		return `"` + appID + `":{"application_id":"` + appID + `","processed_on":"2025-01-10","entries":[` + entries + `]}`
	}
	entryA1 := `{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}`

	corrupt := map[string]struct {
		state   string
		wantIDs []string
	}{
		"已销毁但没有任何清册": {
			`{"version":1,"archives":{"A-1":` + archive(`,"destroyed":true,"manifest_id":"APP-1"`) + `},"manifests":{}}`,
			[]string{"A-1", "APP-1"},
		},
		"已销毁但归属的清册不存在": {
			`{"version":1,"archives":{"A-1":` + archive(`,"destroyed":true,"manifest_id":"APP-9"`) + `},"manifests":{` + manifest("APP-1", entryA1) + `}}`,
			[]string{"A-1", "APP-9"},
		},
		"已销毁但没有申请编号": {
			`{"version":1,"archives":{"A-1":` + archive(`,"destroyed":true`) + `},"manifests":{` + manifest("APP-1", entryA1) + `}}`,
			[]string{"A-1"},
		},
		"清册未收录该档案": {
			`{"version":1,"archives":{"A-1":` + archive(`,"destroyed":true,"manifest_id":"APP-1"`) + `},"manifests":{` + manifest("APP-1", "") + `}}`,
			[]string{"A-1", "APP-1"},
		},
		"清册重复收录同一档案": {
			`{"version":1,"archives":{"A-1":` + archive(`,"destroyed":true,"manifest_id":"APP-1"`) + `},"manifests":{` + manifest("APP-1", entryA1+`,`+entryA1) + `}}`,
			[]string{"A-1", "APP-1"},
		},
		"清册收录不存在的档案": {
			`{"version":1,"archives":{"A-1":` + archive(`,"destroyed":true,"manifest_id":"APP-1"`) + `},"manifests":{` + manifest("APP-1", entryA1+`,`+`{"id":"A-2","category":"凭证","start":"2020-01-01","end":"2025-01-10"}`) + `}}`,
			[]string{"A-2", "APP-1"},
		},
		"清册收录的档案未销毁": {
			`{"version":1,"archives":{"A-1":` + archive(`,"destroyed":false`) + `},"manifests":{` + manifest("APP-1", entryA1) + `}}`,
			[]string{"A-1", "APP-1"},
		},
		"未销毁档案挂有清册申请编号": {
			`{"version":1,"archives":{"A-1":` + archive(`,"destroyed":false,"manifest_id":"APP-1"`) + `},"manifests":{` + manifest("APP-1", entryA1) + `}}`,
			[]string{"A-1", "APP-1"},
		},
		"同一档案出现在两份清册": {
			`{"version":1,"archives":{"A-1":` + archive(`,"destroyed":true,"manifest_id":"APP-1"`) + `},"manifests":{` + manifest("APP-1", entryA1) + `,` + manifest("APP-2", entryA1) + `}}`,
			[]string{"A-1", "APP-2"},
		},
		"归属指向别的申请": {
			`{"version":1,"archives":{"A-1":` + archive(`,"destroyed":true,"manifest_id":"APP-2"`) + `},"manifests":{` + manifest("APP-1", entryA1) + `,` + manifest("APP-2", "") + `}}`,
			[]string{"A-1", "APP-2"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("销毁状态与清册对应不上时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			for _, id := range tc.wantIDs {
				if !strings.Contains(err.Error(), id) {
					t.Fatalf("错误信息应指出 %s，得到 %v", id, err)
				}
			}
			if got := readStateFile(t, dir); got != tc.state {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}
}

// 保管库打开后保存内容才出现销毁状态与清册不对应：下一次使用有效输入的
// 查询、核对与办理都必须按记录损坏失败——即使操作的是另一份档案——
// 不返回正常历史、清册或部分报告，不留下任何变更，原保存内容保持原样。
func TestManifestCorruptionAfterOpenFailsAllOperations(t *testing.T) {
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
	if _, found, err := s.History("A-1"); err != nil || !found {
		t.Fatalf("损坏前查询应成功: found=%v err=%v", found, err)
	}

	// 清册 APP-1 仍在，但把 A-1 改回未销毁：清册收录了未销毁档案。
	rewriteState(t, dir, func(doc map[string]any) {
		doc["archives"].(map[string]any)["A-1"].(map[string]any)["destroyed"] = false
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误指出 A-1 与 APP-1。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("清册收录未销毁档案时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "A-1") || !strings.Contains(err.Error(), "APP-1") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出 A-1/APP-1，得到 %v", err)
	}

	// 已打开的实例：即使只操作无关的 A-2，也必须按整库损坏失败。
	if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History 必须返回 ErrCorruptState 且无结论: found=%v err=%v", found, err)
	}
	if _, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("损坏后 GetManifest 必须返回 ErrCorruptState: found=%v err=%v", found, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出部分报告: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
	}
	if err := s.Register(RegisterInput{ID: "A-3", Category: "凭证", Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-01-10")}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Register 必须失败: %v", err)
	}

	// 失败不得触发修复：不补清册、不改销毁标记、不删冲突记录。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}

// 已销毁档案的清册在打开后被删掉：档案记下的申请编号找不到清册，
// 后续所有操作同样必须按记录损坏失败，不能返回没有清册的正常历史。
func TestManifestDeletedAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatal(err)
	}

	rewriteState(t, dir, func(doc map[string]any) {
		delete(doc["manifests"].(map[string]any), "APP-1")
	})
	corruptContent := readStateFile(t, dir)

	// 已销毁档案不得被重新判为可以销毁，也不能用另一申请编号再生成清册。
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" {
		t.Fatalf("清册缺失后 Check 必须失败: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("清册缺失后不得用另一申请编号再销毁: %+v err=%v", m, err)
	}
	// 历史查询不得返回“已销毁但没有清册”的正常结论。
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("清册缺失后 History 必须失败且无结论: found=%v err=%v", found, err)
	}
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}

// 对应关系完好的合法记录不受影响：未销毁且无清册归属的档案合法，
// 合法销毁后的历史与原清册完整可查，相同申请重放仍取回原清册；
// 引入期限修订前保存的销毁记录（没有 initial_end）也能正常打开。
func TestValidManifestLinksKeepWorking(t *testing.T) {
	t.Run("销毁后的对应关系可查且可重放", func(t *testing.T) {
		s := openTestStore(t)
		reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
		reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
		m1, err := s.Destroy(DestructionRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
		})
		if err != nil {
			t.Fatal(err)
		}
		h, found, err := s.History("A-1")
		if err != nil || !found || h.Manifest == nil || h.ManifestApplicationID != "APP-1" {
			t.Fatalf("销毁后历史应带原清册: found=%v err=%v %+v", found, err, h)
		}
		m2, err := s.Destroy(DestructionRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
		})
		if err != nil || m2.ApplicationID != m1.ApplicationID || len(m2.Entries) != 1 {
			t.Fatalf("相同申请重放应取回原清册: %+v err=%v", m2, err)
		}
		// 未销毁且没有清册归属的档案是合法记录。
		h2, found, err := s.History("A-2")
		if err != nil || !found || h2.Destroyed || h2.ManifestApplicationID != "" || h2.Manifest != nil {
			t.Fatalf("未销毁档案不应有清册归属: %+v err=%v", h2, err)
		}
	})

	t.Run("旧格式销毁记录可打开", func(t *testing.T) {
		dir := t.TempDir()
		writeStateFile(t, dir, `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","destroyed":true,"manifest_id":"APP-1","freezes":[]}},"manifests":{"APP-1":{"application_id":"APP-1","processed_on":"2025-01-10","entries":[{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}]}}}`)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("修订功能之前保存的销毁记录应能打开: %v", err)
		}
		defer s.Close()
		h, found, err := s.History("A-1")
		if err != nil || !found || h.Manifest == nil {
			t.Fatalf("旧格式销毁记录的历史应完整可查: found=%v err=%v", found, err)
		}
	})
}
