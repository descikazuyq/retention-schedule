package retention

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unicodeDash 是 JSON 文本层面的 Unicode 转义（连字符，解码后即 "-"）。
// 拆成两段拼接，避免源码里直接出现反斜杠加 u 的转义序列。
const unicodeDash = `\` + `u002D`

// 构造一份登记对象文本：freezes 为空时写空列表。
func registrationBody(id, category, end string, freezes ...string) string {
	return `"id":"` + id + `","category":"` + category + `","start":"2020-01-01","end":"` + end +
		`","initial_end":"` + end + `","destroyed":false,"freezes":[` + strings.Join(freezes, ",") + `]`
}

// 构造 archives 对象中的一个成员；rawKey 为 JSON 文本层面的键（可含转义）。
func archiveEntry(rawKey, bodyText string) string {
	return `"` + rawKey + `":{` + bodyText + `}`
}

func duplicateRegistrationState(entriesJSON string) string {
	return `{"version":1,"archives":{` + entriesJSON + `},"manifests":{}}`
}

// 保存的档案集合对象中同一编号直接出现两次时，Open 必须按保存记录损坏失败
// （ErrCorruptState）：错误给出重复的档案编号并说明登记重复，原文件保持原样。
// 两份内容完全一致、类别日期相同但一份带未解除冻结一份冻结列表为空（题述场景：
// 折叠后只剩后一份，按截止日核对会误判可以办理）、类别或期限不同，结果相同；
// 不能合并、忽略，也不能按期限长短或冻结状态挑选可信记录。
func TestOpenRejectsDuplicateArchiveRegistration(t *testing.T) {
	activeFreeze := `{"id":"F-1","reason":"诉讼","frozen_on":"2025-01-05","released":false}`

	corrupt := map[string]struct {
		state   string
		wantAll []string // 错误信息必须全部包含
	}{
		"两份登记内容完全一致": {
			duplicateRegistrationState(
				archiveEntry("A-1", registrationBody("A-1", "合同", "2025-01-10")) + "," +
					archiveEntry("A-1", registrationBody("A-1", "合同", "2025-01-10"))),
			[]string{"A-1", "登记重复"},
		},
		"前一份带未解除冻结后一份冻结列表为空": {
			// 题述场景：json.Unmarshal 折叠后只留下没有冻结的后一份，
			// 按截止日 2025-01-10 核对会误报可以办理，正式销毁也可能成功。
			duplicateRegistrationState(
				archiveEntry("A-1", registrationBody("A-1", "合同", "2025-01-10", activeFreeze)) + "," +
					archiveEntry("A-1", registrationBody("A-1", "合同", "2025-01-10"))),
			[]string{"A-1", "登记重复"},
		},
		"两份类别与期限不同": {
			duplicateRegistrationState(
				archiveEntry("A-1", registrationBody("A-1", "合同", "2025-01-10")) + "," +
					archiveEntry("A-1", registrationBody("A-1", "凭证", "2026-06-30", activeFreeze))),
			[]string{"A-1", "登记重复"},
		},
		"多份档案中第二组编号重复": {
			duplicateRegistrationState(
				archiveEntry("A-1", registrationBody("A-1", "合同", "2025-01-10")) + "," +
					archiveEntry("A-2", registrationBody("A-2", "凭证", "2025-06-30")) + "," +
					archiveEntry("A-2", registrationBody("A-2", "凭证", "2025-06-30"))),
			[]string{"A-2", "登记重复"},
		},
		"直接写出与Unicode转义写出的同一编号": {
			// 第二个键写作 A 加转义连字符加 1（A<->1），解码后仍是 A-1。
			duplicateRegistrationState(
				archiveEntry("A-1", registrationBody("A-1", "合同", "2025-01-10", activeFreeze)) + "," +
					archiveEntry("A"+unicodeDash+"1", registrationBody("A-1", "合同", "2025-01-10"))),
			[]string{"A-1", "登记重复"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("同一档案编号登记两次时不应打开成功")
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

	// 对照组：不同编号各登记一次（即使其中一个编号用 Unicode 转义写出，
	// 解码后仍与另一个不同）必须正常打开；不同登记内容中重复出现 id、
	// freezes 等同名字段是正常保存格式，不能误判成档案编号重复。
	t.Run("合法记录对照组", func(t *testing.T) {
		valid := map[string]string{
			"两个不同编号各自登记一次": duplicateRegistrationState(
				archiveEntry("A-1", registrationBody("A-1", "合同", "2025-01-10", activeFreeze)) + "," +
					archiveEntry("A-2", registrationBody("A-2", "凭证", "2025-06-30"))),
			"一个编号转义写出但解码后仍是不同编号": duplicateRegistrationState(
				// 第二个键 A 加转义连字符加 2 解码为 A-2，与 A-1 不同。
				archiveEntry("A-1", registrationBody("A-1", "合同", "2025-01-10")) + "," +
					archiveEntry("A"+unicodeDash+"2", registrationBody("A-2", "凭证", "2025-06-30", activeFreeze))),
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
				h1, found, err := s.History("A-1")
				if err != nil || !found || h1.ID != "A-1" {
					t.Fatalf("A-1 应能正常查询: found=%v err=%v %+v", found, err, h1)
				}
				// 转义写出的编号解码后按实际文本 A-2 识别。
				h2, found, err := s.History("A-2")
				if err != nil || !found || h2.ID != "A-2" {
					t.Fatalf("A-2（含转义键）应能按解码后编号查询: found=%v err=%v %+v", found, err, h2)
				}
			})
		}
	})
}

// 档案集合保存为 null 与字段缺失等价，按空库打开并可正常登记，
// 不应受重复键校验影响。
func TestNullArchivesCollectionStillOpensEmpty(t *testing.T) {
	dir := t.TempDir()
	writeStateFile(t, dir, `{"version":1,"archives":null,"manifests":null}`)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("archives 为 null 时应按空库打开: %v", err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	if _, found, err := s.History("A-1"); err != nil || !found {
		t.Fatalf("空库登记后应能查询: found=%v err=%v", found, err)
	}
}

// 保管库打开后保存内容才出现同一编号的两份登记：下一次使用有效输入查询历史、
// 取回清册（含已成功申请的幂等重放）、销毁前核对或正式办理（含操作另一份正常
// 档案）都必须按整库记录损坏失败——不返回正常历史、清册或部分报告，不写入新
// 记录、不改变销毁状态、不生成清册，原保存内容保持原样。
func TestDuplicateRegistrationAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-06-30")
	reg(t, s, "A-3", "单据", "2020-01-01", "2025-01-10")
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05"),
	}); err != nil {
		t.Fatal(err)
	}
	// A-3 先合法销毁，供损坏后验证“取回清册”同样整库失败。
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-3", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-3"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.GetManifest("APP-3"); err != nil || !found {
		t.Fatalf("损坏前清册应可取回: found=%v err=%v", found, err)
	}

	// 重新编码当前状态后，在档案集合最前面插入第二份 A-1 登记（冻结列表为空）。
	// 不能靠 map 重新编码制造重复键——json.Marshal 会先折叠，必须做文本插入。
	var doc map[string]any
	if err := json.Unmarshal([]byte(readStateFile(t, dir)), &doc); err != nil {
		t.Fatal(err)
	}
	a1, err := json.Marshal(doc["archives"].(map[string]any)["A-1"])
	if err != nil {
		t.Fatal(err)
	}
	// 去掉冻结列表，模拟“随后又保存一份期限相同但没有冻结的 A-1”。
	var dup map[string]any
	if err := json.Unmarshal(a1, &dup); err != nil {
		t.Fatal(err)
	}
	dup["freezes"] = []any{}
	dupJSON, err := json.Marshal(dup)
	if err != nil {
		t.Fatal(err)
	}
	base, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	const archivesHead = `"archives": {`
	idx := strings.Index(string(base), archivesHead)
	if idx < 0 {
		t.Fatal("未在保存内容中定位到档案集合")
	}
	corruptContent := string(base[:idx+len(archivesHead)]) + "\n    \"A-1\": " + string(dupJSON) + ",\n" +
		string(base[idx+len(archivesHead):])
	writeStateFile(t, dir, corruptContent)

	// 重新打开必须失败，错误指出重复编号并说明登记重复。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("档案集合出现同号登记时，打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) ||
		!strings.Contains(err.Error(), "A-1") ||
		!strings.Contains(err.Error(), "登记重复") {
		t.Fatalf("打开错误应为 ErrCorruptState 并指出 A-1 登记重复，得到 %v", err)
	}

	// 已打开实例：即使本次只操作与重复登记无关的正常档案 A-2，
	// 也必须按整库损坏失败。
	if h, found, err := s.History("A-2"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后查询无关档案也必须失败且无结论: found=%v err=%v", found, err)
	}
	if h, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found || h.ID != "" {
		t.Fatalf("损坏后 History 必须失败且不得给出被后一份覆盖后的登记: found=%v err=%v", found, err)
	}
	if m, found, err := s.GetManifest("APP-3"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("损坏后 GetManifest 必须失败: found=%v err=%v %+v", found, err, m)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出可以办理等结论或部分报告: %+v err=%v", r, err)
	}
	// 题述的误判场景也必须在核对阶段被挡住：正是 A-1、正是截止日当天，
	// 也不能返回“可以办理”。
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-8", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后核对 A-1 必须失败，不能因冻结被覆盖而显示可以办理: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-06-30"), ArchiveIDs: []string{"A-2"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
	}
	// 已成功申请的幂等重放同样不能取回原清册。
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-3", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-3"},
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
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-08"),
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

	// 失败不得触发修复或重新保存：重复登记原样保留。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}

	// 恢复唯一编号后（写回损坏前由程序保存的内容），前一份登记的未解除冻结
	// 仍在，截止日当天核对继续显示冻结阻碍，正式销毁不能成功；既有清册与
	// 无关档案业务不受影响。
	if err := os.WriteFile(filepath.Join(dir, stateFileName), base, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := s.Check(CheckRequest{
		ApplicationID: "APP-8", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil || r.Status != CheckBlocked || len(r.Results) != 1 ||
		len(r.Results[0].Obstructions) != 1 ||
		r.Results[0].Obstructions[0].Kind != ObstructionActiveFreeze {
		t.Fatalf("恢复后 A-1 的未解除冻结必须仍阻止销毁: %+v err=%v", r, err)
	}
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-8", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); !errors.Is(err, ErrActiveFreeze) {
		t.Fatalf("恢复后正式销毁必须仍被冻结挡住，得到 %v", err)
	}
	if m, found, err := s.GetManifest("APP-3"); err != nil || !found || m.ApplicationID != "APP-3" {
		t.Fatalf("恢复后既有清册应继续完整可查: found=%v err=%v %+v", found, err, m)
	}
}
