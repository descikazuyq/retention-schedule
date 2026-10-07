package retention

import (
	"errors"
	"strings"
	"testing"
)

// 本文件为正式销毁（Destroy）补充“多处阻碍同时出现”时的回归保障，重点锁定
// 调用者实际收到的失败类别与定位信息，而不只是确认办理失败。
//
// 销毁前核对（Check）与正式销毁共用 evaluateArchive 这份办理规则，但两者的
// 呈现方式必须保持既有区别，本组保障把这一区别固定下来：
//
//   - Check 一次列出整批名单中每份档案的全部适用阻碍（不存在、已销毁、未到期、
//     每条未解除冻结各一条），用于办理前看清所有问题；
//   - Destroy 在第一份不能办理的档案处失败，错误只带该档案按固定先后
//     （不存在、已销毁、未到期、未解除冻结）取出的首条阻碍及定位信息。
//
// 因此本组测试对每个失败场景都同时断言：
//  1. errors.Is 可判定的公开错误类别（ErrNotFound / ErrNotExpired /
//     ErrActiveFreeze / ErrDuplicateSelection 等既有类别，不引入新类别）；
//  2. 错误信息指出实际受阻的对象（具体档案编号；未到期带当前截止日与申请处理
//     日期；冻结带具体冻结编号；名单重复带重复的档案编号）；
//  3. 顺序靠后的问题不得出现在错误里（证明选出的是“首个”阻碍而不是任意一个）；
//  4. 失败整体不留痕：返回的清册为零值、不生成该申请编号的清册、名单中原本可以
//     销毁的档案仍未销毁、原有期限（含修订历史）与冻结及解除历史保持原样。

// destroy 是测试中提交一次正式销毁的简写。
func destroy(t *testing.T, s *Store, appID, processedOn string, ids ...string) (Manifest, error) {
	t.Helper()
	return s.Destroy(DestructionRequest{
		ApplicationID: appID,
		ProcessedOn:   MustParseDate(processedOn),
		ArchiveIDs:    ids,
	})
}

// assertDestroyObstruction 断言正式销毁失败的类别与定位信息：
// 错误必须可判定为 want，信息必须包含 wantAll 中的每段文本，且不得包含
// wantNone 中任何一段（用于证明顺序靠后的阻碍没有被误报成首个）。
func assertDestroyObstruction(t *testing.T, err error, want error, wantAll, wantNone []string) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("销毁失败类别应可判定为 %v，得到 %v", want, err)
	}
	if err == nil {
		t.Fatal("销毁应当失败")
	}
	msg := err.Error()
	for _, w := range wantAll {
		if !strings.Contains(msg, w) {
			t.Fatalf("错误信息应指出实际受阻对象 %q，得到 %v", w, err)
		}
	}
	for _, banned := range wantNone {
		if strings.Contains(msg, banned) {
			t.Fatalf("错误信息不应包含顺序靠后的阻碍 %q，得到 %v", banned, err)
		}
	}
}

// assertDestroyFailureLeftNoTrace 断言一次失败的正式销毁没有留下任何痕迹：
// 返回的清册为零值；该申请编号下没有清册；所列档案均仍可查且未被销毁；
// 不改动任何既有清册。
func assertDestroyFailureLeftNoTrace(t *testing.T, s *Store, m Manifest, appID string, notDestroyed []string) {
	t.Helper()
	if m.ApplicationID != "" || m.ProcessedOn != (Date{}) || len(m.Entries) != 0 {
		t.Fatalf("失败的销毁不应返回成功清册，得到 %+v", m)
	}
	if got, found, err := s.GetManifest(appID); err != nil || found {
		t.Fatalf("失败申请 %s 不应生成清册: found=%v manifest=%+v err=%v", appID, found, got, err)
	}
	for _, id := range notDestroyed {
		h, found, err := s.History(id)
		if err != nil || !found {
			t.Fatalf("失败后档案 %s 的历史应原样保留且可查: found=%v err=%v", id, found, err)
		}
		if h.Destroyed || h.ManifestApplicationID != "" || h.Manifest != nil {
			t.Fatalf("名单中的档案 %s 在失败后不应被销毁: %+v", id, h)
		}
	}
}

// TestDestroyFailsAtFirstBlockedArchiveInSubmissionOrder 锁定正式销毁与核对的
// 核心区别：一批合法且不重复的编号中，不同档案分别不存在、未到期或仍有冻结时，
// 正式销毁在名单中最先不能办理的档案处失败，前面满足条件的档案只是被跳过；
// 交换受阻档案在名单中的位置，错误类别与定位信息跟随新的提交顺序。
//
// 档案编号的文本大小、登记先后，以及成功清册条目按编号排序的既有行为，都不能
// 改变“按提交顺序取首个阻碍”这一结果。
func TestDestroyFailsAtFirstBlockedArchiveInSubmissionOrder(t *testing.T) {
	s := openTestStore(t)

	// 登记先后故意与后续提交顺序、编号文本顺序都不同：
	// C-9 最先登记，B-2 其次，A-3 再次，A-1 最后。
	reg(t, s, "C-9", "图纸", "2020-01-01", "2025-01-10") // 到期但有冻结
	reg(t, s, "B-2", "凭证", "2020-01-01", "2026-06-30") // 2025-01-10 尚未到期
	reg(t, s, "A-3", "合同", "2020-01-01", "2025-01-10") // 到期、无冻结，可销毁
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10") // 到期、无冻结，可销毁
	if err := s.Freeze(FreezeInput{ArchiveID: "C-9", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")}); err != nil {
		t.Fatal(err)
	}
	// 编号 M-404 全程不登记，作为“不存在”阻碍；其文本比所有已登记编号都大。

	// 先办一份成功清册：提交顺序与编号相反，清册条目仍按既有规则按编号排序。
	reg(t, s, "D-1", "合同", "2020-01-01", "2024-12-31")
	reg(t, s, "D-2", "凭证", "2020-01-01", "2024-12-31")
	ok, err := destroy(t, s, "APP-0", "2025-01-10", "D-2", "D-1")
	if err != nil {
		t.Fatalf("前置成功清册办理失败: %v", err)
	}
	if len(ok.Entries) != 2 || ok.Entries[0].ID != "D-1" || ok.Entries[1].ID != "D-2" {
		t.Fatalf("成功清册条目应按编号排序: %+v", ok.Entries)
	}

	// 三个受阻位置 M-404（不存在）、B-2（未到期）、C-9（有冻结）轮换到
	// “首个受阻”位置；前面始终放两份可销毁档案，验证它们只被跳过。
	cases := []struct {
		name     string
		appID    string
		ids      []string
		want     error
		wantAll  []string
		wantNone []string
	}{
		{
			name:    "不存在档案最先受阻：跳过前面的可销毁档案",
			appID:   "APP-A",
			ids:     []string{"A-1", "A-3", "M-404", "B-2", "C-9"},
			want:    ErrNotFound,
			wantAll: []string{"M-404", "不存在"},
			// 不能因为 C-9 登记最早、编号更小或同样受阻就改报它，也不能报未到期。
			wantNone: []string{"B-2", "C-9", "尚未到期", "冻结"},
		},
		{
			name:     "交换位置后未到期档案最先受阻：错误跟随提交顺序",
			appID:    "APP-B",
			ids:      []string{"A-1", "A-3", "B-2", "M-404", "C-9"},
			want:     ErrNotExpired,
			wantAll:  []string{"B-2", "尚未到期", "截止日 2026-06-30", "处理日期 2025-01-10"},
			wantNone: []string{"M-404", "C-9", "不存在", "冻结"},
		},
		{
			name:     "交换位置后冻结档案最先受阻：错误跟随提交顺序",
			appID:    "APP-C",
			ids:      []string{"A-1", "A-3", "C-9", "B-2", "M-404"},
			want:     ErrActiveFreeze,
			wantAll:  []string{"C-9", "未解除", "F-1"},
			wantNone: []string{"M-404", "B-2", "不存在", "尚未到期"},
		},
	}
	allStillAround := []string{"A-1", "A-3", "B-2", "C-9"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := destroy(t, s, tc.appID, "2025-01-10", tc.ids...)
			assertDestroyObstruction(t, err, tc.want, tc.wantAll, tc.wantNone)
			assertDestroyFailureLeftNoTrace(t, s, m, tc.appID, allStillAround)
		})
	}

	// 名单中可销毁的档案经历三次失败后仍可销毁；受阻档案的期限与冻结原样保留。
	hB2, _, _ := s.History("B-2")
	if !hB2.RetentionEnd.Equal(MustParseDate("2026-06-30")) || hB2.Destroyed {
		t.Fatalf("未到期档案的期限应原样保留: %+v", hB2)
	}
	hC9, _, _ := s.History("C-9")
	if len(hC9.ActiveFreezes) != 1 || hC9.ActiveFreezes[0].ID != "F-1" || hC9.Destroyed {
		t.Fatalf("冻结档案的未解除冻结应原样保留: %+v", hC9)
	}

	// 对照：同一份名单做销毁前核对时一次列出全部阻碍，保留与正式销毁的区别。
	r := mustCheck(t, s, "APP-CHECK", "2025-01-10", "A-1", "A-3", "M-404", "B-2", "C-9")
	if r.Status != CheckBlocked {
		t.Fatalf("核对应报告存在阻碍，得到 %s", r.Status)
	}
	if len(r.Results) != 5 {
		t.Fatalf("核对应逐份列出全部 5 份档案的结果，得到 %d 份", len(r.Results))
	}
	if kinds := obstructionKinds(r.Results[2]); len(kinds) != 1 || kinds[0] != ObstructionMissing {
		t.Fatalf("核对中 M-404 应列不存在阻碍: %+v", r.Results[2].Obstructions)
	}
	if kinds := obstructionKinds(r.Results[3]); len(kinds) != 1 || kinds[0] != ObstructionNotExpired {
		t.Fatalf("核对中 B-2 应列未到期阻碍: %+v", r.Results[3].Obstructions)
	}
	if kinds := obstructionKinds(r.Results[4]); len(kinds) != 1 || kinds[0] != ObstructionActiveFreeze {
		t.Fatalf("核对中 C-9 应列冻结阻碍: %+v", r.Results[4].Obstructions)
	}
	// 核对不占用申请编号，也不影响此前的成功清册。
	if _, found, _ := s.GetManifest("APP-CHECK"); found {
		t.Fatal("核对不应生成清册或占用申请编号")
	}
	prev, found, err := s.GetManifest("APP-0")
	if err != nil || !found || len(prev.Entries) != 2 || prev.Entries[0].ID != "D-1" {
		t.Fatalf("既有成功清册应保持原样: found=%v err=%v m=%+v", found, err, prev)
	}
}

// TestDestroyNotExpiredPrecedesActiveFreezeWithDates 锁定同一档案同时“未到期”
// 和“有未解除冻结”时的类别先后：先报可识别的未到期错误，并带当前截止日与
// 申请处理日期；处理日期到达截止日当天（即已到期）后，未解除冻结才成为首个
// 阻碍。截止日当天即到期，未到期判断不能被已登记的冻结抢先。
func TestDestroyNotExpiredPrecedesActiveFreezeWithDates(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	// 截止日之前就登记了一条始终未解除的冻结。
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-08")}); err != nil {
		t.Fatal(err)
	}

	// 处理日期 2025-01-09：未到期先于冻结，错误带当前截止日与处理日期。
	m, err := destroy(t, s, "APP-NE-1", "2025-01-09", "A-1")
	assertDestroyObstruction(t, err, ErrNotExpired,
		[]string{"A-1", "尚未到期", "截止日 2025-01-10", "处理日期 2025-01-09"},
		[]string{"F-1", "冻结"})
	assertDestroyFailureLeftNoTrace(t, s, m, "APP-NE-1", []string{"A-1"})

	// 对照：同一状态下核对把未到期与冻结并列列出，区别于正式销毁只取首条。
	r := mustCheck(t, s, "APP-NE-CHK", "2025-01-09", "A-1")
	if r.Status != CheckBlocked {
		t.Fatalf("核对应报告存在阻碍，得到 %s", r.Status)
	}
	if kinds := obstructionKinds(r.Results[0]); len(kinds) != 2 ||
		kinds[0] != ObstructionNotExpired || kinds[1] != ObstructionActiveFreeze {
		t.Fatalf("核对应并列列出未到期与未解除冻结: %+v", r.Results[0].Obstructions)
	}

	// 处理日期到了截止日当天 2025-01-10：已到期，未解除冻结成为首个阻碍。
	m, err = destroy(t, s, "APP-NE-2", "2025-01-10", "A-1")
	assertDestroyObstruction(t, err, ErrActiveFreeze,
		[]string{"A-1", "未解除", "F-1"},
		[]string{"尚未到期"})
	assertDestroyFailureLeftNoTrace(t, s, m, "APP-NE-2", []string{"A-1"})

	// 两次失败后冻结仍未解除，期限未变，没有产生任何清册。
	h, _, _ := s.History("A-1")
	if !h.RetentionEnd.Equal(MustParseDate("2025-01-10")) ||
		len(h.Freezes) != 1 || len(h.ActiveFreezes) != 1 || h.ActiveFreezes[0].ID != "F-1" {
		t.Fatalf("失败后期限与冻结历史应原样保留: %+v", h)
	}
}

// TestDestroyNotExpiredUsesRevisedCurrentEnd 锁定未到期判断必须按当前生效的
// 截止日：期限曾经修订（这里由 2025-01-10 延长到 2026-01-10）的档案，在最初
// 登记的截止日当天提交时仍未到期，错误给出的是修订后的当前截止日 2026-01-10，
// 不能用最初登记的期限把它判成到期、进而改报冻结。
func TestDestroyNotExpiredUsesRevisedCurrentEnd(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-2", "合同", "2020-01-01", "2025-01-10")
	if err := s.Freeze(FreezeInput{ArchiveID: "A-2", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2024-12-20")}); err != nil {
		t.Fatal(err)
	}
	// 成功修订把当前截止日延长到 2026-01-10；修订编号与销毁申请编号互不占用，
	// 下方销毁使用的申请编号均不同于 R-1。
	rec, err := s.Revise(ReviseInput{
		RevisionID:  "R-1",
		ArchiveID:   "A-2",
		OriginalEnd: MustParseDate("2025-01-10"),
		NewEnd:      MustParseDate("2026-01-10"),
		RevisedOn:   MustParseDate("2024-12-01"),
		Reason:      "诉讼需要延长保管",
	})
	if err != nil {
		t.Fatalf("前置修订应成功: %v", err)
	}
	if !rec.NewEnd.Equal(MustParseDate("2026-01-10")) {
		t.Fatalf("修订后新截止日不正确: %+v", rec)
	}

	// 在最初登记的截止日 2025-01-10 提交：按当前截止日 2026-01-10 仍未到期，
	// 必须先报未到期并指出当前截止日，而不是报冻结。
	m, err := destroy(t, s, "APP-RV-1", "2025-01-10", "A-2")
	assertDestroyObstruction(t, err, ErrNotExpired,
		[]string{"A-2", "尚未到期", "截止日 2026-01-10", "处理日期 2025-01-10"},
		[]string{"F-1", "冻结"})
	assertDestroyFailureLeftNoTrace(t, s, m, "APP-RV-1", []string{"A-2"})

	// 处理日期达到修订后的当前截止日：才轮到未解除冻结成为首个阻碍。
	m, err = destroy(t, s, "APP-RV-2", "2026-01-10", "A-2")
	assertDestroyObstruction(t, err, ErrActiveFreeze,
		[]string{"A-2", "未解除", "F-1"},
		[]string{"尚未到期"})
	assertDestroyFailureLeftNoTrace(t, s, m, "APP-RV-2", []string{"A-2"})

	// 两次失败后：最初截止日、修订历史与当前截止日均原样保留，冻结仍未解除。
	h, _, _ := s.History("A-2")
	if !h.InitialEnd.Equal(MustParseDate("2025-01-10")) ||
		!h.RetentionEnd.Equal(MustParseDate("2026-01-10")) ||
		len(h.Revisions) != 1 || h.Revisions[0].RevisionID != "R-1" {
		t.Fatalf("失败后期限与修订历史应原样保留: %+v", h)
	}
	if len(h.ActiveFreezes) != 1 || h.ActiveFreezes[0].ID != "F-1" {
		t.Fatalf("失败后未解除冻结应原样保留: %+v", h.ActiveFreezes)
	}
}

// TestDestroyActiveFreezeNamesEarliestRegisteredOpenFreeze 锁定到期档案存在多条
// 未解除冻结时的定位信息：冻结错误必须指出最早登记且仍未解除的那条。
// 冻结编号的文本大小与冻结日期的先后都不能代替登记顺序；已经解除的记录要被
// 跳过，其余未解除冻结之间的相对登记次序保持不变。
func TestDestroyActiveFreezeNamesEarliestRegisteredOpenFreeze(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	// 登记顺序刻意与冻结编号大小、冻结日期先后都不同：
	//	登记第 1 条：F-9，冻结日期 2025-01-03，仍未解除；
	//	登记第 2 条：F-1，冻结日期 2025-01-02，已解除（应被跳过）；
	//	登记第 3 条：F-5，冻结日期 2025-01-01，仍未解除（日期最早但登记最晚）。
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-9", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-03")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "审计", FrozenOn: MustParseDate("2025-01-02")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "审计结束", ReleasedOn: MustParseDate("2025-01-04")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-5", Reason: "巡检", FrozenOn: MustParseDate("2025-01-01")}); err != nil {
		t.Fatal(err)
	}

	// 已到期：正式销毁必须报告登记最早且仍未解除的 F-9，
	// 不能按编号大小选 F-1/F-5，不能按冻结日期选 F-5，也不能报已解除的 F-1。
	m, err := destroy(t, s, "APP-FRZ-1", "2025-01-10", "A-1")
	assertDestroyObstruction(t, err, ErrActiveFreeze,
		[]string{"A-1", "未解除", "F-9"},
		[]string{"F-1", "F-5"})
	assertDestroyFailureLeftNoTrace(t, s, m, "APP-FRZ-1", []string{"A-1"})

	// 对照：核对列出的未解除冻结按登记次序为 F-9、F-5，已解除的 F-1 被跳过。
	r := mustCheck(t, s, "APP-FRZ-CHK", "2025-01-10", "A-1")
	if r.Status != CheckBlocked {
		t.Fatalf("核对应报告存在阻碍，得到 %s", r.Status)
	}
	var activeIDs []string
	for _, o := range r.Results[0].Obstructions {
		if o.Kind != ObstructionActiveFreeze {
			t.Fatalf("已到期档案核对中不应再有未到期阻碍: %+v", r.Results[0].Obstructions)
		}
		activeIDs = append(activeIDs, o.Freeze.FreezeID)
	}
	if len(activeIDs) != 2 || activeIDs[0] != "F-9" || activeIDs[1] != "F-5" {
		t.Fatalf("未解除冻结应按登记次序列出 F-9、F-5，得到 %v", activeIDs)
	}

	// 失败后全部冻结及解除历史原样保留：F-9、F-5 仍未解除，F-1 仍已解除。
	h, _, _ := s.History("A-1")
	if len(h.Freezes) != 3 {
		t.Fatalf("全部冻结历史应保留 3 条，得到 %d 条", len(h.Freezes))
	}
	if h.Freezes[0].ID != "F-9" || h.Freezes[0].Released {
		t.Fatalf("F-9 应保持登记最早且未解除: %+v", h.Freezes[0])
	}
	if h.Freezes[1].ID != "F-1" || !h.Freezes[1].Released ||
		h.Freezes[1].ReleaseReason != "审计结束" ||
		!h.Freezes[1].ReleasedOn.Equal(MustParseDate("2025-01-04")) {
		t.Fatalf("F-1 的解除历史应原样保留: %+v", h.Freezes[1])
	}
	if h.Freezes[2].ID != "F-5" || h.Freezes[2].Released {
		t.Fatalf("F-5 应保持未解除且登记次序不变: %+v", h.Freezes[2])
	}
	if len(h.ActiveFreezes) != 2 || h.ActiveFreezes[0].ID != "F-9" || h.ActiveFreezes[1].ID != "F-5" {
		t.Fatalf("当前未解除冻结应为 F-9、F-5: %+v", h.ActiveFreezes)
	}
}

// TestDestroyInvalidListRejectedBeforeBusinessObstructions 锁定输入校验先于
// 一切业务判断：同一档案编号只因首尾空白不同而重复时，即使名单同时包含不存在
// 或被冻结的档案，也必须先报名单重复（ErrDuplicateSelection），而不是先报
// 档案层面的任何阻碍；空白编号同样先按空白字段拒绝。
func TestDestroyInvalidListRejectedBeforeBusinessObstructions(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	// A-1 已到期但带着未解除冻结，本身是一个真实的业务阻碍；NOPE 则不存在。
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		appID    string
		ids      []string
		want     error
		wantAll  []string
		wantNone []string
	}{
		{
			name:     "首尾空白差异造成的重复先于不存在与冻结",
			appID:    "APP-DUP-1",
			ids:      []string{"A-1", "\tNOPE\t", "NOPE"},
			want:     ErrDuplicateSelection,
			wantAll:  []string{"NOPE", "重复"},
			wantNone: []string{"不存在", "冻结", "尚未到期"},
		},
		{
			name:     "重复的是被冻结档案本身，仍先报重复",
			appID:    "APP-DUP-2",
			ids:      []string{" A-1 ", "A-1", "NOPE"},
			want:     ErrDuplicateSelection,
			wantAll:  []string{"A-1", "重复"},
			wantNone: []string{"不存在", "冻结", "尚未到期"},
		},
		{
			name:     "空白编号先于其他业务阻碍被拒绝",
			appID:    "APP-DUP-3",
			ids:      []string{"A-1", "  ", "NOPE"},
			want:     ErrBlankField,
			wantAll:  []string{"档案编号", "空白"},
			wantNone: []string{"不存在", "冻结", "重复"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := destroy(t, s, tc.appID, "2025-01-10", tc.ids...)
			assertDestroyObstruction(t, err, tc.want, tc.wantAll, tc.wantNone)
			assertDestroyFailureLeftNoTrace(t, s, m, tc.appID, []string{"A-1"})

			// 输入校验阶段即失败：不存在的编号仍不存在，冻结没有被解除或新增，
			// 失败申请编号下没有清册。
			if h, found, err := s.History("NOPE"); err != nil || found || h.ID != "" {
				t.Fatalf("输入失败不应改变不存在档案的结果: found=%v h=%+v err=%v", found, h, err)
			}
			hA1, _, _ := s.History("A-1")
			if hA1.Destroyed || len(hA1.ActiveFreezes) != 1 || hA1.ActiveFreezes[0].ID != "F-1" {
				t.Fatalf("输入失败后 A-1 的销毁状态与冻结应原样保留: %+v", hA1)
			}
		})
	}
}
