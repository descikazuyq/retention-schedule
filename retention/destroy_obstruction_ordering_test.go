package retention

import (
	"errors"
	"strings"
	"testing"
)

// 正式销毁在多处阻碍同时出现时的回归保障。
//
// 销毁前核对（Check）一次列出整批全部阻碍，正式销毁（Destroy）则在名单中
// 第一份不能办理的档案处失败——本文件只补充这一区别下的回归保障，不改变
// 任何办理规则与公开错误类别：
//
//   - 失败由名单中最先不能办理的档案决定，前面满足条件的档案被跳过；
//     交换两份受阻档案在名单中的位置，错误跟随新的提交顺序；档案编号大小、
//     登记先后和成功清册的条目排序都不能改变这个结果；
//   - 错误必须指出实际受阻的档案（编号、截止日、冻结编号等定位信息），
//     不能只确认办理失败；
//   - 同一档案既未到期又有未解除冻结时先报未到期，并带当前截止日与处理
//     日期；期限修订过的档案按当前生效的截止日判断；
//   - 到期档案有多条未解除冻结时，冻结错误指出最早登记且仍未解除的那条，
//     冻结编号大小与冻结日期先后都不能代替登记顺序；
//   - 请求本身无效（如仅因首尾空白不同而重复的名单）先于一切业务判断；
//   - 上述失败都不返回成功清册、不生成该申请的清册，名单中原本可以销毁的
//     档案仍未销毁，原有期限、冻结及解除历史保持原样。

// assertDestroyFailedClean 确认一次失败的销毁没有留下任何影响：
// 申请编号没有生成清册，名单中（已登记的）档案都未销毁。
func assertDestroyFailedClean(t *testing.T, s *Store, appID string, ids ...string) {
	t.Helper()
	if _, found, err := s.GetManifest(appID); err != nil || found {
		t.Fatalf("失败的申请 %s 不应生成清册: found=%v err=%v", appID, found, err)
	}
	for _, id := range ids {
		h, found, err := s.History(id)
		if err != nil || !found {
			t.Fatalf("档案 %s 历史应可查: found=%v err=%v", id, found, err)
		}
		if h.Destroyed {
			t.Fatalf("档案 %s 不应被部分销毁", id)
		}
	}
}

// freeze 为档案登记一条冻结，失败即终止测试。
func freeze(t *testing.T, s *Store, archiveID, freezeID, reason, frozenOn string) {
	t.Helper()
	if err := s.Freeze(FreezeInput{
		ArchiveID: archiveID, FreezeID: freezeID,
		Reason: reason, FrozenOn: MustParseDate(frozenOn),
	}); err != nil {
		t.Fatalf("冻结 %s/%s 失败: %v", archiveID, freezeID, err)
	}
}

// release 解除一条冻结，失败即终止测试。
func release(t *testing.T, s *Store, archiveID, freezeID, reason, releasedOn string) {
	t.Helper()
	if err := s.Release(ReleaseInput{
		ArchiveID: archiveID, FreezeID: freezeID,
		Reason: reason, ReleasedOn: MustParseDate(releasedOn),
	}); err != nil {
		t.Fatalf("解除 %s/%s 失败: %v", archiveID, freezeID, err)
	}
}

// 多处阻碍同时出现时，失败由名单中最先不能办理的档案决定：
// 交换受阻档案的位置，错误跟随新的提交顺序；档案编号大小、登记先后和
// 成功清册的条目排序都不能改变结果。错误必须指出实际受阻的档案。
func TestDestroyFailsAtFirstBlockedArchive(t *testing.T) {
	s := openTestStore(t)

	// 登记顺序故意与编号大小不一致。
	reg(t, s, "A-9", "合同", "2020-01-01", "2025-01-10") // 到期、无冻结
	reg(t, s, "A-8", "合同", "2020-01-01", "2025-01-10") // 到期、无冻结
	reg(t, s, "A-6", "合同", "2020-01-01", "2025-01-10") // 到期、无冻结：失败批中应被跳过
	reg(t, s, "A-2", "合同", "2020-01-01", "2025-01-10") // 到期但有未解除冻结
	freeze(t, s, "A-2", "F-1", "诉讼", "2025-01-08")
	reg(t, s, "A-7", "凭证", "2020-01-01", "2026-01-10") // 未到期
	// A-5 不登记：不存在。

	// 先成功销毁一批：提交顺序与编号排序不同，清册条目按编号排序保存。
	// 这一排序不得影响后续失败申请的错误选择。
	m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-OK",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"A-9", "A-8"},
	})
	if err != nil {
		t.Fatalf("先行成功销毁失败: %v", err)
	}
	if len(m.Entries) != 2 || m.Entries[0].ID != "A-8" || m.Entries[1].ID != "A-9" {
		t.Fatalf("成功清册条目应按编号排序保存，得到 %+v", m.Entries)
	}

	processed := MustParseDate("2025-01-10")
	cases := map[string]struct {
		ids      []string
		wantErr  error    // errors.Is 可判定的失败类别
		wantAll  []string // 错误信息必须全部包含（实际受阻档案的定位信息）
		wantNone []string // 错误信息不得包含（名单中位置靠后的阻碍）
	}{
		"满足条件的档案被跳过，失败落在首份不能办理的档案": {
			// A-6 可以销毁但被跳过；A-5 不存在，先于 A-2 的冻结与 A-7 的未到期报告。
			ids:      []string{"A-6", "A-5", "A-2", "A-7"},
			wantErr:  ErrNotFound,
			wantAll:  []string{"A-5"},
			wantNone: []string{"A-2", "A-7", "F-1"},
		},
		"冻结档案排在前时先报冻结": {
			ids:      []string{"A-2", "A-5"},
			wantErr:  ErrActiveFreeze,
			wantAll:  []string{"A-2", "F-1"},
			wantNone: []string{"A-5"},
		},
		"交换后不存在档案排在前，错误跟随新的提交顺序": {
			// 与上一例只交换两份受阻档案的位置；A-5 编号大于 A-2，
			// 编号大小不能改变按提交顺序选出的结果。
			ids:      []string{"A-5", "A-2"},
			wantErr:  ErrNotFound,
			wantAll:  []string{"A-5"},
			wantNone: []string{"A-2", "F-1"},
		},
		"未到期档案排在前时先报未到期并带日期": {
			// A-7 编号大于 A-2、登记也更晚，仍因排在前面而成为首个阻碍。
			ids:      []string{"A-7", "A-2"},
			wantErr:  ErrNotExpired,
			wantAll:  []string{"A-7", "2026-01-10", "2025-01-10"},
			wantNone: []string{"A-2", "F-1"},
		},
		"交换后冻结档案排在前，错误跟随新的提交顺序": {
			ids:      []string{"A-2", "A-7"},
			wantErr:  ErrActiveFreeze,
			wantAll:  []string{"A-2", "F-1"},
			wantNone: []string{"A-7"},
		},
		"编号更大、登记更晚的未到期档案排在前时仍先报它": {
			ids:      []string{"A-7", "A-5"},
			wantErr:  ErrNotExpired,
			wantAll:  []string{"A-7", "2026-01-10"},
			wantNone: []string{"A-5"},
		},
	}
	appSeq := 0
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			appSeq++
			appID := "APP-B" + string(rune('0'+appSeq))
			_, err := s.Destroy(DestructionRequest{
				ApplicationID: appID,
				ProcessedOn:   processed,
				ArchiveIDs:    tc.ids,
			})
			if err == nil {
				t.Fatal("名单中有不能办理的档案时不应销毁成功")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("错误应可判定为 %v，得到 %v", tc.wantErr, err)
			}
			msg := err.Error()
			for _, want := range tc.wantAll {
				if !strings.Contains(msg, want) {
					t.Fatalf("错误信息应包含实际受阻档案的定位信息 %q，得到 %v", want, err)
				}
			}
			for _, banned := range tc.wantNone {
				if strings.Contains(msg, banned) {
					t.Fatalf("错误信息不应包含名单中位置靠后的阻碍 %q，得到 %v", banned, err)
				}
			}
			// 失败不生成清册，名单中原本可以销毁的档案仍未销毁。
			assertDestroyFailedClean(t, s, appID, "A-6", "A-2", "A-7")
		})
	}

	// 原有期限、冻结及解除历史保持原样。
	h, found, err := s.History("A-2")
	if err != nil || !found {
		t.Fatalf("A-2 历史应可查: found=%v err=%v", found, err)
	}
	if len(h.Freezes) != 1 || h.Freezes[0].ID != "F-1" || h.Freezes[0].Released {
		t.Fatalf("A-2 的冻结历史应保持原样，得到 %+v", h.Freezes)
	}
	if len(h.ActiveFreezes) != 1 || h.ActiveFreezes[0].ID != "F-1" {
		t.Fatalf("A-2 的未解除冻结应保持原样，得到 %+v", h.ActiveFreezes)
	}
	h, found, err = s.History("A-7")
	if err != nil || !found {
		t.Fatalf("A-7 历史应可查: found=%v err=%v", found, err)
	}
	if h.RetentionEnd.String() != "2026-01-10" || h.InitialEnd.String() != "2026-01-10" {
		t.Fatalf("A-7 的期限应保持原样，得到 当前=%s 最初=%s", h.RetentionEnd, h.InitialEnd)
	}
}

// 同一档案既未到期又有未解除冻结时，先返回可识别的未到期错误，
// 并带当前截止日和处理日期；期限修订过的档案按当前生效的截止日判断，
// 不能用最初登记的期限选择错误。
func TestDestroyNotExpiredPrecedesActiveFreeze(t *testing.T) {
	s := openTestStore(t)

	// 截止日 2025-01-10，已登记未解除冻结。
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	freeze(t, s, "A-1", "F-1", "诉讼", "2025-01-08")

	// 处理日期 2025-01-09 尚未到截止日：即使已有冻结，也先报未到期，
	// 错误带当前截止日与处理日期。
	_, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1",
		ProcessedOn:   MustParseDate("2025-01-09"),
		ArchiveIDs:    []string{"A-1"},
	})
	if !errors.Is(err, ErrNotExpired) {
		t.Fatalf("未到截止日时应先报未到期 ErrNotExpired，得到 %v", err)
	}
	if errors.Is(err, ErrActiveFreeze) {
		t.Fatalf("未到期错误不应同时判为冻结错误，得到 %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"A-1", "2025-01-10", "2025-01-09"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("未到期错误应带档案编号、当前截止日与处理日期（%q），得到 %v", want, err)
		}
	}
	if strings.Contains(msg, "F-1") {
		t.Fatalf("未到期优先时不应报告冻结编号，得到 %v", err)
	}
	assertDestroyFailedClean(t, s, "APP-1", "A-1")

	// 处理日期到了截止日当天，冻结尚未解除才成为首个阻碍。
	_, err = s.Destroy(DestructionRequest{
		ApplicationID: "APP-2",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"A-1"},
	})
	if !errors.Is(err, ErrActiveFreeze) {
		t.Fatalf("截止日当天冻结未解除时应报 ErrActiveFreeze，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "F-1") {
		t.Fatalf("冻结错误应指出冻结编号 F-1，得到 %v", err)
	}
	assertDestroyFailedClean(t, s, "APP-2", "A-1")

	// 期限曾经延长的档案：按当前生效的截止日判断，不按最初登记的期限。
	reg(t, s, "A-2", "合同", "2020-01-01", "2025-01-10")
	freeze(t, s, "A-2", "F-1", "诉讼", "2025-01-08")
	if _, err := s.Revise(ReviseInput{
		RevisionID: "REV-1", ArchiveID: "A-2",
		OriginalEnd: MustParseDate("2025-01-10"), NewEnd: MustParseDate("2025-03-10"),
		RevisedOn: MustParseDate("2025-01-09"), Reason: "延期",
	}); err != nil {
		t.Fatalf("延长 A-2 期限失败: %v", err)
	}
	// 处理日期已过最初截止日 2025-01-10，但未到当前截止日 2025-03-10：
	// 必须报未到期（带当前截止日），不能按最初期限改报冻结。
	_, err = s.Destroy(DestructionRequest{
		ApplicationID: "APP-3",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"A-2"},
	})
	if !errors.Is(err, ErrNotExpired) {
		t.Fatalf("修订后期限未到时应报 ErrNotExpired，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "2025-03-10") {
		t.Fatalf("未到期错误应带当前生效的截止日 2025-03-10，得到 %v", err)
	}
	assertDestroyFailedClean(t, s, "APP-3", "A-2")

	// 期限曾经缩短的档案：同样按当前生效的截止日判断。
	reg(t, s, "A-3", "合同", "2020-01-01", "2025-06-10")
	freeze(t, s, "A-3", "F-1", "审计", "2025-01-08")
	if _, err := s.Revise(ReviseInput{
		RevisionID: "REV-2", ArchiveID: "A-3",
		OriginalEnd: MustParseDate("2025-06-10"), NewEnd: MustParseDate("2025-01-05"),
		RevisedOn: MustParseDate("2025-01-06"), Reason: "缩短",
	}); err != nil {
		t.Fatalf("缩短 A-3 期限失败: %v", err)
	}
	// 处理日期未到最初截止日 2025-06-10，但已过当前截止日 2025-01-05：
	// 必须报冻结，不能按最初期限改报未到期。
	_, err = s.Destroy(DestructionRequest{
		ApplicationID: "APP-4",
		ProcessedOn:   MustParseDate("2025-01-10"),
		ArchiveIDs:    []string{"A-3"},
	})
	if !errors.Is(err, ErrActiveFreeze) {
		t.Fatalf("缩短期限后已到期，应报 ErrActiveFreeze，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "F-1") {
		t.Fatalf("冻结错误应指出冻结编号 F-1，得到 %v", err)
	}
	assertDestroyFailedClean(t, s, "APP-4", "A-3")

	// 失败不留影响：期限与修订历史保持原样。
	h, found, err := s.History("A-2")
	if err != nil || !found {
		t.Fatalf("A-2 历史应可查: found=%v err=%v", found, err)
	}
	if h.InitialEnd.String() != "2025-01-10" || h.RetentionEnd.String() != "2025-03-10" ||
		len(h.Revisions) != 1 || h.Revisions[0].RevisionID != "REV-1" {
		t.Fatalf("A-2 的期限与修订历史应保持原样，得到 %+v", h)
	}
	h, found, err = s.History("A-3")
	if err != nil || !found {
		t.Fatalf("A-3 历史应可查: found=%v err=%v", found, err)
	}
	if h.InitialEnd.String() != "2025-06-10" || h.RetentionEnd.String() != "2025-01-05" ||
		len(h.Revisions) != 1 || h.Revisions[0].RevisionID != "REV-2" {
		t.Fatalf("A-3 的期限与修订历史应保持原样，得到 %+v", h)
	}
}

// 到期档案有多条未解除冻结时，冻结错误指出最早登记且仍未解除的那条：
// 冻结编号大小和冻结日期先后不能代替登记顺序；已有解除记录被跳过，
// 其余未解除冻结的相对次序保持不变。
func TestDestroyFreezeErrorFollowsRegistrationOrder(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	// 登记顺序 F-0、F-9、F-5、F-1：编号大小与冻结日期先后都与登记顺序不一致。
	freeze(t, s, "A-1", "F-0", "已结案", "2024-12-01")
	freeze(t, s, "A-1", "F-9", "诉讼", "2025-03-01") // 冻结日期最晚
	freeze(t, s, "A-1", "F-5", "审计", "2025-01-01") // 冻结日期最早
	freeze(t, s, "A-1", "F-1", "保全", "2025-02-01")
	// F-0 早已解除：登记在最先也必须被跳过。
	release(t, s, "A-1", "F-0", "结案", "2024-12-02")

	processed := MustParseDate("2025-01-10")
	destroy := func(appID string) error {
		_, err := s.Destroy(DestructionRequest{
			ApplicationID: appID,
			ProcessedOn:   processed,
			ArchiveIDs:    []string{"A-1"},
		})
		return err
	}

	// 最早登记且仍未解除的是 F-9：编号更小、日期更早的 F-5/F-1 都不能顶替，
	// 已解除的 F-0 被跳过。
	err := destroy("APP-1")
	if !errors.Is(err, ErrActiveFreeze) {
		t.Fatalf("应报 ErrActiveFreeze，得到 %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "F-9") {
		t.Fatalf("冻结错误应指出最早登记的未解除冻结 F-9，得到 %v", err)
	}
	for _, banned := range []string{"F-5", "F-1", "F-0"} {
		if strings.Contains(msg, banned) {
			t.Fatalf("冻结错误不应包含 %q，得到 %v", banned, err)
		}
	}
	assertDestroyFailedClean(t, s, "APP-1", "A-1")

	// 解除 F-9 后，其余未解除冻结的相对次序不变：下一个报告 F-5。
	release(t, s, "A-1", "F-9", "结案", "2025-03-02")
	err = destroy("APP-2")
	if !errors.Is(err, ErrActiveFreeze) {
		t.Fatalf("应报 ErrActiveFreeze，得到 %v", err)
	}
	msg = err.Error()
	if !strings.Contains(msg, "F-5") {
		t.Fatalf("解除 F-9 后应报告 F-5，得到 %v", err)
	}
	for _, banned := range []string{"F-1", "F-9", "F-0"} {
		if strings.Contains(msg, banned) {
			t.Fatalf("冻结错误不应包含 %q，得到 %v", banned, err)
		}
	}
	assertDestroyFailedClean(t, s, "APP-2", "A-1")

	// 再解除 F-5，最后报告 F-1。
	release(t, s, "A-1", "F-5", "完成", "2025-01-02")
	err = destroy("APP-3")
	if !errors.Is(err, ErrActiveFreeze) {
		t.Fatalf("应报 ErrActiveFreeze，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "F-1") {
		t.Fatalf("解除 F-9、F-5 后应报告 F-1，得到 %v", err)
	}
	assertDestroyFailedClean(t, s, "APP-3", "A-1")

	// 冻结及解除历史保持原样：四条记录都在，解除标记与日期不变。
	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("A-1 历史应可查: found=%v err=%v", found, err)
	}
	if len(h.Freezes) != 4 {
		t.Fatalf("全部冻结历史应保持完整，得到 %+v", h.Freezes)
	}
	wantReleased := map[string]bool{"F-0": true, "F-9": true, "F-5": true, "F-1": false}
	for _, f := range h.Freezes {
		want, ok := wantReleased[f.ID]
		if !ok || f.Released != want {
			t.Fatalf("冻结 %s 的解除状态应保持原样（期望 released=%v），得到 %+v", f.ID, want, f)
		}
	}
	if len(h.ActiveFreezes) != 1 || h.ActiveFreezes[0].ID != "F-1" {
		t.Fatalf("仅 F-1 仍未解除，得到 %+v", h.ActiveFreezes)
	}
}

// 请求本身无效时仍先拒绝输入：同一档案编号只因首尾空白不同而重复，
// 即使名单同时包含不存在或被冻结的档案，也应先报名单重复。
func TestDestroyDuplicateSelectionBeatsObstructions(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	freeze(t, s, "A-1", "F-1", "诉讼", "2025-01-08")
	// GHOST 不登记：不存在。

	cases := map[string][]string{
		// 重复项紧挨，名单里同时有不存在的档案与被冻结的档案。
		"重复与不存在、冻结同时出现": {"A-1", "GHOST", " A-1"},
		// 重复项（仅首尾空白不同）排在受阻档案之后：输入校验仍先于业务判断。
		"重复项排在受阻档案之后": {"GHOST", "A-1", "A-1\t"},
	}
	appSeq := 0
	for name, ids := range cases {
		t.Run(name, func(t *testing.T) {
			appSeq++
			appID := "APP-D" + string(rune('0'+appSeq))
			_, err := s.Destroy(DestructionRequest{
				ApplicationID: appID,
				ProcessedOn:   MustParseDate("2025-01-10"),
				ArchiveIDs:    ids,
			})
			if !errors.Is(err, ErrDuplicateSelection) {
				t.Fatalf("名单重复应先报 ErrDuplicateSelection，得到 %v", err)
			}
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrActiveFreeze) {
				t.Fatalf("名单重复时不应改报业务阻碍，得到 %v", err)
			}
			if !strings.Contains(err.Error(), "A-1") {
				t.Fatalf("重复错误应指出重复的档案编号 A-1，得到 %v", err)
			}
			assertDestroyFailedClean(t, s, appID, "A-1")
		})
	}

	// 冻结历史保持原样。
	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("A-1 历史应可查: found=%v err=%v", found, err)
	}
	if len(h.ActiveFreezes) != 1 || h.ActiveFreezes[0].ID != "F-1" {
		t.Fatalf("A-1 的未解除冻结应保持原样，得到 %+v", h.ActiveFreezes)
	}
}
