package retention

import (
	"errors"
	"strings"
	"testing"
)

// 同一份已关闭清册自己的保存内容中，处理日期 processed_on 最多只能出现一次：
// 普通读取对同名字段只保留最后一个值，一份清册若先写处理日期 2025-01-10、后写
// 2025-01-11（档案截止日为 2025-01-10，两个日期都满足到期规则），其他内容均
// 合法时保管库仍能打开，按申请编号取回或档案历史里看到的销毁日期却取决于两处的
// 保存顺序。Open 必须按保存记录损坏失败（ErrCorruptState），错误指出清册申请
// 编号，并明确重复的是处理日期 processed_on 本身，而不是申请编号对应了多份清册；
// 两处日期不同、完全相同或其中一处为 null 都按同一规则拒绝，不挑选某个日期继续
// 使用，交换字段顺序也不改变拒绝结果，原文件保持原样。
func TestOpenRejectsDuplicateProcessedOnField(t *testing.T) {
	// manifest 构造一份已关闭清册，processedFields 是原样拼接进记录的处理日期
	// 字段（可以写一个或多个，字段名写法与取值任意）。
	manifest := func(processedFields ...string) string {
		fields := `"application_id":"APP-1"`
		for _, f := range processedFields {
			fields += "," + f
		}
		fields += `,"entries":[{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}]`
		return `{` + fields + `}`
	}
	state := func(manifestRec string) string {
		return `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1","freezes":[]}},"manifests":{"APP-1":` + manifestRec + `}}`
	}

	corrupt := map[string]string{
		// 任务场景：档案截止日 2025-01-10，先写处理日期 2025-01-10、后写
		// 2025-01-11，两个日期都满足到期规则，普通解码会采用后一个日期。
		"先截止日当天后次日":   state(manifest(`"processed_on":"2025-01-10"`, `"processed_on":"2025-01-11"`)),
		"互换保存顺序":      state(manifest(`"processed_on":"2025-01-11"`, `"processed_on":"2025-01-10"`)),
		"两处日期完全相同":    state(manifest(`"processed_on":"2025-01-10"`, `"processed_on":"2025-01-10"`)),
		"两处都是次日且相同":   state(manifest(`"processed_on":"2025-01-11"`, `"processed_on":"2025-01-11"`)),
		"第二处为null":    state(manifest(`"processed_on":"2025-01-10"`, `"processed_on":null`)),
		"第一处为null":    state(manifest(`"processed_on":null`, `"processed_on":"2025-01-10"`)),
		"两处都为null":    state(manifest(`"processed_on":null`, `"processed_on":null`)),
		"处理日期出现三次":    state(manifest(`"processed_on":"2025-01-10"`, `"processed_on":"2025-01-11"`, `"processed_on":"2025-01-12"`)),
		"大小写写法混用重复":   state(manifest(`"Processed_On":"2025-01-10"`, `"processed_on":"2025-01-11"`)),
		"大小写写法混用顺序相反": state(manifest(`"processed_on":"2025-01-10"`, `"PROCESSED_ON":"2025-01-11"`)),
	}
	// 转义写法需要字面反斜杠，用 Go 双引号字符串构造出 JSON 里的
	// processed_on：JSON 解码后首字符是 p，与直接写出的 processed_on
	// 是同一个字段名。
	escProcessedOn := "\\u0070rocessed_on"
	corrupt["Unicode转义字段名与直接字段名重复"] = state(manifest(`"processed_on":"2025-01-10"`, `"`+escProcessedOn+`":"2025-01-11"`))
	corrupt["Unicode转义字段名与直接字段名重复顺序相反"] = state(manifest(`"`+escProcessedOn+`":"2025-01-10"`, `"processed_on":"2025-01-11"`))

	for name, content := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, content)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("同一份清册的保存内容中出现两次处理日期时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, "APP-1") {
				t.Fatalf("错误信息应指出清册申请编号 APP-1，得到 %v", err)
			}
			if !strings.Contains(msg, "processed_on") || !strings.Contains(msg, "处理日期") {
				t.Fatalf("错误信息应明确重复的是处理日期 processed_on，得到 %v", err)
			}
			// 不得误报为同一申请编号对应多份清册：重复的是单份清册内的处理日期。
			if strings.Contains(msg, "多份清册") {
				t.Fatalf("错误信息不应误报为申请编号对应多份清册，得到 %v", err)
			}
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：单个处理日期（含现有大小写写法与转义写法单独出现）继续可读；
	// 仅一次为 null 仍按既有的“缺少处理日期”规则损坏，而不是按重复字段拒绝；
	// 多份清册各自保存一个处理日期属于正常记录，不合在一起计数。
	t.Run("合法记录对照组", func(t *testing.T) {
		valid := map[string]struct {
			content string
			want    string // APP-1 清册唯一一次保存的处理日期
		}{
			"单个处理日期截止日当天":           {state(manifest(`"processed_on":"2025-01-10"`)), "2025-01-10"},
			"单个处理日期晚于截止日":           {state(manifest(`"processed_on":"2025-01-11"`)), "2025-01-11"},
			"大小写写法Processed_On单独出现": {state(manifest(`"Processed_On":"2025-01-10"`)), "2025-01-10"},
			"大小写写法PROCESSED_ON单独出现": {state(manifest(`"PROCESSED_ON":"2025-01-10"`)), "2025-01-10"},
			// 多份清册各自带一个处理日期：重复检查只在单份清册内计数。
			"多份清册各自一个处理日期": {`{"version":1,"archives":{` +
				`"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1","freezes":[]},` +
				`"A-2":{"id":"A-2","category":"凭证","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-2","freezes":[]}` +
				`},"manifests":{` +
				`"APP-1":{"application_id":"APP-1","processed_on":"2025-01-10","entries":[{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}]},` +
				`"APP-2":{"application_id":"APP-2","processed_on":"2025-01-11","entries":[{"id":"A-2","category":"凭证","start":"2020-01-01","end":"2025-01-10"}]}` +
				`}}`, "2025-01-10"},
		}
		// 转义写法需要字面反斜杠，用 Go 双引号字符串构造（escProcessedOn
		// 已在外层构造）：转义字段名单独出现时继续可读。
		valid["Unicode转义字段名单独出现"] = struct {
			content string
			want    string
		}{state(manifest(`"` + escProcessedOn + `":"2025-01-10"`)), "2025-01-10"}
		for name, tc := range valid {
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				writeStateFile(t, dir, tc.content)
				s, err := Open(dir)
				if err != nil {
					t.Fatalf("合法记录应能打开: %v", err)
				}
				defer s.Close()
				m, found, err := s.GetManifest("APP-1")
				if err != nil || !found {
					t.Fatalf("合法清册应可按申请编号取回: found=%v err=%v", found, err)
				}
				if !m.ProcessedOn.Equal(MustParseDate(tc.want)) {
					t.Fatalf("处理日期应按唯一一次保存读取为 %s，得到 %s", tc.want, m.ProcessedOn)
				}
			})
		}

		// 仅一次为 null 不是字段重复：继续沿用既有“缺少处理日期”的损坏规则，
		// 不能误报成处理日期出现了多次。
		dir := t.TempDir()
		content := state(manifest(`"processed_on":null`))
		writeStateFile(t, dir, content)
		s, err := Open(dir)
		if err == nil {
			s.Close()
			t.Fatal("清册仅有的处理日期为 null 时应按缺少处理日期损坏，而不是打开成功")
		}
		if !errors.Is(err, ErrCorruptState) {
			t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
		}
		if !strings.Contains(err.Error(), "缺少处理日期") || strings.Contains(err.Error(), "出现了多次") {
			t.Fatalf("单个 null 应报缺少处理日期而非重复字段，得到 %v", err)
		}
	})

	// 合法清册仍可按申请编号取回、通过档案历史查看，或用相同日期和档案集合
	// 重提原申请取得原内容。
	t.Run("合法清册取回与重放不变", func(t *testing.T) {
		dir := t.TempDir()
		writeStateFile(t, dir, state(manifest(`"processed_on":"2025-01-10"`)))
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("合法记录应能打开: %v", err)
		}
		defer s.Close()
		m, found, err := s.GetManifest("APP-1")
		if err != nil || !found || m.ApplicationID != "APP-1" ||
			!m.ProcessedOn.Equal(MustParseDate("2025-01-10")) || len(m.Entries) != 1 {
			t.Fatalf("应能按申请编号取回原清册: %+v found=%v err=%v", m, found, err)
		}
		h, found, err := s.History("A-1")
		if err != nil || !found || h.Manifest == nil ||
			!h.Manifest.ProcessedOn.Equal(MustParseDate("2025-01-10")) {
			t.Fatalf("档案历史应能查看所属清册与处理日期: %+v found=%v err=%v", h, found, err)
		}
		// 相同日期和档案集合重提原申请，取回的是同一份内容，不产生新记录。
		replay, err := s.Destroy(DestructionRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
		})
		if err != nil || replay.ApplicationID != "APP-1" ||
			!replay.ProcessedOn.Equal(MustParseDate("2025-01-10")) || len(replay.Entries) != 1 {
			t.Fatalf("相同日期与档案集合重提应取回原清册: %+v err=%v", replay, err)
		}
	})
}

// 保管库打开后保存内容才出现重复的处理日期：下一次使用有效输入查询历史、取回清册、
// 销毁前核对与各项办理都必须按整库记录损坏失败——即使查询、操作的是另一份正常
// 档案——不返回正常历史、清册或部分核对报告，不销毁、不生成清册，也不借办理业务
// 覆盖原内容或重新保存来消除重复，原保存内容保持原样。
func TestDuplicateProcessedOnFieldAfterOpenFailsAllOperations(t *testing.T) {
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

	// 在 APP-1 清册里再写入一个处理日期：先保留原来的 2025-01-10，后面再写
	// 2025-01-11——普通解码会采用后一个日期，使取回的销毁日期随保存顺序变化。
	// 整份状态只有 APP-1 一份清册，故文件中 processed_on 只出现这一处。
	good := readStateFile(t, dir)
	corrupt := strings.Replace(good, `"processed_on": "2025-01-10"`,
		`"processed_on": "2025-01-10", "processed_on": "2025-01-11"`, 1)
	if corrupt == good {
		t.Fatal("未找到清册处理日期的位置，测试夹具失效")
	}
	writeStateFile(t, dir, corrupt)
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误指出清册申请编号 APP-1，并明确重复的是处理日期
	// processed_on，而不是申请编号对应了多份清册。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("同一份清册的保存内容中出现两个处理日期时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "APP-1") ||
		!strings.Contains(err.Error(), "processed_on") ||
		!strings.Contains(err.Error(), "处理日期") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出清册 APP-1 的处理日期 processed_on 重复，得到 %v", err)
	}
	if strings.Contains(err.Error(), "多份清册") {
		t.Fatalf("错误信息不应误报为申请编号对应多份清册，得到 %v", err)
	}

	// 已打开实例：即使本次只查询、操作没有问题的正常档案 A-2，也必须按整库
	// 损坏失败。
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
	// 已成功申请的幂等重放同样不能取回原清册，也不能借重放覆盖原内容。
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后已成功申请的重放也必须失败且不得返回原清册: %+v err=%v", m, err)
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

	// 失败不得触发修复：不挑选其中一个日期、不重新保存来消除重复，
	// 原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 去掉重复的处理日期后，合法记录恢复可用：APP-1 的处理日期仍是损坏前唯一
	// 的 2025-01-10，不会被后写入的 2025-01-11 顶替；无关档案业务不受影响。
	writeStateFile(t, dir, good)
	m, found, err := s.GetManifest("APP-1")
	if err != nil || !found || !m.ProcessedOn.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("恢复后应取回处理日期为 2025-01-10 的原清册: %+v found=%v err=%v", m, found, err)
	}
	replay, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil || !replay.ProcessedOn.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("恢复后相同日期与档案集合重提应取回原清册: %+v err=%v", replay, err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-2", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); err != nil {
		t.Fatalf("恢复后无关档案应能正常办理销毁: %v", err)
	}
}
