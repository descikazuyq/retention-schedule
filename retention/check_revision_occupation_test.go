package retention

import (
	"errors"
	"strings"
	"testing"
)

// TestCheckFailsWhenApplicationIDEqualsSuccessfulRevisionID 保护销毁前核对与
// 正式销毁遵守同一条编号规则：申请编号只要已被本保管库内一条成功的期限修订
// 占用，就不能再当成新的销毁申请编号，即使名单中的档案全部到期、没有未解除
// 冻结，也不能报告可以办理。
//
// 这项回归保障固定以下结论：
//   - 整次核对明确失败，错误可按既有 ErrRevisionConflict 识别，并指出冲突编号；
//   - 返回的报告为空：没有正常状态（可以办理/存在阻碍都不行），不逐份列出
//     档案结果，也不附清册——这与已成功销毁申请改变日期或名单时返回的
//     CheckConflict 报告（不报错、附原清册）是两种不同结果，不能混成一种；
//   - 编号占用属于整个保管库：修订挂在另一份档案上，名单只核对一份无关的
//     正常档案，同样拒绝；
//   - 申请编号沿用去除首尾空白的规则，带空白的同号不能绕过；
//   - 核对是只读的：失败后档案当前截止日、修订与冻结历史、销毁状态及已有
//     清册全部保持原样，不生成清册，不留下新的编号占用，保存文件逐字节不变；
//   - 换成真正未使用的申请编号后，同一批符合销毁条件的档案仍报告可以办理。
func TestCheckFailsWhenApplicationIDEqualsSuccessfulRevisionID(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// A-1 是成功修订的所属档案；A-2 是核对名单里另一份完全正常的档案：
	// 处理日期当天到期，唯一一条冻结已解除（不作为阻碍，同时用来核对
	// 冻结历史在失败后保持原样）；A-3 先用于生成一份已有清册。
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	reg(t, s, "A-3", "图纸", "2020-01-01", "2025-01-10")
	revise(t, s, "R-USED", "A-1", "2025-01-10", "2026-01-10", "2024-12-01", "延长")
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-2", FreezeID: "F-1",
		Reason: "诉讼", FrozenOn: MustParseDate("2025-01-08"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-2", FreezeID: "F-1",
		Reason: "结案", ReleasedOn: MustParseDate("2025-01-09"),
	}); err != nil {
		t.Fatal(err)
	}
	existing, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-M", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-3"},
	})
	if err != nil {
		t.Fatalf("前置清册生成失败: %v", err)
	}

	stateBefore := readStateFile(t, dir)

	// 提交有效的处理日期和全部符合销毁条件的名单，但申请编号等于一条成功
	// 修订的编号：整次核对必须明确失败。
	r, err := s.Check(CheckRequest{
		ApplicationID: "R-USED", ProcessedOn: MustParseDate("2025-01-10"),
		ArchiveIDs: []string{"A-2"},
	})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("申请编号被成功修订占用时应返回 ErrRevisionConflict，得到 %v", err)
	}
	if errors.Is(err, ErrApplicationMismatch) {
		t.Fatalf("修订占用不应被识别成销毁申请不一致: %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "R-USED") {
		t.Fatalf("错误应指出冲突编号 R-USED: %v", err)
	}

	// 报告为空：不给任何正常状态，不逐份列结果，不附清册。
	if r.Status != "" || r.ApplicationID != "" || !r.ProcessedOn.IsZero() ||
		r.Results != nil || r.Manifest != nil {
		t.Fatalf("修订编号冲突时必须返回空报告，得到 %+v", r)
	}

	// 带首尾空白的同号沿用去空白规则，不能绕过限制（也不能误判成空白编号）。
	r2, err := s.Check(CheckRequest{
		ApplicationID: " \tR-USED\n ", ProcessedOn: MustParseDate("2025-01-10"),
		ArchiveIDs: []string{"A-2"},
	})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("带空白的同号应仍按 ErrRevisionConflict 拒绝，得到 %v", err)
	}
	if r2.Status != "" || r2.Results != nil || r2.Manifest != nil {
		t.Fatalf("带空白同号被拒时同样不得返回报告: %+v", r2)
	}

	// 核对只读：保存文件逐字节保持原样。
	if got := readStateFile(t, dir); got != stateBefore {
		t.Fatalf("失败的核对改动了保存内容:\n%q", got)
	}

	// 被修订档案的当前截止日与修订历史保持原样。
	h1, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("A-1 历史查询失败: found=%v err=%v", found, err)
	}
	if !h1.RetentionEnd.Equal(MustParseDate("2026-01-10")) || len(h1.Revisions) != 1 ||
		h1.Revisions[0].RevisionID != "R-USED" || h1.Destroyed {
		t.Fatalf("A-1 的截止日、修订历史或销毁状态被核对改动: %+v", h1)
	}
	// 名单内正常档案的冻结历史保持原样、仍未销毁。
	h2, found, err := s.History("A-2")
	if err != nil || !found {
		t.Fatalf("A-2 历史查询失败: found=%v err=%v", found, err)
	}
	if h2.Destroyed || len(h2.Freezes) != 1 || !h2.Freezes[0].Released || len(h2.ActiveFreezes) != 0 {
		t.Fatalf("A-2 的冻结历史或销毁状态被核对改动: %+v", h2)
	}
	// 已销毁档案与已有清册保持原样。
	h3, found, err := s.History("A-3")
	if err != nil || !found || !h3.Destroyed || h3.ManifestApplicationID != "APP-M" {
		t.Fatalf("A-3 的销毁状态应保持原样: found=%v err=%v %+v", found, err, h3)
	}
	m, found, err := s.GetManifest("APP-M")
	if err != nil || !found || m.ApplicationID != "APP-M" || len(m.Entries) != 1 ||
		!m.ProcessedOn.Equal(existing.ProcessedOn) {
		t.Fatalf("已有清册应保持原样: found=%v err=%v %+v", found, err, m)
	}
	// 不生成清册，冲突编号下取不到任何销毁清册。
	if _, found, err := s.GetManifest("R-USED"); err != nil || found {
		t.Fatalf("失败的核对不应在冲突编号下生成清册: found=%v err=%v", found, err)
	}

	// 失败不留下新的编号占用：换成真正未使用的申请编号后，同一批符合
	// 销毁条件的档案仍应得到可以办理的报告。
	ready, err := s.Check(CheckRequest{
		ApplicationID: "APP-FRESH", ProcessedOn: MustParseDate("2025-01-10"),
		ArchiveIDs: []string{"A-2"},
	})
	if err != nil {
		t.Fatalf("未使用编号核对同一批档案不应失败: %v", err)
	}
	if ready.Status != CheckReady || ready.Manifest != nil || len(ready.Results) != 1 ||
		len(ready.Results[0].Obstructions) != 0 {
		t.Fatalf("未使用编号应报告可以办理: %+v", ready)
	}

	// 与销毁申请编号冲突的结果明确区分：沿用已成功销毁申请编号改变日期时，
	// 核对不报错，而是返回 CheckConflict 报告并附可供取回的原清册；
	// 修订编号占用没有原销毁清册可取，必须是错误加空报告，二者不能混成一种。
	conflict, err := s.Check(CheckRequest{
		ApplicationID: "APP-M", ProcessedOn: MustParseDate("2025-01-11"),
		ArchiveIDs: []string{"A-3"},
	})
	if err != nil {
		t.Fatalf("销毁申请改日期应在报告中体现冲突而非报错: %v", err)
	}
	if conflict.Status != CheckConflict || conflict.Manifest == nil ||
		conflict.Manifest.ApplicationID != "APP-M" || len(conflict.Manifest.Entries) != 1 {
		t.Fatalf("销毁申请冲突应返回 CheckConflict 并附原清册: %+v", conflict)
	}
}

// TestCheckRevisionOccupationSurvivesLaterRevisionAndDestruction 固定编号占用
// 的时间边界：历史中较早的成功修订仍然占用原编号，不因同一档案后来再次
// 修订而释放；该档案后来已销毁同样不释放。占用属于整个保管库，核对名单
// 只包含另一份正常档案时也必须拒绝。
func TestCheckRevisionOccupationSurvivesLaterRevisionAndDestruction(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")

	// A-1 先用 R-OLD 延长，再用 R-NEW 缩短回处理日期当天。
	revise(t, s, "R-OLD", "A-1", "2025-01-10", "2025-03-10", "2024-12-01", "延长")
	revise(t, s, "R-NEW", "A-1", "2025-03-10", "2025-01-10", "2025-01-02", "缩短")

	// 较早的成功修订编号仍被占用，即使核对的是另一份无关的正常档案。
	r, err := s.Check(CheckRequest{
		ApplicationID: "R-OLD", ProcessedOn: MustParseDate("2025-01-10"),
		ArchiveIDs: []string{"A-2"},
	})
	if !errors.Is(err, ErrRevisionConflict) || !strings.Contains(err.Error(), "R-OLD") {
		t.Fatalf("较早的成功修订编号应仍被占用，得到 %v", err)
	}
	if r.Status != "" || r.Results != nil || r.Manifest != nil {
		t.Fatalf("占用冲突时不应返回报告: %+v", r)
	}

	// 修订所属档案随后已销毁：编号仍不释放，也不能被误当成清册申请编号
	// （清册编号是 APP-1，不是 R-OLD）。
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"),
		ArchiveIDs: []string{"A-1"},
	}); err != nil {
		t.Fatalf("A-1 销毁失败: %v", err)
	}
	r2, err := s.Check(CheckRequest{
		ApplicationID: "R-OLD", ProcessedOn: MustParseDate("2025-01-10"),
		ArchiveIDs: []string{"A-2"},
	})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("所属档案销毁后旧修订编号仍应占用，得到 %v", err)
	}
	if r2.Status != "" || r2.Manifest != nil || r2.Results != nil {
		t.Fatalf("修订编号冲突绝不能表现成取回清册或其他报告: %+v", r2)
	}

	// 未使用的新编号核对仍符合条件的 A-2：可以办理。
	ready := mustCheck(t, s, "APP-2", "2025-01-10", "A-2")
	if ready.Status != CheckReady || len(ready.Results) != 1 ||
		len(ready.Results[0].Obstructions) != 0 {
		t.Fatalf("未使用编号应正常报告可以办理: %+v", ready)
	}
}

// TestCheckFailedRevisionDoesNotOccupyApplicationID 保护“只有成功修订才占用
// 编号”这一边界：提交的原截止日与当前保存值不一致时修订失败，不留历史，
// 该次修订编号仍可用于销毁前核对。用它核对已到期、没有冻结且尚未销毁的
// 档案，应正常报告可以办理，不能因为曾经提交过修订就拒绝。
func TestCheckFailedRevisionDoesNotOccupyApplicationID(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	// 原截止日故意提交成与当前保存值 2025-01-10 不一致的值：修订失败，
	// 可按 ErrRetentionEndChanged 识别，且不留任何历史。
	_, err := s.Revise(ReviseInput{
		RevisionID:  "R-FAIL",
		ArchiveID:   "A-1",
		OriginalEnd: MustParseDate("2025-01-09"),
		NewEnd:      MustParseDate("2026-01-10"),
		RevisedOn:   MustParseDate("2024-12-01"),
		Reason:      "基于过期信息延长",
	})
	if !errors.Is(err, ErrRetentionEndChanged) {
		t.Fatalf("前置修订应因期限已变化失败，得到 %v", err)
	}
	h, found, _ := s.History("A-1")
	if !found || len(h.Revisions) != 0 || !h.RetentionEnd.Equal(MustParseDate("2025-01-10")) {
		t.Fatalf("失败的修订不应留历史或改变当前截止日: %+v", h)
	}

	// 同一个失败过的编号用于销毁前核对：档案截止日当天到期、无冻结、
	// 未销毁，应正常报告可以办理，不能报 ErrRevisionConflict。
	r, err := s.Check(CheckRequest{
		ApplicationID: "R-FAIL", ProcessedOn: MustParseDate("2025-01-10"),
		ArchiveIDs: []string{"A-1"},
	})
	if err != nil {
		t.Fatalf("失败过的修订编号不应占用销毁申请编号，核对不应失败: %v", err)
	}
	if r.Status != CheckReady || r.Manifest != nil {
		t.Fatalf("应报告可以办理且不附清册，得到 %s: %+v", r.Status, r)
	}
	if len(r.Results) != 1 || r.Results[0].ID != "A-1" ||
		len(r.Results[0].Obstructions) != 0 {
		t.Fatalf("应逐份给出无阻碍结果: %+v", r.Results)
	}

	// 核对不生成清册，档案仍未销毁；失败编号从未成为占用。
	if _, found, err := s.GetManifest("R-FAIL"); err != nil || found {
		t.Fatalf("核对不应生成清册: found=%v err=%v", found, err)
	}
	h, _, _ = s.History("A-1")
	if h.Destroyed || len(h.Revisions) != 0 {
		t.Fatalf("核对后档案不应被销毁或新增修订历史: %+v", h)
	}
}
