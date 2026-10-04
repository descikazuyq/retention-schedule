package retention

import (
	"errors"
	"strings"
	"testing"
)

// 读取已保存档案时，起算日与当前生效的截止日都必须存在且截止日不得早于
// 起算日。任一档案缺少其中一个日期或顺序不合法，Open 都必须按保存记录损坏
// 失败（ErrCorruptState），错误指出档案编号；缺日期时说明缺的是起算日还是
// 截止日，顺序错误时给出两项日期。已修订或已销毁的档案同样遵守这条规则，
// 原文件保持原样。
func TestOpenRejectsArchiveWithInvalidRetentionPeriod(t *testing.T) {
	// 没有修订记录的旧档案形态：可以缺少最初截止日与修订列表。
	legacy := func(extra string) string {
		return `{"id":"A-1","category":"合同","freezes":[]` + extra + `}`
	}
	// 修订链衔接完整但当前截止日缺失：不能用最初截止日或修订记录顶替。
	revisedMissingEnd := `{"id":"A-1","category":"合同","start":"2020-01-01",` +
		`"initial_end":"2025-01-10","destroyed":false,"freezes":[],` +
		`"revisions":[{"id":"R-1","old_end":"2025-01-10","new_end":"2025-06-30","revised_on":"2025-01-09","reason":"延期"}]}`
	// 已销毁且清册归属、条目与处理日期都合法，但档案缺少当前截止日。
	destroyedMissingEnd := `{"version":1,"archives":{"A-1":` +
		`{"id":"A-1","category":"合同","start":"2020-01-01","initial_end":"2025-01-10",` +
		`"destroyed":true,"manifest_id":"APP-1","freezes":[]}},"manifests":{"APP-1":` +
		`{"application_id":"APP-1","processed_on":"2025-01-10","entries":[` +
		`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10"}]}}}`

	corrupt := map[string]struct {
		state string
		want  []string
	}{
		"缺少起算日": {
			`{"version":1,"archives":{"A-1":` + legacy(`,"end":"2025-01-10"`) + `},"manifests":{}}`,
			[]string{"A-1", "起算日"},
		},
		"缺少当前截止日": {
			`{"version":1,"archives":{"A-1":` + legacy(`,"start":"2020-01-01"`) + `},"manifests":{}}`,
			[]string{"A-1", "截止日"},
		},
		"截止日早于起算日": {
			`{"version":1,"archives":{"A-1":` + legacy(`,"start":"2025-01-10","end":"2025-01-09"`) + `},"manifests":{}}`,
			[]string{"A-1", "2025-01-10", "2025-01-09"},
		},
		"已修订档案缺少当前截止日": {
			`{"version":1,"archives":{"A-1":` + revisedMissingEnd + `},"manifests":{}}`,
			[]string{"A-1", "截止日"},
		},
		"已修订档案截止日早于起算日": {
			`{"version":1,"archives":{"A-1":{"id":"A-1","category":"合同","start":"2025-01-10",` +
				`"end":"2025-01-09","initial_end":"2025-01-10","destroyed":false,"freezes":[],` +
				`"revisions":[{"id":"R-1","old_end":"2025-01-10","new_end":"2025-01-09","revised_on":"2025-01-09","reason":"缩短"}]` +
				`}},"manifests":{}}`,
			[]string{"A-1", "2025-01-10", "2025-01-09"},
		},
		"已销毁档案缺少当前截止日": {
			destroyedMissingEnd,
			[]string{"A-1", "截止日"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.state)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("档案保管期限缺失或顺序不合法时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("错误信息应指出 %q，得到 %v", want, err)
				}
			}
			if got := readStateFile(t, dir); got != tc.state {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}
}

// 没有修订记录的旧档案可以缺少最初截止日和修订列表，只要起算日、当前截止日
// 齐备且顺序合法，就仍按兼容规则查看历史；截止日等于起算日也合法，
// 截止日当天核对即到期。
func TestLegacyArchiveWithoutInitialEndStillOpens(t *testing.T) {
	dir := t.TempDir()
	writeStateFile(t, dir, `{"version":1,"archives":{"A-1":`+
		`{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","destroyed":false,"freezes":[]}`+
		`,"A-2":`+
		`{"id":"A-2","category":"凭证","start":"2025-01-10","end":"2025-01-10","destroyed":false,"freezes":[]}`+
		`},"manifests":{}}`)

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("起算日与当前截止日齐备的旧档案应能打开: %v", err)
	}
	defer s.Close()

	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("旧档案应能查询历史: found=%v err=%v", found, err)
	}
	if !h.InitialEnd.Equal(MustParseDate("2025-01-10")) || !h.RetentionEnd.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("兼容规则应按登记截止日补齐最初截止日: %+v", h)
	}

	// 截止日等于起算日合法，截止日当天核对即到期。
	r, err := s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"A-2"},
	})
	if err != nil {
		t.Fatalf("截止日等于起算日的档案应能核对: %v", err)
	}
	if r.Status != CheckReady {
		t.Fatalf("截止日当天核对应可以办理，得到 %s", r.Status)
	}
}

// 保管库打开后保存内容才出现期限问题的，下一次使用有效输入查询、核对或办理
// 业务也必须报整库记录损坏：即使只操作另一份正常档案，也不得返回正常历史、
// 清册或部分核对结果，办理不得产生业务变更，原保存内容保持原样。
func TestRetentionPeriodCorruptionAfterOpenFailsSubsequentOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	if _, found, err := s.History("A-1"); err != nil || !found {
		t.Fatalf("前置查询应成功: found=%v err=%v", found, err)
	}

	// 打开后另一份档案的当前截止日被抹掉：整库记录损坏。
	corrupted := `{"version":1,"archives":{` +
		`"A-1":{"id":"A-1","category":"合同","start":"2020-01-01","end":"2025-01-10","initial_end":"2025-01-10","destroyed":false,"freezes":[]},` +
		`"A-2":{"id":"A-2","category":"凭证","start":"2020-01-01","initial_end":"2025-01-10","destroyed":false,"freezes":[]}` +
		`},"manifests":{}}`
	writeStateFile(t, dir, corrupted)

	assertCorrupt := func(op string, err error) {
		t.Helper()
		if !errors.Is(err, ErrCorruptState) {
			t.Fatalf("%s 应报整库记录损坏，得到 %v", op, err)
		}
	}
	if _, found, err := s.History("A-1"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("损坏后 History 应失败且不得给出结论: found=%v err=%v", found, err)
	}
	if _, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found {
		t.Fatalf("损坏后 GetManifest 应失败且不得给出结论: found=%v err=%v", found, err)
	}
	_, err = s.Check(CheckRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"A-1"},
	})
	assertCorrupt("Check", err)
	_, err = s.Destroy(DestructionRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"A-1"},
	})
	assertCorrupt("Destroy", err)
	assertCorrupt("Register", s.Register(RegisterInput{
		ID: "A-3", Category: "合同",
		Start: MustParseDate("2020-01-01"), End: MustParseDate("2025-01-10"),
	}))
	assertCorrupt("Freeze", s.Freeze(FreezeInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05"),
	}))

	if got := readStateFile(t, dir); got != corrupted {
		t.Fatalf("损坏后办理不得改动保存内容:\n%q", got)
	}
}
