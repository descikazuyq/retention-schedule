package retention

import (
	"errors"
	"strings"
	"testing"
)

// 同一份清册自己的保存内容中，处理日期 processed_on 最多只能出现一次：普通解码
// 对同名字段只保留最后一个值，一份清册若先写处理日期 2025-01-10、后写
// 2025-01-11，而档案截止日为 2025-01-10、两个日期都满足到期规则、其他内容均
// 合法时，保管库仍能打开，查询到的销毁日期却取决于两处的保存顺序。Open 必须按
// 保存记录损坏失败（ErrCorruptState），错误指出清册申请编号，并说明重复的是
// 处理日期 processed_on 本身，不能误报为申请编号对应了多份清册；两处日期不同、
// 完全相同或其中一处为 null 都按同一规则拒绝，不挑选某个日期继续使用，交换
// 保存顺序也不改变拒绝结果，原文件保持原样。
func TestOpenRejectsDuplicateProcessedOnField(t *testing.T) {
	// manifest 构造一份已关闭清册记录，processedOnFields 是原样拼接进记录的
	// 处理日期字段（可以写一个或多个，字段名写法与取值任意）；条目与档案登记
	// 内容一致，截止日 2025-01-10，两个处理日期 2025-01-10 与 2025-01-11 都
	// 满足到期规则。
	manifest := func(processedOnFields ...string) string {
		fields := `"application_id":"APP-1"`
		for _, f := range processedOnFields {
			fields += "," + f
		}
		fields += `,"entries":[{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}]`
		return `{` + fields + `}`
	}
	state := func(manifestRec string) string {
		return `{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1","freezes":[]}},"manifests":{"APP-1":` + manifestRec + `}}`
	}

	corrupt := map[string]string{
		// 任务场景：截止日 2025-01-10，先写处理日期 2025-01-10、后写
		// 2025-01-11，普通解码会采用后一个日期，取回的销毁日期随保存顺序变化。
		"先写截止日当天后写次日": state(manifest(`"processed_on":"2025-01-10"`, `"processed_on":"2025-01-11"`)),
		"先写次日后写截止日当天": state(manifest(`"processed_on":"2025-01-11"`, `"processed_on":"2025-01-10"`)),
		"两处日期完全相同":    state(manifest(`"processed_on":"2025-01-10"`, `"processed_on":"2025-01-10"`)),
		"第二处为null":    state(manifest(`"processed_on":"2025-01-10"`, `"processed_on":null`)),
		"第一处为null":    state(manifest(`"processed_on":null`, `"processed_on":"2025-01-10"`)),
		"两处都为null":    state(manifest(`"processed_on":null`, `"processed_on":null`)),
		"大小写写法混用重复":   state(manifest(`"Processed_On":"2025-01-10"`, `"processed_on":"2025-01-11"`)),
		"大小写写法混用顺序相反": state(manifest(`"processed_on":"2025-01-10"`, `"Processed_On":"2025-01-11"`)),
		"处理日期出现三次":    state(manifest(`"processed_on":"2025-01-10"`, `"processed_on":"2025-01-11"`, `"processed_on":"2025-01-12"`)),
	}
	// 转义写法需要字面反斜杠，单独构造：反引号字符串里的 \u0065 是字面的
	// 反斜杠+u0065，JSON 解码后才是字符 e，与直接写出的 processed_on 是同一个字段名。
	corrupt["Unicode转义字段名与直接字段名重复"] = state(manifest(`"proc\u0065ssed_on":"2025-01-10"`, `"processed_on":"2025-01-11"`))
	corrupt["Unicode转义字段名与直接字段名重复顺序相反"] = state(manifest(`"processed_on":"2025-01-10"`, `"proc\u0065ssed_on":"2025-01-11"`))

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
				t.Fatalf("错误信息应说明重复的是处理日期 processed_on，得到 %v", err)
			}
			// 不得误报为申请编号对应多份清册：重复的是同一份清册内的处理日期字段。
			if strings.Contains(msg, "多份清册") {
				t.Fatalf("错误信息不应误报为申请编号对应多份清册，得到 %v", err)
			}
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}

	// 对照组：单个处理日期（含现有能识别的大小写写法与转义写法单独出现）继续
	// 可读；多份清册各自保存一个处理日期属于正常记录，不合在一起计数。
	t.Run("合法记录对照组", func(t *testing.T) {
		valid := map[string]string{
			"单个处理日期":                state(manifest(`"processed_on":"2025-01-10"`)),
			"截止日当天的处理日期":            state(manifest(`"processed_on":"2025-01-10"`)),
			"大小写写法Processed_On单独出现": state(manifest(`"Processed_On":"2025-01-10"`)),
			// 多份清册各自保存一个处理日期，不合在一起计数。
			"多份清册各自一个处理日期": `{"version":1,"archives":{` +
				`"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":true,"manifest_id":"APP-1","freezes":[]},` +
				`"A-2":{"id":"A-2","category":"凭证","start":"2020-01-01","end":"2025-06-30","initial_end":"2025-06-30","destroyed":true,"manifest_id":"APP-2","freezes":[]}` +
				`},"manifests":{` +
				`"APP-1":{"application_id":"APP-1","processed_on":"2025-01-10","entries":[{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}]},` +
				`"APP-2":{"application_id":"APP-2","processed_on":"2025-06-30","entries":[{"id":"A-2","category":"凭证","start":"2020-01-01","end":"2025-06-30"}]}` +
				`}}`,
		}
		// 转义写法需要字面反斜杠，单独构造。
		valid["Unicode转义字段名单独出现"] = state(manifest(`"proc\u0065ssed_on":"2025-01-10"`))
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

	// 合法清册的既有行为不变：可按申请编号取回、可通过档案历史查看，也可用相同
	// 日期和档案集合重提原申请取回原清册；处理日期缺失、为 null 或早于条目
	// 截止日的记录仍按既有规则拒绝。
	t.Run("合法清册行为不变", func(t *testing.T) {
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
			t.Fatalf("已关闭清册应完整可查: %+v found=%v err=%v", m, found, err)
		}
		h, found, err := s.History("A-1")
		if err != nil || !found || !h.Destroyed || h.Manifest == nil ||
			!h.Manifest.ProcessedOn.Equal(MustParseDate("2025-01-10")) {
			t.Fatalf("档案历史中的清册应完整可查: %+v found=%v err=%v", h, found, err)
		}
		// 用相同日期和档案集合重提原申请，取回原清册。
		replay, err := s.Destroy(DestructionRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
		})
		if err != nil || replay.ApplicationID != "APP-1" ||
			!replay.ProcessedOn.Equal(MustParseDate("2025-01-10")) {
			t.Fatalf("重提原申请应取回原清册: %+v err=%v", replay, err)
		}
	})
}

// 保管库打开后保存内容才出现重复的处理日期：下一次使用有效输入查询历史、取回清册、
// 销毁前核对与各项办理都必须按整库记录损坏失败——即使操作的是另一份没有问题的
// 档案或另一份正常清册——不返回正常历史、清册或部分核对报告，不改变档案状态或
// 生成清册，也不重新保存来消除重复，原保存内容保持原样。
func TestDuplicateProcessedOnFieldAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-2", FreezeID: "F-2", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatal(err)
	}

	// 在 APP-1 的清册里再写入一个处理日期：先保留原来的 2025-01-10，后面再写
	// 2025-01-11——普通解码会采用后一个日期，取回的销毁日期随保存顺序变化。
	good := readStateFile(t, dir)
	corrupt := strings.Replace(good, `"processed_on": "2025-01-10"`,
		`"processed_on": "2025-01-10", "processed_on": "2025-01-11"`, 1)
	if corrupt == good {
		t.Fatal("未找到 APP-1 清册的处理日期位置，测试夹具失效")
	}
	writeStateFile(t, dir, corrupt)
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，错误指出清册申请编号，并说明重复的是处理日期 processed_on。
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

	// 已打开实例：即使本次只操作没有问题的正常档案 A-2，也必须按整库损坏失败。
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
	// 已成功申请的幂等重放同样不能取回原清册。
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后已成功申请的重放也必须失败且不得返回原清册: %+v err=%v", m, err)
	}
	if err := s.Register(RegisterInput{
		ID: "A-3", Category: "单据", Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-06-30"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Register 必须失败: %v", err)
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-2", FreezeID: "F-X", Reason: "保全", FrozenOn: MustParseDate("2025-06-01"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Freeze 必须失败: %v", err)
	}
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-2", FreezeID: "F-2", Reason: "结案", ReleasedOn: MustParseDate("2025-06-01"),
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

	// 失败不得触发修复：不挑选某个日期、不重新保存来消除重复，原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 去掉重复的处理日期后，合法记录恢复可用：清册仍按申请编号取回，处理日期
	// 仍是损坏前的 2025-01-10，重提原申请仍取回原清册，无关档案业务不受影响。
	writeStateFile(t, dir, good)
	m, found, err := s.GetManifest("APP-1")
	if err != nil || !found || m.ApplicationID != "APP-1" ||
		!m.ProcessedOn.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("恢复后清册应按原处理日期取回: %+v found=%v err=%v", m, found, err)
	}
	replay, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil || replay.ApplicationID != "APP-1" ||
		!replay.ProcessedOn.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("恢复后重提原申请应取回原清册: %+v err=%v", replay, err)
	}
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-2", FreezeID: "F-2", Reason: "结案", ReleasedOn: MustParseDate("2025-06-01"),
	}); err != nil {
		t.Fatalf("恢复后无关档案应能正常办理解除: %v", err)
	}
}
