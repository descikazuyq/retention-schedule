package retention

import (
	"errors"
	"strings"
	"testing"
)

// TestCheckRejectsRevisionOccupiedID 保护销毁前核对与正式销毁共用的编号规则：
// 只要本保管库已经用一个编号成功修订过期限，该编号就不能再作为销毁申请编号
// 通过核对——即使名单中的档案全部到期且没有冻结，也不能报告可以办理。
//
//   - 申请编号等于一条成功修订的编号时，整次核对明确失败，错误可按既有
//     ErrRevisionConflict 识别并指出冲突编号；返回的报告为空：不给正常状态、
//     不列逐份档案结果、不附清册，与已有销毁申请改变日期或名单时的
//     “申请编号冲突”报告（CheckConflict 附原清册）是两种不同的结果；
//   - 编号占用属于整个保管库：核对名单只包含另一份正常档案时同样拒绝，
//     而不是只看名单内档案的修订历史；
//   - 较早的成功修订不因同一档案后来再次修订而释放原编号；
//   - 申请编号沿用去除首尾空白的规则，带空白的同号不能绕过限制；
//   - 核对失败后状态保持原样：当前截止日、修订与冻结历史、销毁状态及
//     已有清册都不变，不生成清册，也不留下新的编号占用；换成真正未使用的
//     申请编号后，同一批符合销毁条件的档案仍报告可以办理。
func TestCheckRejectsRevisionOccupiedID(t *testing.T) {
	s := openTestStore(t)
	processedOn := "2025-01-10"

	// 两份按处理日期均已到期、尚未销毁的档案；A-1 带一条已解除冻结，
	// 用于核对失败后确认冻结历史保持原样。
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-05")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2024-06-01")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2024-07-01")}); err != nil {
		t.Fatal(err)
	}

	// 用编号 R-1 成功修订 A-1 的期限：R-1 从此被整个保管库占用。
	revise(t, s, "R-1", "A-1", "2025-01-05", "2025-01-08", "2024-12-01", "重新评估")

	// 申请编号等于成功修订的编号：处理日期与名单都有效、档案全部到期
	// 且无未解除冻结，也不能报告可以办理，整次核对应明确失败。
	report, err := s.Check(CheckRequest{
		ApplicationID: "R-1",
		ProcessedOn:   MustParseDate(processedOn),
		ArchiveIDs:    []string{"A-1", "A-2"},
	})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("申请编号占用成功修订编号时应返回 ErrRevisionConflict，得到 %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "R-1") {
		t.Fatalf("错误应指出冲突编号 R-1: %v", err)
	}
	// 报告为空：不给正常状态，不列逐份档案结果，也不附清册。
	if report.Status != "" || report.ApplicationID != "" || !report.ProcessedOn.IsZero() ||
		len(report.Results) != 0 || report.Manifest != nil {
		t.Fatalf("编号被修订占用时报告应为空: %+v", report)
	}

	// 编号占用属于整个保管库：名单只包含从未修订过的 A-2 时同样拒绝。
	if _, err := s.Check(CheckRequest{
		ApplicationID: "R-1",
		ProcessedOn:   MustParseDate(processedOn),
		ArchiveIDs:    []string{"A-2"},
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("名单不含被修订档案时仍应拒绝，得到 %v", err)
	}

	// 申请编号沿用去除首尾空白的规则：带空白的同号不能绕过限制。
	if _, err := s.Check(CheckRequest{
		ApplicationID: "  R-1  ",
		ProcessedOn:   MustParseDate(processedOn),
		ArchiveIDs:    []string{"A-2"},
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("带首尾空白的同号不应绕过限制，得到 %v", err)
	}

	// 同一档案后来再次成功修订（R-2），较早的 R-1 仍占用原编号，不释放。
	revise(t, s, "R-2", "A-1", "2025-01-08", "2025-01-06", "2024-12-15", "再次调整")
	if _, err := s.Check(CheckRequest{
		ApplicationID: "R-1",
		ProcessedOn:   MustParseDate(processedOn),
		ArchiveIDs:    []string{"A-2"},
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("再次修订后较早编号 R-1 仍应被占用，得到 %v", err)
	}

	// 失败的核对不留下任何痕迹：当前截止日、修订与冻结历史、销毁状态
	// 都保持原样，不生成清册，也不留下新的编号占用。
	h1, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("A-1 历史查询失败: found=%v err=%v", found, err)
	}
	if h1.Destroyed || h1.ManifestApplicationID != "" || h1.Manifest != nil {
		t.Fatalf("核对失败后 A-1 不应被销毁或归入清册: %+v", h1)
	}
	if !h1.RetentionEnd.Equal(MustParseDate("2025-01-06")) ||
		!h1.InitialEnd.Equal(MustParseDate("2025-01-05")) {
		t.Fatalf("核对失败后截止日应保持修订后的值: %+v", h1)
	}
	if len(h1.Revisions) != 2 || h1.Revisions[0].RevisionID != "R-1" ||
		h1.Revisions[1].RevisionID != "R-2" {
		t.Fatalf("修订历史应保持原样: %+v", h1.Revisions)
	}
	if len(h1.Freezes) != 1 || !h1.Freezes[0].Released || len(h1.ActiveFreezes) != 0 {
		t.Fatalf("冻结历史应保持原样: %+v", h1.Freezes)
	}
	assertUndestroyed(t, s, "A-2")
	if _, found, err := s.GetManifest("R-1"); err != nil || found {
		t.Fatalf("失败的核对不应生成清册: found=%v err=%v", found, err)
	}

	// 换成真正未使用的申请编号，同一批档案仍报告可以办理。
	ready := mustCheck(t, s, "APP-9", processedOn, "A-1", "A-2")
	if ready.Status != CheckReady {
		t.Fatalf("未占用的编号应可以办理，得到 %s: %+v", ready.Status, ready.Results)
	}
	if ready.Manifest != nil || len(ready.Results) != 2 {
		t.Fatalf("可以办理的报告不应附清册且应逐份列出结果: %+v", ready)
	}

	// 与已有销毁申请的“申请编号冲突”是不同结果：成功申请改变日期再核对，
	// 得到的是附原清册的 CheckConflict 报告，而不是 ErrRevisionConflict 失败。
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9",
		ProcessedOn:   MustParseDate(processedOn),
		ArchiveIDs:    []string{"A-1", "A-2"},
	}); err != nil {
		t.Fatalf("符合条件的申请应销毁成功: %v", err)
	}
	conflict := mustCheck(t, s, "APP-9", "2025-01-11", "A-1", "A-2")
	if conflict.Status != CheckConflict || conflict.Manifest == nil ||
		conflict.Manifest.ApplicationID != "APP-9" {
		t.Fatalf("已有销毁申请改变日期应报告申请编号冲突并附原清册: %+v", conflict)
	}
}

// TestCheckFailedRevisionDoesNotOccupyID 保护“只有成功修订才占用编号”的边界：
// 提交的原截止日与当前保存值不一致时修订失败、不留历史，该次修订编号仍可
// 用于销毁前核对；用它核对已到期、无冻结且未销毁的档案，应正常报告可以办理。
func TestCheckFailedRevisionDoesNotOccupyID(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-05")

	// 原截止日与当前保存值不一致：修订失败且不留历史，编号 R-X 未被占用。
	_, err := s.Revise(ReviseInput{
		RevisionID:  "R-X",
		ArchiveID:   "A-1",
		OriginalEnd: MustParseDate("2025-02-02"),
		NewEnd:      MustParseDate("2025-03-01"),
		RevisedOn:   MustParseDate("2024-12-01"),
		Reason:      "原截止日填错",
	})
	if !errors.Is(err, ErrRetentionEndChanged) {
		t.Fatalf("原截止日不一致时应返回 ErrRetentionEndChanged，得到 %v", err)
	}
	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("A-1 历史查询失败: found=%v err=%v", found, err)
	}
	if len(h.Revisions) != 0 || !h.RetentionEnd.Equal(MustParseDate("2025-01-05")) {
		t.Fatalf("失败的修订不应留下历史或改动截止日: %+v", h)
	}

	// 用失败过的编号核对到期、无冻结、未销毁的档案：正常报告可以办理，
	// 不能因为曾经提交过修订就拒绝。
	report := mustCheck(t, s, "R-X", "2025-01-10", "A-1")
	if report.Status != CheckReady {
		t.Fatalf("失败过的修订编号不应占用，核对应可以办理，得到 %s: %+v", report.Status, report.Results)
	}
	if report.Manifest != nil || len(report.Results) != 1 ||
		len(report.Results[0].Obstructions) != 0 {
		t.Fatalf("可以办理的报告不应附清册且 A-1 应无阻碍: %+v", report)
	}
}
