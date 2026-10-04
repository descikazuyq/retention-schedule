package retention

import (
	"errors"
	"strings"
	"testing"
)

// 已保存的冻结历史必须沿用办理时的编号约束：一份档案的全部冻结历史中，
// 一个冻结编号只能出现一次，解除过的记录仍占用原编号。只要同一档案保存了
// 两条相同编号的冻结，无论两条都未解除、一条已解除而另一条未解除，还是
// 均已解除，即使原因、日期与解除信息完全一致，Open 也必须按保存记录损坏
// 失败（ErrCorruptState），错误信息指出档案编号与重复的冻结编号，原文件
// 保持原样。唯一性只限同一份档案：不同档案各有一条同编号冻结仍是合法记录。
func TestOpenRejectsDuplicateSavedFreezeIDs(t *testing.T) {
	freezeActive := func(id string) string {
		return `{"id":"` + id + `","reason":"诉讼","frozen_on":"2025-01-05","released":false}`
	}
	freezeReleased := func(id string) string {
		return `{"id":"` + id + `","reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-06"}`
	}
	archive := func(id string, destroyed bool, freezes string) string {
		destroyedFields := `,"destroyed":false`
		if destroyed {
			destroyedFields = `,"destroyed":true,"manifest_id":"APP-1"`
		}
		return `"` + id + `":{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10"` +
			destroyedFields + `,"freezes":[` + freezes + `]}`
	}
	entry := func(id string) string {
		return `{"id":"` + id + `","category":"合同","start":"2020-01-01","end":"2025-01-10"}`
	}
	state := func(archivesJSON, manifestsJSON string) string {
		return `{"version":1,"archives":{` + archivesJSON + `},"manifests":{` + manifestsJSON + `}}`
	}

	corrupt := map[string]struct {
		state         string
		wantAll       []string // 错误信息必须全部包含
		wantDuplicate bool     // 错误信息应明确说明冻结历史出现重复
	}{
		"两条同号冻结均未解除": {
			state(archive("A-1", false, freezeActive("F-1")+","+freezeActive("F-1")), ""),
			[]string{"A-1", "F-1"}, true,
		},
		"一条已解除而另一条未解除": {
			state(archive("A-1", false, freezeReleased("F-1")+","+freezeActive("F-1")), ""),
			[]string{"A-1", "F-1"}, true,
		},
		"两条同号冻结均已解除且解除信息完全一致": {
			state(archive("A-1", false, freezeReleased("F-1")+","+freezeReleased("F-1")), ""),
			[]string{"A-1", "F-1"}, true,
		},
		"重复记录夹在不同编号记录之间": {
			state(archive("A-1", false,
				freezeActive("F-1")+","+freezeActive("F-2")+","+freezeActive("F-1")), ""),
			[]string{"A-1", "F-1"}, true,
		},
		"已销毁档案的冻结历史出现同号重复": {
			// 即使两条均已合法解除、清册归属、期限与处理日期都正确，重复编号仍拒绝打开。
			state(archive("A-1", true, freezeReleased("F-1")+","+freezeReleased("F-1")),
				`"APP-1":{"application_id":"APP-1","processed_on":"2025-01-10","entries":[`+entry("A-1")+`]}`),
			[]string{"A-1", "F-1"}, true,
		},
		"已保存冻结编号为空白": {
			state(archive("A-1", false,
				`{"id":"  ","reason":"诉讼","frozen_on":"2025-01-05","released":false}`), ""),
			[]string{"A-1"}, false,
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("同一档案保存两条同号冻结时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			if tc.wantDuplicate && !strings.Contains(msg, "重复") {
				t.Fatalf("错误信息应说明冻结历史出现重复，得到 %v", err)
			}
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

	// 对照组：唯一性只限定在同一份档案内。
	t.Run("不同档案同号冻结合法对照组", func(t *testing.T) {
		valid := map[string]string{
			"两份未销毁档案各有一条同号冻结": state(
				archive("A-1", false, freezeActive("F-1"))+","+archive("A-2", false, freezeActive("F-1")),
				"",
			),
			"一份已销毁一份未销毁但各有同号冻结": state(
				archive("A-1", true, freezeReleased("F-1"))+","+archive("A-2", false, freezeActive("F-1")),
				`"APP-1":{"application_id":"APP-1","processed_on":"2025-01-10","entries":[`+entry("A-1")+`]}`,
			),
			"同一档案多条不同编号冻结": state(
				archive("A-1", false, freezeActive("F-1")+","+freezeActive("F-2")),
				"",
			),
		}
		for name, content := range valid {
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				writeStateFile(t, dir, content)
				s, err := Open(dir)
				if err != nil {
					t.Fatalf("编号唯一性只限同一档案，合法记录应能打开: %v", err)
				}
				defer s.Close()
			})
		}
	})
}

// 保管库打开后保存内容才出现同一档案同号冻结：下一次使用有效输入查询历史、
// 取回清册、销毁前核对与各项办理（含解除）都必须按整库记录损坏失败——即使
// 本次查看或操作的是另一份正常档案——解除不得挑一条同号记录写入原因和日期，
// 销毁不得生成清册，不返回正常历史、清册或部分核对报告，原保存内容保持原样。
func TestDuplicateFreezeIDAfterOpenFailsAllOperations(t *testing.T) {
	t.Run("两条同号均未解除时解除不得挑一条写入", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
		reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")
		for _, in := range []FreezeInput{
			{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")},
			{ArchiveID: "A-1", FreezeID: "F-2", Reason: "审计", FrozenOn: MustParseDate("2025-01-06")},
			{ArchiveID: "A-2", FreezeID: "F-1", Reason: "协查", FrozenOn: MustParseDate("2025-06-01")},
		} {
			if err := s.Freeze(in); err != nil {
				t.Fatal(err)
			}
		}
		if h, found, err := s.History("A-2"); err != nil || !found || len(h.ActiveFreezes) != 1 {
			t.Fatalf("损坏前查询应成功: found=%v err=%v %+v", found, err, h)
		}

		// 把 A-1 的第二条冻结改成与第一条同号（保存顺序 F-1、F-1，均未解除）。
		rewriteState(t, dir, func(doc map[string]any) {
			freezes := doc["archives"].(map[string]any)["A-1"].(map[string]any)["freezes"].([]any)
			freezes[1].(map[string]any)["id"] = "F-1"
		})
		corruptContent := readStateFile(t, dir)

		// 重新打开必须失败，错误指出档案 A-1 与重复的冻结编号 F-1。
		s2, err := Open(dir)
		if err == nil {
			s2.Close()
			t.Fatal("同一档案出现同号冻结时，打开必须失败")
		}
		if !errors.Is(err, ErrCorruptState) ||
			!strings.Contains(err.Error(), "A-1") || !strings.Contains(err.Error(), "F-1") {
			t.Fatalf("打开错误应为 ErrCorruptState 并指出 A-1/F-1，得到 %v", err)
		}

		// 已打开实例：即使本次只操作无关的正常档案 A-2，也必须按整库损坏失败。
		if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
			t.Fatalf("损坏后查询无关档案也必须失败且无结论: found=%v err=%v", found, err)
		}
		if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
			t.Fatalf("损坏后 History(A-1) 必须失败且不得给出正常冻结历史: found=%v err=%v", found, err)
		}
		if _, found, err := s.GetManifest("APP-X"); !errors.Is(err, ErrCorruptState) || found {
			t.Fatalf("损坏后 GetManifest 必须失败: found=%v err=%v", found, err)
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
			ID: "A-3", Category: "单据", Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-06-30"),
		}); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("损坏后 Register 必须失败: %v", err)
		}
		if err := s.Freeze(FreezeInput{
			ArchiveID: "A-2", FreezeID: "F-X", Reason: "诉讼", FrozenOn: MustParseDate("2025-06-01"),
		}); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("损坏后 Freeze 必须失败: %v", err)
		}
		// 解除同号记录：不得挑选其中一条写入解除原因和日期。
		if err := s.Release(ReleaseInput{
			ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-07"),
		}); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("损坏后 Release 不得挑一条同号记录解除: %v", err)
		}
		// 无关档案 A-2 上本可正常解除的 F-1 同样不能办理。
		if err := s.Release(ReleaseInput{
			ArchiveID: "A-2", FreezeID: "F-1", Reason: "协查结束", ReleasedOn: MustParseDate("2025-06-02"),
		}); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("损坏后解除无关档案的冻结也必须失败: %v", err)
		}
		if _, err := s.Revise(ReviseInput{
			RevisionID: "R-9", ArchiveID: "A-2",
			OriginalEnd: MustParseDate("2025-06-30"), NewEnd: MustParseDate("2026-06-30"),
			RevisedOn: MustParseDate("2025-06-01"), Reason: "延期",
		}); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("损坏后 Revise 必须失败: %v", err)
		}

		// 失败不得触发修复或重新保存：不改编号、不删除/合并记录、不自动解除。
		if got := readStateFile(t, dir); got != corruptContent {
			t.Fatalf("失败后原保存内容被改动:\n%q", got)
		}

		// 恢复编号唯一性后业务恢复：A-1 的 F-1 只有一条，解除只作用于它；
		// F-2 仍未解除，继续按既有规则阻止 A-1 销毁；A-2 不受影响可正常办理。
		rewriteState(t, dir, func(doc map[string]any) {
			freezes := doc["archives"].(map[string]any)["A-1"].(map[string]any)["freezes"].([]any)
			freezes[1].(map[string]any)["id"] = "F-2"
		})
		if err := s.Release(ReleaseInput{
			ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-07"),
		}); err != nil {
			t.Fatalf("恢复后解除 F-1 应成功: %v", err)
		}
		if _, err := s.Destroy(DestructionRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
		}); !errors.Is(err, ErrActiveFreeze) {
			t.Fatalf("恢复后 F-2 仍应阻止 A-1 销毁，得到 %v", err)
		}
		if err := s.Release(ReleaseInput{
			ArchiveID: "A-1", FreezeID: "F-2", Reason: "审计结束", ReleasedOn: MustParseDate("2025-01-08"),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Destroy(DestructionRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
		}); err != nil {
			t.Fatalf("全部冻结合法解除后 A-1 应能按到期规则销毁: %v", err)
		}
		// 损坏期间 A-2 的 F-1 并未被解除：恢复后需正常解除才能销毁，
		// 证明失败没有改动任何一条记录。
		if err := s.Release(ReleaseInput{
			ArchiveID: "A-2", FreezeID: "F-1", Reason: "协查结束", ReleasedOn: MustParseDate("2025-06-02"),
		}); err != nil {
			t.Fatalf("恢复后解除 A-2 的 F-1 应成功: %v", err)
		}
		if _, err := s.Destroy(DestructionRequest{
			ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
		}); err != nil {
			t.Fatalf("无关档案 A-2 应能正常办理销毁: %v", err)
		}
	})

	t.Run("一条已解除一条未解除时解除不得误判为已解除", func(t *testing.T) {
		dir := t.TempDir()
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
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
			ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-06"),
		}); err != nil {
			t.Fatal(err)
		}
		// 保存顺序变为 F-1（已解除）、F-1（未解除）。
		rewriteState(t, dir, func(doc map[string]any) {
			freezes := doc["archives"].(map[string]any)["A-1"].(map[string]any)["freezes"].([]any)
			freezes[1].(map[string]any)["id"] = "F-1"
		})
		corruptContent := readStateFile(t, dir)

		s2, err := Open(dir)
		if err == nil {
			s2.Close()
			t.Fatal("同一档案同号冻结（一条已解除一条未解除）时，打开必须失败")
		}
		if !errors.Is(err, ErrCorruptState) ||
			!strings.Contains(err.Error(), "A-1") || !strings.Contains(err.Error(), "F-1") {
			t.Fatalf("打开错误应为 ErrCorruptState 并指出 A-1/F-1，得到 %v", err)
		}
		// 解除不能只命中已解除的一条而返回“已经解除”，也不能给未解除的一条写入。
		if err := s.Release(ReleaseInput{
			ArchiveID: "A-1", FreezeID: "F-1", Reason: "再次结案", ReleasedOn: MustParseDate("2025-01-09"),
		}); !errors.Is(err, ErrCorruptState) {
			t.Fatalf("损坏后 Release 必须返回 ErrCorruptState，得到 %v", err)
		}
		if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
			t.Fatalf("损坏后 History 必须失败: found=%v err=%v", found, err)
		}
		if got := readStateFile(t, dir); got != corruptContent {
			t.Fatalf("失败后原保存内容被改动:\n%q", got)
		}
	})
}

// 合法性对照组：冻结编号唯一性只限同一份档案。不同档案各有一条同编号冻结时，
// 解除其中一份档案的冻结不能影响另一份；同一档案多条不同编号冻结时，完整
// 历史按登记顺序返回，未解除记录逐条作为销毁阻碍，全部合法解除后才按既有
// 到期规则办理销毁。
func TestFreezeIDUniquenessIsPerArchive(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	reg(t, s, "A-3", "单据", "2020-01-01", "2025-01-10")
	// 两份不同档案各自登记同编号冻结 F-1。
	for _, in := range []FreezeInput{
		{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")},
		{ArchiveID: "A-2", FreezeID: "F-1", Reason: "协查", FrozenOn: MustParseDate("2025-01-06")},
	} {
		if err := s.Freeze(in); err != nil {
			t.Fatalf("不同档案使用同编号冻结应合法: %v", err)
		}
	}
	// 同一档案按非排序顺序登记多条不同编号冻结。
	for _, in := range []FreezeInput{
		{ArchiveID: "A-3", FreezeID: "F-2", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")},
		{ArchiveID: "A-3", FreezeID: "F-1", Reason: "审计", FrozenOn: MustParseDate("2025-01-06")},
		{ArchiveID: "A-3", FreezeID: "F-3", Reason: "核查", FrozenOn: MustParseDate("2025-01-07")},
	} {
		if err := s.Freeze(in); err != nil {
			t.Fatal(err)
		}
	}

	// 只解除 A-1 的 F-1：A-2 的同编号冻结必须仍未解除。
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-08"),
	}); err != nil {
		t.Fatal(err)
	}
	h2, found, err := s.History("A-2")
	if err != nil || !found || len(h2.ActiveFreezes) != 1 || h2.ActiveFreezes[0].ID != "F-1" {
		t.Fatalf("解除 A-1 的 F-1 不应影响 A-2 的同编号冻结: found=%v err=%v %+v", found, err, h2)
	}
	r2, err := s.Check(CheckRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-2"},
	})
	if err != nil || r2.Status != CheckBlocked ||
		len(r2.Results[0].Obstructions) != 1 ||
		r2.Results[0].Obstructions[0].Kind != ObstructionActiveFreeze ||
		r2.Results[0].Obstructions[0].Freeze.FreezeID != "F-1" {
		t.Fatalf("A-2 的同编号冻结仍应逐条作为销毁阻碍: %+v err=%v", r2, err)
	}

	// A-1 已无未解除冻结且到期，可先销毁；A-2 仍被自己的 F-1 挡住。
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatalf("A-1 解除后应能销毁: %v", err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrActiveFreeze) {
		t.Fatalf("A-2 的 F-1 仍应阻止销毁，得到 %v", err)
	}

	// 同一档案多条不同编号冻结：完整历史按登记顺序返回，核对逐条列阻碍。
	h3, found, err := s.History("A-3")
	if err != nil || !found {
		t.Fatalf("A-3 查询失败: found=%v err=%v", found, err)
	}
	gotOrder := []string{}
	for _, f := range h3.Freezes {
		gotOrder = append(gotOrder, f.ID)
	}
	if len(gotOrder) != 3 || gotOrder[0] != "F-2" || gotOrder[1] != "F-1" || gotOrder[2] != "F-3" {
		t.Fatalf("完整冻结历史应按登记顺序返回，得到 %v", gotOrder)
	}
	r3, err := s.Check(CheckRequest{
		ApplicationID: "APP-3", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-3"},
	})
	if err != nil || len(r3.Results[0].Obstructions) != 3 {
		t.Fatalf("三条未解除冻结应逐条成为阻碍: %+v err=%v", r3, err)
	}
	for i, fID := range []string{"F-2", "F-1", "F-3"} {
		if obs := r3.Results[0].Obstructions[i]; obs.Kind != ObstructionActiveFreeze || obs.Freeze.FreezeID != fID {
			t.Fatalf("第 %d 条阻碍应对应按登记顺序的 %s，得到 %+v", i, fID, obs)
		}
	}
	for _, in := range []ReleaseInput{
		{ArchiveID: "A-3", FreezeID: "F-2", Reason: "结束", ReleasedOn: MustParseDate("2025-01-08")},
		{ArchiveID: "A-3", FreezeID: "F-1", Reason: "结束", ReleasedOn: MustParseDate("2025-01-08")},
		{ArchiveID: "A-3", FreezeID: "F-3", Reason: "结束", ReleasedOn: MustParseDate("2025-01-08")},
	} {
		if err := s.Release(in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-3", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-3"},
	}); err != nil {
		t.Fatalf("全部冻结合法解除后 A-3 应能按到期规则销毁: %v", err)
	}

	// 重开后：跨档案同号与同档案不同编号都仍是合法记录。
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("合法记录重开后应能打开: %v", err)
	}
	defer s2.Close()
	if h, found, err := s2.History("A-2"); err != nil || !found || len(h.ActiveFreezes) != 1 {
		t.Fatalf("重开后 A-2 的 F-1 仍应未解除: found=%v err=%v %+v", found, err, h)
	}
	if h, found, err := s2.History("A-3"); err != nil || !found || len(h.Freezes) != 3 || len(h.ActiveFreezes) != 0 {
		t.Fatalf("重开后 A-3 的完整冻结历史应保留: found=%v err=%v %+v", found, err, h)
	}
}
