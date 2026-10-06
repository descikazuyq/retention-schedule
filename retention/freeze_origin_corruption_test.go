package retention

import (
	"errors"
	"strings"
	"testing"
)

// 每条冻结保留的原始信息必须与办理冻结时的要求一致：冻结原因去除首尾
// 空白后非空白，冻结日期为真实的 YYYY-MM-DD。已保存的冻结缺少原因
// （字段缺失、为 null、空串或只有空白）或缺少冻结日期（字段缺失或为
// null）时，即使 JSON 本身能解析，Open 也必须按记录损坏处理：返回
// ErrCorruptState，错误指出档案编号、冻结编号以及缺少的是冻结原因还是
// 冻结日期，两项同时缺少时两项一并指出，原文件保持原样。
//
// 这条要求适用于未解除和已解除的全部冻结历史，也适用于已经销毁的档案：
// 已解除冻结即使解除日期与解除原因完整，也不能拿解除信息补齐或顶替缺失
// 的原冻结原因/冻结日期；已销毁档案即使清册归属、条目与处理日期都正确，
// 同样必须拒绝。
func TestOpenRejectsIncompleteFreezeOriginRecords(t *testing.T) {
	archive := func(id string, destroyed bool, freezeJSON string) string {
		destroyedFields := `,"destroyed":false`
		if destroyed {
			destroyedFields = `,"destroyed":true,"manifest_id":"APP-1"`
		}
		return `"` + id + `":{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10"` +
			destroyedFields + `,"freezes":[` + freezeJSON + `]}`
	}
	entry := func(id string) string {
		return `{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"2025-01-10"}`
	}
	state := func(archivesJSON, entriesJSON string) string {
		manifests := "{}"
		if entriesJSON != "" {
			manifests = `{"APP-1":{"application_id":"APP-1","processed_on":"2025-01-10","entries":[` + entriesJSON + `]}}`
		}
		return `{"version":1,"archives":{` + archivesJSON + `},"manifests":` + manifests + `}`
	}
	active := func(freezeJSON string) string {
		return state(archive("A-1", false, freezeJSON), "")
	}

	corrupt := map[string]struct {
		content string
		wantAll []string // 错误信息必须全部包含
	}{
		"未解除冻结缺原因字段": {
			active(`{"id":"F-1","frozen_on":"2025-01-05","released":false}`),
			[]string{"A-1", "F-1", "冻结原因"},
		},
		"未解除冻结原因为 null": {
			active(`{"id":"F-1","reason":null,"frozen_on":"2025-01-05","released":false}`),
			[]string{"A-1", "F-1", "冻结原因"},
		},
		"未解除冻结原因为空串": {
			active(`{"id":"F-1","reason":"","frozen_on":"2025-01-05","released":false}`),
			[]string{"A-1", "F-1", "冻结原因"},
		},
		"未解除冻结原因只有空白": {
			active(`{"id":"F-1","reason":"  \t ","frozen_on":"2025-01-05","released":false}`),
			[]string{"A-1", "F-1", "冻结原因"},
		},
		"未解除冻结缺冻结日期字段": {
			active(`{"id":"F-1","reason":"诉讼","released":false}`),
			[]string{"A-1", "F-1", "冻结日期"},
		},
		"未解除冻结冻结日期为 null": {
			active(`{"id":"F-1","reason":"诉讼","frozen_on":null,"released":false}`),
			[]string{"A-1", "F-1", "冻结日期"},
		},
		"原因与冻结日期同时缺少": {
			active(`{"id":"F-1","released":false}`),
			[]string{"A-1", "F-1", "冻结原因", "冻结日期"},
		},
		"已填写的冻结日期不是真实日期": {
			// 已填写的日期继续遵守真实 YYYY-MM-DD 要求，解析阶段即按损坏拒绝。
			active(`{"id":"F-1","reason":"诉讼","frozen_on":"2025-02-30","released":false}`),
			nil, // 只要求 ErrCorruptState，不要求定位到具体字段
		},
		"已解除且解除信息完整但缺冻结日期": {
			// 不能拿解除日期 2025-01-06 代替冻结日期。
			active(`{"id":"F-1","reason":"诉讼","released":true,"release_reason":"结案","released_on":"2025-01-06"}`),
			[]string{"A-1", "F-1", "冻结日期"},
		},
		"已解除且解除信息完整但缺冻结原因": {
			// 解除原因“结案”不能补齐原冻结原因。
			active(`{"id":"F-1","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-06"}`),
			[]string{"A-1", "F-1", "冻结原因"},
		},
		"已销毁档案的已解除冻结缺冻结日期": {
			// 清册归属、条目快照与处理日期都合法，解除日期与解除原因完整，
			// 原冻结日期缺失仍须按整库损坏拒绝。
			state(archive("A-1", true,
				`{"id":"F-1","reason":"诉讼","released":true,"release_reason":"结案","released_on":"2025-01-06"}`),
				entry("A-1")),
			[]string{"A-1", "F-1", "冻结日期"},
		},
		"已销毁档案的已解除冻结缺冻结原因": {
			state(archive("A-1", true,
				`{"id":"F-1","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-06"}`),
				entry("A-1")),
			[]string{"A-1", "F-1", "冻结原因"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.content)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("冻结原始信息不完整时不应打开成功")
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
			if got := readStateFile(t, dir); got != tc.content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：冻结原因与冻结日期齐备的记录（未解除、已解除，以及没有
	// initial_end 字段的旧格式）必须能正常打开，防止把合法记录误判成损坏。
	valid := map[string]string{
		"未解除冻结信息完整": state(archive("A-1", false,
			`{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false}`), ""),
		"已解除冻结信息完整": state(archive("A-1", false,
			`{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-06"}`), ""),
		"没有冻结的档案":     state(archive("A-1", false, ""), ""),
		"旧格式档案中的完整冻结": `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","destroyed":false,"freezes":[{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false}]}},"manifests":{}}`,
	}
	for name, content := range valid {
		t.Run("合法记录对照组/"+name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, content)
			s, err := Open(dir)
			if err != nil {
				t.Fatalf("冻结原始信息齐备的记录应能打开: %v", err)
			}
			defer s.Close()
		})
	}
}

// 保管库打开后保存内容才出现冻结原始信息缺项：下一次使用有效输入查看历史、
// 取回清册、销毁前核对与各项办理都必须按整库记录损坏失败——即使操作的是
// 另一份正常档案——不返回部分历史、部分核对报告或正常清册，不借办理操作
// 把内容重新保存，原保存内容保持原样。
func TestIncompleteFreezeOriginAfterOpenFailsAllOperations(t *testing.T) {
	cases := map[string]struct {
		mutate     func(map[string]any)
		wantPhrase string
	}{
		"未解除冻结的冻结日期被删除": {
			mutate: func(doc map[string]any) {
				freezes := doc["archives"].(map[string]any)["A-1"].(map[string]any)["freezes"].([]any)
				delete(freezes[0].(map[string]any), "frozen_on")
			},
			wantPhrase: "冻结日期",
		},
		"未解除冻结的冻结日期改为 null": {
			mutate: func(doc map[string]any) {
				freezes := doc["archives"].(map[string]any)["A-1"].(map[string]any)["freezes"].([]any)
				freezes[0].(map[string]any)["frozen_on"] = nil
			},
			wantPhrase: "冻结日期",
		},
		"未解除冻结的原因改成只有空白": {
			mutate: func(doc map[string]any) {
				freezes := doc["archives"].(map[string]any)["A-1"].(map[string]any)["freezes"].([]any)
				freezes[0].(map[string]any)["reason"] = "  \t "
			},
			wantPhrase: "冻结原因",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
			reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")
			if err := s.Freeze(FreezeInput{
				ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05"),
			}); err != nil {
				t.Fatal(err)
			}
			if _, found, err := s.History("A-1"); err != nil || !found {
				t.Fatalf("损坏前查询应成功: found=%v err=%v", found, err)
			}

			rewriteState(t, dir, tc.mutate)
			corruptContent := readStateFile(t, dir)

			// 重新打开必须失败，错误指出档案、冻结与缺少的项目。
			s2, err := Open(dir)
			if err == nil {
				s2.Close()
				t.Fatal("冻结原始信息缺项时，打开必须失败")
			}
			if !errors.Is(err, ErrCorruptState) ||
				!strings.Contains(err.Error(), "A-1") ||
				!strings.Contains(err.Error(), "F-1") ||
				!strings.Contains(err.Error(), tc.wantPhrase) {
				t.Fatalf("打开错误应为 ErrCorruptState 并指出 A-1/F-1/%s，得到 %v", tc.wantPhrase, err)
			}

			// 已打开实例：即使本次只操作与缺项冻结无关的正常档案 A-2，
			// 也必须按整库损坏失败。
			if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
				t.Fatalf("损坏后查询无关档案也必须失败且无结论: found=%v err=%v", found, err)
			}
			if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
				t.Fatalf("损坏后 History 必须失败且不得给出部分历史: found=%v err=%v", found, err)
			}
			if m, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
				t.Fatalf("损坏后 GetManifest 必须失败: found=%v err=%v %+v", found, err, m)
			}
			if r, err := s.Check(CheckRequest{
				ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
			}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
				t.Fatalf("损坏后 Check 必须失败且不得给出可以办理等结论或部分报告: %+v err=%v", r, err)
			}
			if m, err := s.Destroy(DestructionRequest{
				ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
			}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
				t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
			}
			if err := s.Register(RegisterInput{
				ID: "A-3", Category: "单据", Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-06-30"),
			}); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("损坏后 Register 必须失败: %v", err)
			}
			if err := s.Freeze(FreezeInput{
				ArchiveID: "A-2", FreezeID: "F-X", Reason: "协查", FrozenOn: MustParseDate("2025-06-01"),
			}); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("损坏后 Freeze 必须失败: %v", err)
			}
			if err := s.Release(ReleaseInput{
				ArchiveID: "A-2", FreezeID: "F-X", Reason: "协查结束", ReleasedOn: MustParseDate("2025-06-02"),
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

			// 失败不得触发修复或重新保存：不补日期/原因、不删冻结、不改解除状态。
			if got := readStateFile(t, dir); got != corruptContent {
				t.Fatalf("失败后原保存内容被改动:\n%q", got)
			}
		})
	}
}

// 已销毁档案的一条已解除冻结（解除日期与解除原因完整、清册归属/条目/处理
// 日期全部正确）在打开后丢失原冻结日期：即使本次查看历史、取回清册、核对或
// 办理的是另一份正常档案，也必须整库失败——不能拿解除日期代替冻结日期，
// 相同申请的幂等重放也不能取回清册。
func TestReleasedFreezeMissingOriginOnDestroyedAfterOpen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")
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
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatal(err)
	}

	// 删除原冻结日期：解除日期 2025-01-06 与解除原因仍完整保留。
	rewriteState(t, dir, func(doc map[string]any) {
		freezes := doc["archives"].(map[string]any)["A-1"].(map[string]any)["freezes"].([]any)
		delete(freezes[0].(map[string]any), "frozen_on")
	})
	corruptContent := readStateFile(t, dir)

	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("已销毁档案的冻结缺少原冻结日期时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "A-1") ||
		!strings.Contains(err.Error(), "F-1") ||
		!strings.Contains(err.Error(), "冻结日期") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出 A-1/F-1/冻结日期，得到 %v", err)
	}

	// 已打开实例：查询异常档案及其清册、查询/核对/办理无关的正常档案 A-2，
	// 全部按整库损坏失败，不返回部分历史或正常清册。
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后不得返回异常档案（可能不完整）的历史: found=%v err=%v", found, err)
	}
	if _, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("损坏后查询正常档案 A-2 也必须失败: found=%v err=%v", found, err)
	}
	if m, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("损坏后即使按原申请编号取回清册也必须失败: found=%v err=%v %+v", found, err, m)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后相同申请的幂等重放也不得取回清册: %+v err=%v", m, err)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || r.Manifest != nil {
		t.Fatalf("损坏后核对原申请也必须失败且不得附原清册: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后办理无关档案 A-2 也必须失败: %+v err=%v", m, err)
	}
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 补回原冻结日期后，原申请幂等重放取回原清册，无关档案可正常办理。
	rewriteState(t, dir, func(doc map[string]any) {
		freezes := doc["archives"].(map[string]any)["A-1"].(map[string]any)["freezes"].([]any)
		freezes[0].(map[string]any)["frozen_on"] = "2025-01-05"
	})
	if replay, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil || replay.ApplicationID != "APP-1" || len(replay.Entries) != 1 {
		t.Fatalf("恢复后相同申请应取回原清册: %+v err=%v", replay, err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); err != nil {
		t.Fatalf("恢复后无关档案应能正常办理销毁: %v", err)
	}
}
