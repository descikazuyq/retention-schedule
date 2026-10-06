package retention

import (
	"errors"
	"strings"
	"testing"
)

// 保存的冻结缺少冻结原因（缺失、为 null、为空串或仅含空白）或冻结日期
// （字段缺失或为 null）时，Open 必须按保存记录损坏失败（ErrCorruptState）：
// 错误指出档案编号、冻结编号以及缺少的是冻结原因还是冻结日期，两项同时
// 缺少时两项都要说清；未解除、已解除与已销毁档案的冻结同样适用，
// 原文件保持原样。
func TestOpenRejectsFreezeMissingOriginInfo(t *testing.T) {
	freeze := func(id, attrs string) string {
		return `{"id":"` + id + `"` + attrs + `,"released":false}`
	}
	freezeReleased := func(id, attrs string) string {
		return `{"id":"` + id + `"` + attrs +
			`,"released":true,"release_reason":"结案","released_on":"2025-01-06"}`
	}
	archive := func(id string, freezes string) string {
		return `"` + id + `":{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[` + freezes + `]}`
	}
	destroyedArchive := func(id string, freezes string) string {
		return `"` + id + `":{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1","freezes":[` + freezes + `]}`
	}
	entry := func(id string) string {
		return `{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"2025-01-10"}`
	}
	state := func(archivesJSON string) string {
		return `{"version":1,"archives":{` + archivesJSON + `},"manifests":{}}`
	}
	destroyedState := func(archivesJSON, entriesJSON string) string {
		return `{"version":1,"archives":{` + archivesJSON + `},"manifests":{"APP-1":{"application_id":"APP-1","processed_on":"2025-01-10","entries":[` + entriesJSON + `]}}}`
	}

	corrupt := map[string]struct {
		state   string
		wantAll []string // 错误信息必须全部包含
	}{
		"未解除冻结缺少原因字段": {
			state(archive("A-1", freeze("F-1", `,"frozen_on":"2025-01-05"`))),
			[]string{"A-1", "F-1", "冻结原因"},
		},
		"未解除冻结原因为 null": {
			state(archive("A-1", freeze("F-1", `,"reason":null,"frozen_on":"2025-01-05"`))),
			[]string{"A-1", "F-1", "冻结原因"},
		},
		"未解除冻结原因为空串": {
			state(archive("A-1", freeze("F-1", `,"reason":"","frozen_on":"2025-01-05"`))),
			[]string{"A-1", "F-1", "冻结原因"},
		},
		"未解除冻结原因只有空白": {
			state(archive("A-1", freeze("F-1", `,"reason":"   ","frozen_on":"2025-01-05"`))),
			[]string{"A-1", "F-1", "冻结原因"},
		},
		"未解除冻结缺少冻结日期字段": {
			state(archive("A-1", freeze("F-1", `,"reason":"诉讼"`))),
			[]string{"A-1", "F-1", "冻结日期"},
		},
		"未解除冻结冻结日期为 null": {
			state(archive("A-1", freeze("F-1", `,"reason":"诉讼","frozen_on":null`))),
			[]string{"A-1", "F-1", "冻结日期"},
		},
		"原因与冻结日期同时缺少": {
			state(archive("A-1", freeze("F-1", ""))),
			[]string{"A-1", "F-1", "冻结原因", "冻结日期"},
		},
		"已解除冻结缺少冻结日期不能用解除日期代替": {
			// 解除信息完整也不能顶替缺失的原冻结日期。
			state(archive("A-1", freezeReleased("F-1", `,"reason":"诉讼"`))),
			[]string{"A-1", "F-1", "冻结日期"},
		},
		"已解除冻结缺少原因不能用解除原因补齐": {
			state(archive("A-1", freezeReleased("F-1", `,"frozen_on":"2025-01-05"`))),
			[]string{"A-1", "F-1", "冻结原因"},
		},
		"已销毁档案的冻结缺少冻结日期": {
			// 清册归属、条目与处理日期都正确也不能掩盖这条缺项。
			destroyedState(destroyedArchive("A-1", freezeReleased("F-1", `,"reason":"诉讼"`)), entry("A-1")),
			[]string{"A-1", "F-1", "冻结日期"},
		},
		"已销毁档案的冻结缺少原因": {
			destroyedState(destroyedArchive("A-1", freezeReleased("F-1", `,"frozen_on":"2025-01-05"`)), entry("A-1")),
			[]string{"A-1", "F-1", "冻结原因"},
		},
		"多份档案中第二份的冻结缺项": {
			state(archive("A-1", freeze("F-1", `,"reason":"诉讼","frozen_on":"2025-01-05"`)) + "," +
				archive("A-2", freeze("F-2", `,"reason":"  "`))),
			[]string{"A-2", "F-2", "冻结原因", "冻结日期"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("冻结缺少冻结原因或冻结日期时不应打开成功")
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
			if got := readStateFile(t, dir); got != tc.state {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：冻结原因与冻结日期齐备的记录（未解除、已解除、已销毁档案）
	// 与没有冻结的档案都可正常打开。
	t.Run("合法记录对照组", func(t *testing.T) {
		valid := map[string]string{
			"没有冻结的档案":   state(archive("A-1", "")),
			"未解除冻结信息齐备": state(archive("A-1", freeze("F-1", `,"reason":"诉讼","frozen_on":"2025-01-05"`))),
			"已解除冻结信息齐备": state(archive("A-1", freezeReleased("F-1", `,"reason":"诉讼","frozen_on":"2025-01-05"`))),
			"已销毁档案冻结信息齐备": destroyedState(
				destroyedArchive("A-1", freezeReleased("F-1", `,"reason":"诉讼","frozen_on":"2025-01-05"`)),
				entry("A-1")),
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
				if _, _, err := s.History("A-1"); err != nil {
					t.Fatalf("合法记录的历史应可查: %v", err)
				}
			})
		}
	})
}

// 保管库打开后保存内容才出现冻结原始信息缺项：下一次使用有效输入查询历史、
// 取回清册、销毁前核对与各项办理都必须按整库记录损坏失败——即使本次操作的
// 是另一份正常档案——不返回部分历史、部分核对报告或正常清册，也不借办理
// 操作把内容重新保存，原保存内容保持原样。
func TestFreezeMissingOriginInfoAfterOpenFailsAllOperations(t *testing.T) {
	cases := map[string]func(map[string]any){
		"冻结原因被改成空白": func(doc map[string]any) {
			freezes := doc["archives"].(map[string]any)["A-1"].(map[string]any)["freezes"].([]any)
			freezes[0].(map[string]any)["reason"] = "   "
		},
		"冻结日期字段被删除": func(doc map[string]any) {
			freezes := doc["archives"].(map[string]any)["A-1"].(map[string]any)["freezes"].([]any)
			delete(freezes[0].(map[string]any), "frozen_on")
		},
		"已解除冻结的冻结日期被改成 null": func(doc map[string]any) {
			freezes := doc["archives"].(map[string]any)["A-1"].(map[string]any)["freezes"].([]any)
			freezes[1].(map[string]any)["frozen_on"] = nil
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
			if err := s.Freeze(FreezeInput{
				ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05"),
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.Freeze(FreezeInput{
				ArchiveID: "A-1", FreezeID: "F-2", Reason: "审计", FrozenOn: MustParseDate("2025-01-06"),
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.Release(ReleaseInput{
				ArchiveID: "A-1", FreezeID: "F-2", Reason: "结案", ReleasedOn: MustParseDate("2025-01-07"),
			}); err != nil {
				t.Fatal(err)
			}

			rewriteState(t, dir, mutate)
			corruptContent := readStateFile(t, dir)

			// 重新打开必须失败，错误指出档案编号、冻结编号与缺少的项目。
			s2, err := Open(dir)
			if err == nil {
				s2.Close()
				t.Fatal("冻结原始信息缺项时，打开必须失败")
			}
			if !errors.Is(err, ErrCorruptState) ||
				!strings.Contains(err.Error(), "A-1") {
				t.Fatalf("打开错误应为 ErrCorruptState 并指出 A-1，得到 %v", err)
			}

			// 已打开实例：即使本次只操作另一份正常档案 A-2，也必须按整库损坏失败。
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
				t.Fatalf("损坏后 Check 必须失败且不得给出结论或部分报告: %+v err=%v", r, err)
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
				ArchiveID: "A-2", FreezeID: "F-X", Reason: "诉讼", FrozenOn: MustParseDate("2025-06-01"),
			}); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("损坏后 Freeze 必须失败: %v", err)
			}
			if err := s.Release(ReleaseInput{
				ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-06-02"),
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

			// 失败不得触发修复：不补写日期或原因、不删冻结、不改解除状态，
			// 也不借办理操作把内容重新保存。
			if got := readStateFile(t, dir); got != corruptContent {
				t.Fatalf("失败后原保存内容被改动:\n%q", got)
			}
		})
	}
}

// 正常新增冻结仍要求原因与日期齐备，且原因去除首尾空白后保存——
// 读取侧的缺项校验不影响办理入口的既有行为。
func TestFreezeInputValidationUnchanged(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "  ", FrozenOn: MustParseDate("2025-01-05"),
	}); !errors.Is(err, ErrBlankField) {
		t.Fatalf("空白原因仍应拒绝: %v", err)
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼",
	}); !errors.Is(err, ErrInvalidDate) {
		t.Fatalf("缺失冻结日期仍应拒绝: %v", err)
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "  诉讼  ", FrozenOn: MustParseDate("2025-01-05"),
	}); err != nil {
		t.Fatal(err)
	}
	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("历史应可查: found=%v err=%v", found, err)
	}
	if len(h.Freezes) != 1 || h.Freezes[0].Reason != "诉讼" ||
		!h.Freezes[0].FrozenOn.Equal(MustParseDate("2025-01-05")) {
		t.Fatalf("原因应去除首尾空白后保存，冻结日期应保留: %+v", h.Freezes)
	}
}
