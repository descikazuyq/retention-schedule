package retention

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

func mustCheck(t *testing.T, s *Store, appID, processedOn string, ids ...string) CheckReport {
	t.Helper()
	r, err := s.Check(CheckRequest{
		ApplicationID: appID,
		ProcessedOn:   MustParseDate(processedOn),
		ArchiveIDs:    ids,
	})
	if err != nil {
		t.Fatalf("核对 %s 失败: %v", appID, err)
	}
	return r
}

// obstructionKinds 收集一份档案结果中的全部阻碍类别，保持出现顺序。
func obstructionKinds(r ArchiveCheckResult) []ObstructionKind {
	kinds := make([]ObstructionKind, 0, len(r.Obstructions))
	for _, o := range r.Obstructions {
		kinds = append(kinds, o.Kind)
	}
	return kinds
}

func TestCheckReadyWhenAllClear(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2024-12-31")
	// A-1 有一条已解除冻结，不应成为阻碍。
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-08")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-09")}); err != nil {
		t.Fatal(err)
	}

	// 截止日当天即到期；处理日期晚于截止日同样到期。
	r := mustCheck(t, s, "APP-1", "2025-01-10", "A-2", "A-1")
	if r.Status != CheckReady {
		t.Fatalf("全部无阻碍应可以办理，得到 %s: %+v", r.Status, r.Results)
	}
	if r.Manifest != nil {
		t.Fatalf("未成功使用的申请不应附清册: %+v", r.Manifest)
	}
	// 结果按提交顺序，而非编号排序。
	if len(r.Results) != 2 || r.Results[0].ID != "A-2" || r.Results[1].ID != "A-1" {
		t.Fatalf("结果应按提交顺序: %+v", r.Results)
	}
	first := r.Results[1]
	if !first.Exists || first.Category != "合同" ||
		!first.Start.Equal(MustParseDate("2020-01-01")) ||
		!first.End.Equal(MustParseDate("2025-01-10")) || first.Destroyed {
		t.Fatalf("已登记档案应显示类别、起算日、截止日和销毁状态: %+v", first)
	}
	if len(first.Obstructions) != 0 {
		t.Fatalf("无阻碍档案不应列出阻碍: %+v", first.Obstructions)
	}
}

func TestCheckObstructionsAllKinds(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10") // 未到期
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10") // 到期但有冻结
	reg(t, s, "A-3", "图纸", "2020-01-01", "2025-01-08") // 在 2025-01-09 已到期、无冻结，可办
	// A-2：一条未到期名单共用处理日期 2025-01-09，故 A-1、A-2 都未到期；
	// 给 A-2 挂冻结验证“未到期 + 冻结”并列。
	if err := s.Freeze(FreezeInput{ArchiveID: "A-2", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Freeze(FreezeInput{ArchiveID: "A-2", FreezeID: "F-2", Reason: "审计", FrozenOn: MustParseDate("2025-01-06")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Freeze(FreezeInput{ArchiveID: "A-2", FreezeID: "F-3", Reason: "巡检", FrozenOn: MustParseDate("2025-01-07")}); err != nil {
		t.Fatal(err)
	}
	// F-2 已解除，不能作为阻碍，也不能打断其余冻结的历史顺序。
	if err := s.Release(ReleaseInput{ArchiveID: "A-2", FreezeID: "F-2", Reason: "审计结束", ReleasedOn: MustParseDate("2025-01-08")}); err != nil {
		t.Fatal(err)
	}

	r := mustCheck(t, s, "APP-1", "2025-01-09", "A-1", "GHOST", "A-2", "A-3")
	if r.Status != CheckBlocked {
		t.Fatalf("存在阻碍应判存在阻碍，得到 %s", r.Status)
	}
	if len(r.Results) != 4 {
		t.Fatalf("应逐份列出 4 条结果，得到 %d", len(r.Results))
	}

	// 提交顺序保留。
	gotIDs := []string{r.Results[0].ID, r.Results[1].ID, r.Results[2].ID, r.Results[3].ID}
	if want := []string{"A-1", "GHOST", "A-2", "A-3"}; len(gotIDs) != len(want) {
		t.Fatalf("顺序错误: %v", gotIDs)
	} else {
		for i := range want {
			if gotIDs[i] != want[i] {
				t.Fatalf("顺序错误: %v", gotIDs)
			}
		}
	}

	// 不存在：单独标明，Exists 为 false，登记字段为零值，只有一条不存在阻碍。
	ghost := r.Results[1]
	if ghost.Exists || ghost.Category != "" || !ghost.Start.IsZero() || ghost.Destroyed {
		t.Fatalf("不存在编号不应带登记字段: %+v", ghost)
	}
	if kinds := obstructionKinds(ghost); len(kinds) != 1 || kinds[0] != ObstructionMissing {
		t.Fatalf("不存在编号应只有一条不存在阻碍: %+v", ghost.Obstructions)
	}

	// 未到期：A-1 只报未到期。
	if kinds := obstructionKinds(r.Results[0]); len(kinds) != 1 || kinds[0] != ObstructionNotExpired {
		t.Fatalf("A-1 应只有未到期阻碍: %+v", r.Results[0].Obstructions)
	}

	// A-2：未到期与两条未解除冻结并列，冻结顺序沿用冻结历史（F-1 后直接 F-3）。
	a2 := r.Results[2]
	kinds := obstructionKinds(a2)
	if len(kinds) != 3 || kinds[0] != ObstructionNotExpired ||
		kinds[1] != ObstructionActiveFreeze || kinds[2] != ObstructionActiveFreeze {
		t.Fatalf("A-2 应同时显示未到期和每条未解除冻结: %+v", a2.Obstructions)
	}
	f1 := a2.Obstructions[1].Freeze
	f3 := a2.Obstructions[2].Freeze
	if f1.FreezeID != "F-1" || f1.Reason != "诉讼" || !f1.FrozenOn.Equal(MustParseDate("2025-01-05")) {
		t.Fatalf("F-1 冻结信息不正确: %+v", f1)
	}
	if f3.FreezeID != "F-3" || f3.Reason != "巡检" || !f3.FrozenOn.Equal(MustParseDate("2025-01-07")) {
		t.Fatalf("F-3 冻结信息应沿用历史顺序且内容完整: %+v", f3)
	}

	// A-3 无阻碍，不影响整批判定但仍逐份列出。
	if len(r.Results[3].Obstructions) != 0 {
		t.Fatalf("A-3 应无阻碍: %+v", r.Results[3].Obstructions)
	}
}

func TestCheckDestroyedShowsOwningManifest(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 用一个新申请编号核对已销毁档案：属存在阻碍，阻碍须给出所属清册编号与处理日期。
	r := mustCheck(t, s, "APP-OTHER", "2025-01-11", "A-1")
	if r.Status != CheckBlocked {
		t.Fatalf("含已销毁档案应判存在阻碍，得到 %s", r.Status)
	}
	res := r.Results[0]
	if !res.Exists || !res.Destroyed {
		t.Fatalf("已销毁档案应存在且销毁状态为真: %+v", res)
	}
	if len(res.Obstructions) != 1 || res.Obstructions[0].Kind != ObstructionDestroyed {
		t.Fatalf("已销毁档案应只列已销毁阻碍: %+v", res.Obstructions)
	}
	obs := res.Obstructions[0]
	if obs.ManifestApplicationID != "APP-9" || !obs.ProcessedOn.Equal(m.ProcessedOn) {
		t.Fatalf("已销毁阻碍应附所属清册申请编号和处理日期: %+v", obs)
	}
}

func TestCheckReplayableAndConflict(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	first, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"),
		ArchiveIDs: []string{"A-1", "A-2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// 同日期、同集合、仅名单顺序变化：可以取回原清册，不因档案已销毁判受阻。
	r := mustCheck(t, s, "APP-1", "2025-01-10", "A-2", "A-1")
	if r.Status != CheckReplayable {
		t.Fatalf("同一申请重放应可以取回原清册，得到 %s", r.Status)
	}
	if r.Manifest == nil || r.Manifest.ApplicationID != "APP-1" || len(r.Manifest.Entries) != 2 {
		t.Fatalf("应返回原清册: %+v", r.Manifest)
	}
	if r.Manifest.Entries[0].ID != first.Entries[0].ID {
		t.Fatalf("取回的应是原清册内容: %+v", r.Manifest.Entries)
	}
	if len(r.Results) != 0 {
		t.Fatalf("取回原清册时不应再逐份列阻碍: %+v", r.Results)
	}

	// 沿用编号改日期：冲突并附原清册，不得显示可以办理。
	r2, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-11"),
		ArchiveIDs: []string{"A-1", "A-2"},
	})
	if err != nil {
		t.Fatalf("冲突应体现在报告中而非错误: %v", err)
	}
	if r2.Status != CheckConflict || r2.Manifest == nil || r2.Manifest.ApplicationID != "APP-1" {
		t.Fatalf("改日期应判申请编号冲突并附原清册: %+v", r2)
	}

	// 沿用编号改集合：同样冲突并附原清册。
	reg(t, s, "A-3", "图纸", "2020-01-01", "2025-01-10")
	r3 := mustCheck(t, s, "APP-1", "2025-01-10", "A-1")
	if r3.Status != CheckConflict || r3.Manifest == nil || len(r3.Manifest.Entries) != 2 {
		t.Fatalf("改集合应判冲突并附原清册: %+v", r3)
	}
}

func TestCheckValidationFailures(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	validOn := MustParseDate("2025-01-10")

	cases := []struct {
		name string
		req  CheckRequest
		want error
	}{
		{"空白申请编号", CheckRequest{ApplicationID: "  ", ProcessedOn: validOn, ArchiveIDs: []string{"A-1"}}, ErrBlankField},
		{"缺失处理日期", CheckRequest{ApplicationID: "APP", ArchiveIDs: []string{"A-1"}}, ErrInvalidDate},
		{"空白档案编号", CheckRequest{ApplicationID: "APP", ProcessedOn: validOn, ArchiveIDs: []string{"A-1", "\t"}}, ErrBlankField},
		{"空名单", CheckRequest{ApplicationID: "APP", ProcessedOn: validOn, ArchiveIDs: nil}, ErrEmptyDestructionList},
		{"重复编号", CheckRequest{ApplicationID: "APP", ProcessedOn: validOn, ArchiveIDs: []string{"A-1", "A-1"}}, ErrDuplicateSelection},
		{"仅首尾空白不同也算重复", CheckRequest{ApplicationID: "APP", ProcessedOn: validOn, ArchiveIDs: []string{"A-1", "  A-1  "}}, ErrDuplicateSelection},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := s.Check(tc.req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("应失败 %v，得到 %v", tc.want, err)
			}
			if r.Status != "" || r.Results != nil || r.Manifest != nil {
				t.Fatalf("失败时不得返回部分报告: %+v", r)
			}
		})
	}
}

func TestCheckIsReadOnlyAndDoesNotReserveApplication(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")

	// 先核对可以办理。
	before := mustCheck(t, s, "APP-1", "2025-01-10", "A-1")
	if before.Status != CheckReady {
		t.Fatalf("前置核对应可以办理，得到 %s", before.Status)
	}
	// 核对不销毁、不产生清册。
	h, found, _ := s.History("A-1")
	if !found || h.Destroyed {
		t.Fatalf("核对不应销毁档案: %+v", h)
	}
	if _, found, err := s.GetManifest("APP-1"); err != nil || found {
		t.Fatalf("核对不应生成清册，found=%v err=%v", found, err)
	}

	// 核对不占用申请编号：随后同编号正式提交成功。
	m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	})
	if err != nil {
		t.Fatalf("核对后同编号应仍可正式提交: %v", err)
	}
	if m.ApplicationID != "APP-1" {
		t.Fatalf("正式提交清册不正确: %+v", m)
	}

	// 报告只说明核对当时情况：此前可办的核对之后新增冻结，再次正式提交须按最新状态受阻。
	s2 := openTestStore(t)
	reg(t, s2, "B-1", "合同", "2020-01-01", "2025-01-10")
	if r := mustCheck(t, s2, "APP-B", "2025-01-10", "B-1"); r.Status != CheckReady {
		t.Fatalf("前置核对应可以办理，得到 %s", r.Status)
	}
	if err := s2.Freeze(FreezeInput{ArchiveID: "B-1", FreezeID: "F", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-10")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Destroy(DestructionRequest{
		ApplicationID: "APP-B", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"B-1"},
	}); !errors.Is(err, ErrActiveFreeze) {
		t.Fatalf("核对后新增冻结，正式提交应按最新状态被阻止，得到 %v", err)
	}
}

func TestCheckFailureAfterCorruptionDoesNotReturnStaleReport(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	// 先有一次成功核对，调用者手里持有旧报告。
	old := mustCheck(t, s, "APP-1", "2025-01-10", "A-1")
	if old.Status != CheckReady {
		t.Fatalf("前置核对应成功: %+v", old)
	}

	// 本地记录随后损坏：再次核对必须明确失败，不能拿旧报告充当本次结果。
	if err := os.WriteFile(s.statePath(), []byte("{这不是合法JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Check(CheckRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"), ArchiveIDs: []string{"A-1"},
	}); err == nil {
		t.Fatal("状态文件损坏时核对应明确失败")
	}
}

func TestCheckReportIsDetachedCopy(t *testing.T) {
	s := openTestStore(t)
	reg(t, s, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2025-01-10")
	if _, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"),
		ArchiveIDs: []string{"A-1", "A-2"},
	}); err != nil {
		t.Fatal(err)
	}

	r := mustCheck(t, s, "APP-1", "2025-01-10", "A-1", "A-2")
	if r.Status != CheckReplayable || r.Manifest == nil {
		t.Fatalf("前置条件不正确: %+v", r)
	}
	// 调用者修改返回报告及其中清册。
	r.Manifest.ApplicationID = "HACK"
	r.Manifest.Entries[0].Category = "被篡改"
	r.Manifest.Entries = r.Manifest.Entries[:1]

	// 保存的历史不受影响。
	m, found, err := s.GetManifest("APP-1")
	if err != nil || !found {
		t.Fatalf("原清册应仍可查: found=%v err=%v", found, err)
	}
	if m.ApplicationID != "APP-1" || len(m.Entries) != 2 || m.Entries[0].Category != "合同" {
		t.Fatalf("修改报告不得影响保存的清册: %+v", m)
	}
	h, _, _ := s.History("A-1")
	if h.Manifest == nil || h.Manifest.ApplicationID != "APP-1" {
		t.Fatalf("历史中清册不应受报告修改影响: %+v", h.Manifest)
	}
}

// TestCheckSnapshotConsistencyUnderConcurrency 验证多个本机程序并发办理时，
// 同一份报告里的档案与清册必定来自同一个已保存状态：A-1、A-2 在一次销毁中
// 原子地进入同一份清册，因此任何一次核对看到的两者销毁状态必须一致，
// 且已销毁时所属清册信息必定同时可见。
func TestCheckSnapshotConsistencyUnderConcurrency(t *testing.T) {
	dir := t.TempDir()
	setup, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg(t, setup, "A-1", "合同", "2020-01-01", "2025-01-10")
	reg(t, setup, "A-2", "凭证", "2020-01-01", "2025-01-10")
	if err := setup.Close(); err != nil {
		t.Fatal(err)
	}

	const readers = 8
	const iterations = 500
	var wg sync.WaitGroup
	start := make(chan struct{})
	var failures int64

	// 读者们持续核对：一个用未使用编号看逐份状态，一个直接核对已成功编号。
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sr, err := Open(dir)
			if err != nil {
				atomic.AddInt64(&failures, 1)
				return
			}
			defer sr.Close()
			<-start
			for k := 0; k < iterations; k++ {
				r, err := sr.Check(CheckRequest{
					ApplicationID: "APP-R", ProcessedOn: MustParseDate("2025-01-10"),
					ArchiveIDs: []string{"A-1", "A-2"},
				})
				if err != nil {
					atomic.AddInt64(&failures, 1)
					return
				}
				if r.Status != CheckReady && r.Status != CheckBlocked {
					t.Errorf("未使用编号核对只应可以办理或存在阻碍，得到 %s", r.Status)
					return
				}
				if len(r.Results) != 2 {
					t.Errorf("报告应含两份档案，得到 %d", len(r.Results))
					return
				}
				d1, d2 := r.Results[0].Destroyed, r.Results[1].Destroyed
				if d1 != d2 {
					t.Errorf("报告混入了销毁前后的不同状态: %+v", r.Results)
					return
				}
				if d1 {
					for _, res := range r.Results {
						obs := res.Obstructions
						if len(obs) != 1 || obs[0].Kind != ObstructionDestroyed ||
							obs[0].ManifestApplicationID != "APP-1" ||
							!obs[0].ProcessedOn.Equal(MustParseDate("2025-01-10")) {
							t.Errorf("已销毁档案的所属清册必须与状态同快照可见: %+v", obs)
							return
						}
					}
				}

				// 直接核对该已成功编号：要么尚未生成（可以办理且无清册），
				// 要么可以取回原清册，二者必居其一，不能出现半生成状态。
				rr, err := sr.Check(CheckRequest{
					ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"),
					ArchiveIDs: []string{"A-2", "A-1"},
				})
				if err != nil {
					t.Errorf("核对已成功编号不应失败: %v", err)
					return
				}
				switch rr.Status {
				case CheckReady:
					if rr.Manifest != nil {
						t.Errorf("可以办理时不应带清册: %+v", rr.Manifest)
						return
					}
				case CheckReplayable:
					if rr.Manifest == nil || len(rr.Manifest.Entries) != 2 {
						t.Errorf("可取回时原清册必须完整: %+v", rr.Manifest)
						return
					}
				default:
					t.Errorf("已成功编号不应出现其他状态: %s", rr.Status)
					return
				}
			}
		}()
	}

	// 办理者在读者开始后销毁两份档案（单次原子办理）。
	wg.Add(1)
	go func() {
		defer wg.Done()
		sm, err := Open(dir)
		if err != nil {
			atomic.AddInt64(&failures, 1)
			close(start)
			return
		}
		defer sm.Close()
		close(start)
		if _, err := sm.Destroy(DestructionRequest{
			ApplicationID: "APP-1", ProcessedOn: MustParseDate("2025-01-10"),
			ArchiveIDs: []string{"A-1", "A-2"},
		}); err != nil {
			t.Errorf("并发销毁失败: %v", err)
		}
	}()
	wg.Wait()

	if atomic.LoadInt64(&failures) != 0 {
		t.Fatalf("并发核对中出现打开失败等错误")
	}
}
