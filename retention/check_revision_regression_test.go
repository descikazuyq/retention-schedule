package retention

import (
	"errors"
	"strings"
	"testing"
)

// TestCheckThenReviseThenDestroyRegression 保护几项既有行为之间的关系：
// 核对报告不预占档案、不保证稍后提交一定成功；正式销毁按提交时当前生效的
// 保管截止日判断（截止日当天即到期）；清册条目保存关闭瞬间最终生效的期限；
// 修订按成功顺序生效，修订办理日期仅作记录；调用者手里的核对报告是核对当时
// 的快照，不被后续修订或销毁改写。
func TestCheckThenReviseThenDestroyRegression(t *testing.T) {
	s := openTestStore(t)

	// 两份没有冻结、尚未销毁的档案。处理日期 2025-01-10 不早于两份截止日，
	// 且被修订档案 A-1 的最初截止日（2025-01-08）严格早于处理日期。
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-08")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	const appID = "APP-1"
	processedOn := "2025-01-10"
	ids := []string{"A-1", "A-2"}

	// 第一次核对：两份均到期、无冻结，整批可以办理。
	firstReport := mustCheck(t, s, appID, processedOn, ids...)
	if firstReport.Status != CheckReady {
		t.Fatalf("首次核对应可以办理，得到 %s: %+v", firstReport.Status, firstReport.Results)
	}
	if len(firstReport.Results) != 2 ||
		!firstReport.Results[0].End.Equal(MustParseDate("2025-01-08")) ||
		!firstReport.Results[1].End.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("首次核对应带各自当时截止日: %+v", firstReport.Results)
	}

	// 只延长 A-1 的截止日，使其晚于申请处理日期；A-2 保持到期。
	revise(t, s, "R-1", "A-1", "2025-01-08", "2025-02-20", "2025-01-09", "业务需要延长")

	// 沿用原申请编号、处理日期和名单正式提交：必须按提交时当前生效期限判未到期，
	// 错误可按 ErrNotExpired 识别，并指出受阻档案与当前截止日。
	m, err := s.Destroy(DestructionRequest{
		ApplicationID: appID,
		ProcessedOn:   MustParseDate(processedOn),
		ArchiveIDs:    ids,
	})
	if !errors.Is(err, ErrNotExpired) {
		t.Fatalf("A-1 延期后整批提交应失败 ErrNotExpired，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "A-1") || !strings.Contains(err.Error(), "2025-02-20") {
		t.Fatalf("未到期错误应指出受阻档案与当前截止日: %v", err)
	}
	if m.ApplicationID != "" || len(m.Entries) != 0 {
		t.Fatalf("失败提交不应返回部分清册: %+v", m)
	}

	// 整批都不能销毁：另一份符合条件的 A-2 也不能先行成功；申请编号下没有清册。
	for _, id := range ids {
		h, found, _ := s.History(id)
		if !found || h.Destroyed {
			t.Fatalf("整批失败时 %s 不应被销毁: %+v", id, h)
		}
	}
	if _, found, err := s.GetManifest(appID); err != nil || found {
		t.Fatalf("整批失败不应留下清册，found=%v err=%v", found, err)
	}

	// 再次核对同一申请：存在阻碍；被修订档案带最新截止日并列未到期阻碍，
	// 另一份仍没有阻碍。
	blockedReport := mustCheck(t, s, appID, processedOn, ids...)
	if blockedReport.Status != CheckBlocked {
		t.Fatalf("延期后再次核对应存在阻碍，得到 %s", blockedReport.Status)
	}
	if blockedReport.Manifest != nil {
		t.Fatalf("申请尚未成功使用，核对不应附清册: %+v", blockedReport.Manifest)
	}
	a1 := blockedReport.Results[0]
	if a1.ID != "A-1" || !a1.End.Equal(MustParseDate("2025-02-20")) {
		t.Fatalf("A-1 登记信息应带最新截止日: %+v", a1)
	}
	if kinds := obstructionKinds(a1); len(kinds) != 1 || kinds[0] != ObstructionNotExpired {
		t.Fatalf("A-1 应只有一条未到期阻碍: %+v", a1.Obstructions)
	}
	a2 := blockedReport.Results[1]
	if a2.ID != "A-2" || !a2.End.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("A-2 结果不正确: %+v", a2)
	}
	if len(a2.Obstructions) != 0 {
		t.Fatalf("A-2 仍符合条件，不应有阻碍: %+v", a2.Obstructions)
	}

	// 把 A-1 的截止日缩短到申请处理日期当天。第二次修订的办理日期（2025-01-05）
	// 故意早于第一次（2025-01-09）：办理日期只是记录信息，不决定哪次期限生效。
	revise(t, s, "R-2", "A-1", "2025-02-20", "2025-01-10", "2025-01-05", "复核后缩短")

	// 当天即到期：用原申请核对，报告恢复为可以办理。
	readyAgain := mustCheck(t, s, appID, processedOn, ids...)
	if readyAgain.Status != CheckReady {
		t.Fatalf("缩短到处理日期当天应恢复可以办理，得到 %s", readyAgain.Status)
	}
	if !readyAgain.Results[0].End.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("A-1 核对截止日应为缩短后的最终值: %+v", readyAgain.Results[0])
	}

	// 沿用原申请正式提交：整体成功，两份档案进入同一份已关闭清册。
	manifest, err := s.Destroy(DestructionRequest{
		ApplicationID: appID,
		ProcessedOn:   MustParseDate(processedOn),
		ArchiveIDs:    ids,
	})
	if err != nil {
		t.Fatalf("两份均到期后正式提交应整体成功: %v", err)
	}
	if manifest.ApplicationID != appID ||
		!manifest.ProcessedOn.Equal(MustParseDate(processedOn)) ||
		len(manifest.Entries) != 2 {
		t.Fatalf("应生成含两份档案的已关闭清册: %+v", manifest)
	}
	// 清册条目按编号排序；被修订条目的截止日必须是最终生效值，
	// 不能是最初登记值（2025-01-08）或中间延长过的期限（2025-02-20）。
	finalEnd := MustParseDate("2025-01-10")
	e1, e2 := manifest.Entries[0], manifest.Entries[1]
	if e1.ID != "A-1" || !e1.End.Equal(finalEnd) || e1.Category != "合同" ||
		!e1.Start.Equal(MustParseDate("2020-01-01")) {
		t.Fatalf("A-1 清册条目应为关闭瞬间最终登记内容: %+v", e1)
	}
	if e2.ID != "A-2" || !e2.End.Equal(finalEnd) || e2.Category != "凭证" {
		t.Fatalf("A-2 清册条目不正确: %+v", e2)
	}

	// 同一申请再核对：可以取回原清册。
	replay := mustCheck(t, s, appID, processedOn, "A-2", "A-1")
	if replay.Status != CheckReplayable || replay.Manifest == nil ||
		len(replay.Manifest.Entries) != 2 ||
		!replay.Manifest.Entries[0].End.Equal(finalEnd) {
		t.Fatalf("成功后同申请应可取回原清册: %+v", replay)
	}

	// 新查询反映当前保存状态：两份档案都已销毁，阻碍附所属同一清册。
	fresh := mustCheck(t, s, "APP-AFTER", processedOn, ids...)
	if fresh.Status != CheckBlocked {
		t.Fatalf("新查询应反映已销毁状态，得到 %s", fresh.Status)
	}
	for _, r := range fresh.Results {
		if !r.Destroyed {
			t.Fatalf("%s 应已销毁: %+v", r.ID, r)
		}
		if kinds := obstructionKinds(r); len(kinds) != 1 || kinds[0] != ObstructionDestroyed {
			t.Fatalf("%s 应只有已销毁阻碍: %+v", r.ID, r.Obstructions)
		}
		obs := r.Obstructions[0]
		if obs.ManifestApplicationID != appID || !obs.ProcessedOn.Equal(MustParseDate(processedOn)) {
			t.Fatalf("已销毁阻碍应附所属清册信息: %+v", obs)
		}
	}

	// 销毁后历史：最初截止日、最终截止日与按成功顺序保留的两次修订都在，
	// 与清册内容相符。
	h1, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("销毁后应仍可查 A-1 历史: found=%v err=%v", found, err)
	}
	if !h1.Destroyed || h1.ManifestApplicationID != appID || h1.Manifest == nil {
		t.Fatalf("A-1 历史应记录销毁状态与所属清册: %+v", h1)
	}
	if !h1.InitialEnd.Equal(MustParseDate("2025-01-08")) {
		t.Fatalf("最初截止日应保留登记值 2025-01-08，得到 %s", h1.InitialEnd)
	}
	if !h1.RetentionEnd.Equal(finalEnd) {
		t.Fatalf("最终截止日应为最后一次成功修订值 2025-01-10，得到 %s", h1.RetentionEnd)
	}
	if len(h1.Revisions) != 2 {
		t.Fatalf("应保留两次修订，得到 %d", len(h1.Revisions))
	}
	// 顺序按成功提交，而非修订办理日期（R-2 的办理日期更早）。
	r1, r2 := h1.Revisions[0], h1.Revisions[1]
	if r1.RevisionID != "R-1" ||
		!r1.OldEnd.Equal(MustParseDate("2025-01-08")) || !r1.NewEnd.Equal(MustParseDate("2025-02-20")) ||
		!r1.RevisedOn.Equal(MustParseDate("2025-01-09")) {
		t.Fatalf("第一次修订记录不符: %+v", r1)
	}
	if r2.RevisionID != "R-2" ||
		!r2.OldEnd.Equal(MustParseDate("2025-02-20")) || !r2.NewEnd.Equal(finalEnd) ||
		!r2.RevisedOn.Equal(MustParseDate("2025-01-05")) {
		t.Fatalf("第二次修订记录应按成功顺序衔接: %+v", r2)
	}
	// 历史中清册与独立取回的清册一致，被修订条目截止日为最终生效值。
	if h1.Manifest == nil || len(h1.Manifest.Entries) != 2 {
		t.Fatalf("历史应附完整清册: %+v", h1.Manifest)
	}
	if !h1.Manifest.Entries[0].End.Equal(h1.RetentionEnd) {
		t.Fatalf("清册条目截止日应与最终生效期限一致: 清册=%s 历史=%s",
			h1.Manifest.Entries[0].End, h1.RetentionEnd)
	}
	stored, found, err := s.GetManifest(appID)
	if err != nil || !found {
		t.Fatalf("已关闭清册应可取回: found=%v err=%v", found, err)
	}
	if len(stored.Entries) != 2 || !stored.Entries[0].End.Equal(finalEnd) {
		t.Fatalf("保存的清册内容不正确: %+v", stored)
	}
	h2, found, _ := s.History("A-2")
	if !found || !h2.Destroyed || !h2.InitialEnd.Equal(finalEnd) || !h2.RetentionEnd.Equal(finalEnd) {
		t.Fatalf("A-2 历史应显示已销毁且期限未变: %+v", h2)
	}

	// 调用者此前拿到的报告保留各自核对时的截止日与结论，
	// 不被后续修订或销毁改写。
	if firstReport.Status != CheckReady ||
		!firstReport.Results[0].End.Equal(MustParseDate("2025-01-08")) ||
		len(firstReport.Results[0].Obstructions) != 0 {
		t.Fatalf("首次可办报告被后续操作改写: status=%s result=%+v",
			firstReport.Status, firstReport.Results[0])
	}
	if blockedReport.Status != CheckBlocked ||
		!blockedReport.Results[0].End.Equal(MustParseDate("2025-02-20")) ||
		len(blockedReport.Results[0].Obstructions) != 1 ||
		blockedReport.Results[0].Obstructions[0].Kind != ObstructionNotExpired {
		t.Fatalf("延期后的受阻报告被后续操作改写: status=%s result=%+v",
			blockedReport.Status, blockedReport.Results[0])
	}
}
