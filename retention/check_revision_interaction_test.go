package retention

import (
	"errors"
	"strings"
	"testing"
)

// TestCheckThenReviseBeforeSubmit 保护“销毁前核对”“期限修订”“正式销毁”
// 几项已有行为之间的关系：
//
//   - 核对报告只说明核对当时的情况，不预占档案、不占用申请编号，
//     也不保证稍后的正式提交一定成功；
//   - 正式提交必须采用提交时当前生效的保管截止日（最后一次成功修订的
//     新截止日），核对之后期限被延长会挡住整批销毁，另一份符合条件的
//     档案也不能先行成功；
//   - 再把期限缩短到申请处理日期当天时，截止日当天即到期，同一申请
//     编号、处理日期和名单可先核对为可以办理、再整体提交成功，清册
//     条目采用最终生效值，既不是最初登记值也不是中间延长过的期限；
//   - 修订记录中的办理日期只是记录信息：后一次成功修订的办理日期
//     早于前一次，仍以最后一次成功提交的新截止日为准；
//   - 调用者此前拿到的报告保留各自核对时的截止日与结论，不因后续
//     修订或销毁被改写；新查询反映当前保存状态，销毁后历史仍能看到
//     最初截止日、最终截止日和按成功顺序保留的两次修订，与清册相符。
func TestCheckThenReviseBeforeSubmit(t *testing.T) {
	s := openTestStore(t)
	processedOn := MustParseDate("2025-01-10")
	initialEnd := MustParseDate("2025-01-05")  // 被修订档案的最初截止日，早于处理日期
	extendedEnd := MustParseDate("2025-03-10") // 中间延长到的期限，晚于处理日期
	finalEnd := processedOn                    // 最终缩短到处理日期当天：当天即到期
	ids := []string{"A-1", "A-2"}

	// 同一批两份没有冻结、尚未销毁的档案：
	// A-1 最初截止日早于处理日期，A-2 截止日即处理日期，处理日期不早于任一截止日。
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-05")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")

	// 首次核对：整批可以办理。
	first := mustCheck(t, s, "APP-1", "2025-01-10", ids...)
	if first.Status != CheckReady {
		t.Fatalf("首次核对应可以办理，得到 %s: %+v", first.Status, first.Results)
	}
	if first.Manifest != nil {
		t.Fatalf("尚未成功使用的申请不应附清册: %+v", first.Manifest)
	}
	if len(first.Results) != 2 {
		t.Fatalf("应逐份列出两份档案: %+v", first.Results)
	}
	for i, wantEnd := range []string{"2025-01-05", "2025-01-10"} {
		r := first.Results[i]
		if !r.Exists || r.Destroyed || len(r.Obstructions) != 0 ||
			!r.End.Equal(MustParseDate(wantEnd)) {
			t.Fatalf("首次核对中 %s 应无阻碍且截止日为 %s: %+v", r.ID, wantEnd, r)
		}
	}
	// 核对不销毁、不生成清册、不占用申请编号。
	assertUndestroyed(t, s, ids...)
	if _, found, err := s.GetManifest("APP-1"); err != nil || found {
		t.Fatalf("首次核对后申请编号下不应有清册: found=%v err=%v", found, err)
	}

	// 随后只延长 A-1 的截止日，使它晚于申请的处理日期；A-2 保持到期。
	revise(t, s, "R-1", "A-1", "2025-01-05", "2025-03-10", "2025-06-01", "业务需要延长")

	// 沿用原申请编号、处理日期和名单正式提交：必须明确返回未到期错误，
	// 可按既有 ErrNotExpired 类别识别，并指出受阻档案及当前截止日。
	m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: processedOn, ArchiveIDs: ids,
	})
	if !errors.Is(err, ErrNotExpired) {
		t.Fatalf("延长后正式提交应返回 ErrNotExpired，得到 %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "A-1") || !strings.Contains(msg, "2025-03-10") {
		t.Fatalf("未到期错误应指出受阻档案 A-1 与当前截止日 2025-03-10: %v", err)
	}
	if m.ApplicationID != "" || len(m.Entries) != 0 {
		t.Fatalf("失败时不得返回清册: %+v", m)
	}
	// 整批都不能销毁：连符合条件的 A-2 也不能先行成功，申请编号下没有清册。
	assertUndestroyed(t, s, ids...)
	if _, found, err := s.GetManifest("APP-1"); err != nil || found {
		t.Fatalf("失败的申请不应留下清册，found=%v err=%v", found, err)
	}

	// 再次核对同一申请：存在阻碍。被修订档案的登记信息带最新截止日，
	// 列出未到期阻碍；另一份仍没有阻碍。
	blocked := mustCheck(t, s, "APP-1", "2025-01-10", ids...)
	if blocked.Status != CheckBlocked {
		t.Fatalf("延长后再次核对应存在阻碍，得到 %s", blocked.Status)
	}
	if blocked.Manifest != nil {
		t.Fatalf("申请尚未成功，核对报告不应附清册: %+v", blocked.Manifest)
	}
	b1 := blocked.Results[0]
	if !b1.Exists || b1.Destroyed || b1.Category != "合同" ||
		!b1.Start.Equal(MustParseDate("2020-01-01")) || !b1.End.Equal(extendedEnd) {
		t.Fatalf("被修订档案的登记信息应带最新截止日 %s: %+v", extendedEnd, b1)
	}
	if kinds := obstructionKinds(b1); len(kinds) != 1 || kinds[0] != ObstructionNotExpired {
		t.Fatalf("A-1 应只列未到期阻碍: %+v", b1.Obstructions)
	}
	b2 := blocked.Results[1]
	if !b2.Exists || b2.Destroyed || !b2.End.Equal(MustParseDate("2025-01-10")) ||
		len(b2.Obstructions) != 0 {
		t.Fatalf("A-2 应保持到期且没有阻碍: %+v", b2)
	}

	// 把 A-1 的截止日缩短到申请处理日期当天。
	// 本次成功修订填写的办理日期（2024-12-01）早于前一次（2025-06-01）：
	// 办理日期只是记录信息，不决定哪次期限生效，仍采用最后一次成功
	// 提交的新截止日 2025-01-10。
	revise(t, s, "R-2", "A-1", "2025-03-10", "2025-01-10", "2024-12-01", "重新评估缩短")

	// 用原申请核对：当天算到期，报告恢复为可以办理。
	readyAgain := mustCheck(t, s, "APP-1", "2025-01-10", ids...)
	if readyAgain.Status != CheckReady {
		t.Fatalf("缩短到处理日期当天后核对应恢复可以办理，得到 %s", readyAgain.Status)
	}
	if !readyAgain.Results[0].End.Equal(finalEnd) || len(readyAgain.Results[0].Obstructions) != 0 {
		t.Fatalf("被修订档案应按最终截止日 %s 判定到期: %+v", finalEnd, readyAgain.Results[0])
	}

	// 沿用原申请编号、处理日期和名单正式提交：整体成功，两份档案进入
	// 同一份已关闭清册。
	manifest, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: processedOn, ArchiveIDs: ids,
	})
	if err != nil {
		t.Fatalf("两份档案均到期且无冻结，正式提交应整体成功: %v", err)
	}
	if manifest.ApplicationID != "APP-1" || !manifest.ProcessedOn.Equal(processedOn) ||
		len(manifest.Entries) != 2 {
		t.Fatalf("应生成含两份条目的已关闭清册: %+v", manifest)
	}
	// 条目按编号排序；被修订条目的截止日是最终生效值，不能使用最初
	// 登记值 2025-01-05 或中间延长过的 2025-03-10。
	if manifest.Entries[0].ID != "A-1" || manifest.Entries[1].ID != "A-2" {
		t.Fatalf("清册条目应按编号稳定排序: %+v", manifest.Entries)
	}
	e1 := manifest.Entries[0]
	if e1.Category != "合同" || !e1.Start.Equal(MustParseDate("2020-01-01")) ||
		!e1.End.Equal(finalEnd) {
		t.Fatalf("A-1 清册条目应保存最终生效截止日 %s: %+v", finalEnd, e1)
	}
	if !manifest.Entries[1].End.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("A-2 清册条目截止日不正确: %+v", manifest.Entries[1])
	}

	// 调用者此前拿到的报告保留各自核对时的截止日与结论，不因后续修订
	// 或销毁被改写。
	if first.Status != CheckReady ||
		!first.Results[0].End.Equal(initialEnd) || !first.Results[1].End.Equal(MustParseDate("2025-01-10")) ||
		len(first.Results[0].Obstructions) != 0 || first.Manifest != nil {
		t.Fatalf("首次报告被后续修订/销毁改写: %+v", first)
	}
	if blocked.Status != CheckBlocked || !blocked.Results[0].End.Equal(extendedEnd) ||
		len(blocked.Results[0].Obstructions) != 1 ||
		blocked.Results[0].Obstructions[0].Kind != ObstructionNotExpired ||
		len(blocked.Results[1].Obstructions) != 0 || blocked.Manifest != nil {
		t.Fatalf("存在阻碍报告被后续修订/销毁改写: %+v", blocked)
	}
	if readyAgain.Status != CheckReady || !readyAgain.Results[0].End.Equal(finalEnd) ||
		len(readyAgain.Results[0].Obstructions) != 0 {
		t.Fatalf("恢复可办的报告被后续销毁改写: %+v", readyAgain)
	}

	// 新查询反映当前保存状态：按申请编号取回的就是关闭时那份清册。
	got, found, err := s.GetManifest("APP-1")
	if err != nil || !found {
		t.Fatalf("成功提交后应能取回清册: found=%v err=%v", found, err)
	}
	if got.ApplicationID != manifest.ApplicationID || !got.ProcessedOn.Equal(manifest.ProcessedOn) ||
		len(got.Entries) != 2 || !got.Entries[0].End.Equal(finalEnd) {
		t.Fatalf("新查询的清册应与关闭时一致: %+v", got)
	}

	// 销毁后的档案历史仍能看到最初截止日、最终截止日和按成功顺序
	// 保留的两次修订；办理日期乱序也不改变顺序，与清册内容相符。
	h1, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("A-1 历史查询失败: found=%v err=%v", found, err)
	}
	if !h1.Destroyed || h1.ManifestApplicationID != "APP-1" {
		t.Fatalf("A-1 应已销毁并归属 APP-1: %+v", h1)
	}
	if !h1.InitialEnd.Equal(initialEnd) || !h1.RetentionEnd.Equal(finalEnd) {
		t.Fatalf("A-1 历史应保留最初截止日 %s 与最终截止日 %s: %+v", initialEnd, finalEnd, h1)
	}
	if len(h1.Revisions) != 2 {
		t.Fatalf("A-1 应保留两次修订，得到 %d: %+v", len(h1.Revisions), h1.Revisions)
	}
	r1, r2 := h1.Revisions[0], h1.Revisions[1]
	if r1.RevisionID != "R-1" || !r1.OldEnd.Equal(initialEnd) || !r1.NewEnd.Equal(extendedEnd) ||
		!r1.RevisedOn.Equal(MustParseDate("2025-06-01")) || r1.Reason != "业务需要延长" {
		t.Fatalf("第一条修订（延长）记录不符: %+v", r1)
	}
	if r2.RevisionID != "R-2" || !r2.OldEnd.Equal(extendedEnd) || !r2.NewEnd.Equal(finalEnd) ||
		!r2.RevisedOn.Equal(MustParseDate("2024-12-01")) || r2.Reason != "重新评估缩短" {
		t.Fatalf("第二条修订（缩短，办理日期更早）记录不符: %+v", r2)
	}
	if h1.Manifest == nil || h1.Manifest.ApplicationID != "APP-1" ||
		len(h1.Manifest.Entries) != 2 || !h1.Manifest.Entries[0].End.Equal(finalEnd) {
		t.Fatalf("A-1 历史附带的清册应与清册内容相符: %+v", h1.Manifest)
	}

	h2, found, err := s.History("A-2")
	if err != nil || !found {
		t.Fatalf("A-2 历史查询失败: found=%v err=%v", found, err)
	}
	if !h2.Destroyed || h2.ManifestApplicationID != "APP-1" ||
		!h2.InitialEnd.Equal(MustParseDate("2025-01-10")) ||
		!h2.RetentionEnd.Equal(MustParseDate("2025-01-10")) || len(h2.Revisions) != 0 {
		t.Fatalf("未修订的 A-2 历史不符: %+v", h2)
	}
	if h2.Manifest == nil || len(h2.Manifest.Entries) != 2 {
		t.Fatalf("A-2 应与 A-1 在同一份清册中: %+v", h2.Manifest)
	}
}

// assertUndestroyed 断言名单中的每份档案都仍未销毁。
func assertUndestroyed(t *testing.T, s *Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		h, found, err := s.History(id)
		if err != nil || !found {
			t.Fatalf("档案 %s 应仍可查询: found=%v err=%v", id, found, err)
		}
		if h.Destroyed || h.ManifestApplicationID != "" || h.Manifest != nil {
			t.Fatalf("整批失败时档案 %s 不应被销毁或归入清册: %+v", id, h)
		}
	}
}
