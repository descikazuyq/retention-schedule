package retention

import (
	"errors"
	"strings"
	"testing"
)

// 本组测试回归保障冻结历史读取时多类错误共同出现的报告先后关系：
//
//   - 同一保管库里既有原冻结信息缺项（缺冻结原因或冻结日期），又有解除信息
//     错误（已解除却缺解除原因、缺解除日期，或解除日期早于冻结日期）时，
//     必须先说明原冻结信息缺项——即使解除问题属于编号文本更靠前的档案，
//     或出现在同一档案更早登记的冻结中；
//   - 同一条冻结同时缺少原冻结原因与日期时两项都说明，完整的解除信息不能
//     代替原冻结信息；
//   - 同类问题多处出现时，按档案编号的文本升序、再按该档案冻结历史的登记
//     顺序选出首个问题：冻结编号大小与日期先后都不能替代登记顺序，不同档案
//     在保存内容中的排列位置也不影响报告；
//   - 全部原冻结信息齐备后，才报告按同样顺序选出的解除信息错误；同一条已
//     解除记录同时缺少解除原因与日期时仍先说明解除原因；日期倒置时错误同时
//     给出解除日期与冻结日期。
//
// 所有用例的保存记录都能正常解析、档案身份与冻结编号合法、其他业务关系没有
// 矛盾，因此命中的只可能是冻结历史自身的完整性校验。

// frArchive 构造一份只有冻结历史不同的合法登记记录。
func frArchive(id string, freezes ...string) string {
	return `"` + id + `":{"id":"` + id +
		`","category":"合同","start":"2020-01-01","end":"2026-01-10",` +
		`"initial_end":"2026-01-10","destroyed":false,"freezes":[` +
		strings.Join(freezes, ",") + `]}`
}

func frState(archivesJSON string) string {
	return `{"version":1,"archives":{` + archivesJSON + `},"manifests":{}}`
}

// 各类冻结记录构造器：原始信息与解除信息分别可控，方便组合出多类问题并存。
func frActive(id, frozenOn string) string {
	return `{"id":"` + id + `","reason":"诉讼","frozen_on":"` + frozenOn + `","released":false}`
}

func frActiveMissingDate(id string) string {
	return `{"id":"` + id + `","reason":"诉讼","released":false}`
}

func frActiveMissingReason(id, frozenOn string) string {
	return `{"id":"` + id + `","reason":"  ","frozen_on":"` + frozenOn + `","released":false}`
}

// frReleasedMissingBothOrigin 是一条已解除、解除信息完整，但原冻结原因与冻结
// 日期都缺失的记录——完整的解除信息不能补齐或代替原冻结信息。
func frReleasedMissingBothOrigin(id, releasedOn string) string {
	return `{"id":"` + id + `","released":true,"release_reason":"结案","released_on":"` + releasedOn + `"}`
}

func frReleasedValid(id, frozenOn, releasedOn string) string {
	return `{"id":"` + id + `","reason":"诉讼","frozen_on":"` + frozenOn +
		`","released":true,"release_reason":"结案","released_on":"` + releasedOn + `"}`
}

func frReleasedMissingReason(id, frozenOn, releasedOn string) string {
	return `{"id":"` + id + `","reason":"诉讼","frozen_on":"` + frozenOn +
		`","released":true,"released_on":"` + releasedOn + `"}`
}

func frReleasedMissingDate(id, frozenOn string) string {
	return `{"id":"` + id + `","reason":"诉讼","frozen_on":"` + frozenOn +
		`","released":true,"release_reason":"结案"}`
}

func frReleasedMissingReasonAndDate(id, frozenOn string) string {
	return `{"id":"` + id + `","reason":"诉讼","frozen_on":"` + frozenOn + `","released":true}`
}

func frReleasedBeforeFreeze(id, frozenOn, releasedOn string) string {
	// 与完整解除记录同形，仅解除日期早于冻结日期。
	return frReleasedValid(id, frozenOn, releasedOn)
}

// assertCorruptFreezeError 校验打开错误：必须是 ErrCorruptState，信息中包含
// wantAll 的全部片段，并且不得出现 wantNone 中的任何片段（用于保证报告的是
// 按先后关系选出的那一个问题，而不是同时或提前报告另一个问题）。
func assertCorruptFreezeError(t *testing.T, err error, wantAll, wantNone []string) {
	t.Helper()
	if err == nil {
		t.Fatal("存在冻结历史问题时不应打开成功")
	}
	if !errors.Is(err, ErrCorruptState) {
		t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
	}
	msg := err.Error()
	for _, want := range wantAll {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误信息应包含 %q，得到 %v", want, err)
		}
	}
	for _, banned := range wantNone {
		if strings.Contains(msg, banned) {
			t.Fatalf("错误信息不应包含 %q（不应报告这个问题），得到 %v", banned, err)
		}
	}
}

// 原冻结信息缺项与解除信息错误并存时，无论解除问题在文本顺序上多么靠前，
// 都必须先报告原冻结信息缺项。
func TestFreezeOriginDefectsTakePrecedenceOverReleaseErrors(t *testing.T) {
	corrupt := map[string]struct {
		build    func() string
		wantAll  []string
		wantNone []string
	}{
		"题目主例_A-1解除缺原因_A-2冻结缺日期": {
			// A-1 的 F-9 已解除但缺少解除原因；A-2 的 F-1 缺少冻结日期。
			// 必须指出 A-2、F-1 与缺少冻结日期，不能先报告 A-1 的解除问题。
			build: func() string {
				return frState(
					frArchive("A-1", frReleasedMissingReason("F-9", "2025-01-05", "2025-01-06")) + "," +
						frArchive("A-2", frActiveMissingDate("F-1")))
			},
			wantAll:  []string{"A-2", "F-1", "冻结日期"},
			wantNone: []string{"A-1", "F-9", "解除"},
		},
		"调换档案保存位置_报告不变": {
			// 同一份逻辑内容，只改变两份档案在保存对象中的排列位置。
			build: func() string {
				return frState(
					frArchive("A-2", frActiveMissingDate("F-1")) + "," +
						frArchive("A-1", frReleasedMissingReason("F-9", "2025-01-05", "2025-01-06")))
			},
			wantAll:  []string{"A-2", "F-1", "冻结日期"},
			wantNone: []string{"A-1", "F-9", "解除"},
		},
		"同一档案_更早登记的冻结有解除问题_更晚登记的冻结有原信息缺项": {
			// F-1 先登记且已解除却缺解除日期；F-2 后登记但缺少冻结原因。
			// 原冻结信息缺项仍须先报告 F-2。
			build: func() string {
				return frState(frArchive("A-1",
					frReleasedMissingDate("F-1", "2025-01-05"),
					frActiveMissingReason("F-2", "2025-01-07")))
			},
			wantAll:  []string{"A-1", "F-2", "冻结原因"},
			wantNone: []string{"F-1", "解除"},
		},
		"同一档案_更早登记的冻结缺原信息_更晚登记的冻结有解除问题": {
			// 先登记的 F-1 缺冻结日期，遍历时立即报原信息缺项，
			// 后面 F-2 的解除日期倒置不应被提前报告。
			build: func() string {
				return frState(frArchive("A-1",
					frActiveMissingDate("F-1"),
					frReleasedBeforeFreeze("F-2", "2025-01-07", "2025-01-06")))
			},
			wantAll:  []string{"A-1", "F-1", "冻结日期"},
			wantNone: []string{"F-2", "解除"},
		},
		"A-1解除日期倒置_A-2冻结缺原因": {
			// 解除问题是日期倒置、原信息问题是缺原因时，先后关系同样成立。
			build: func() string {
				return frState(
					frArchive("A-1", frReleasedBeforeFreeze("F-1", "2025-01-05", "2025-01-04")) + "," +
						frArchive("A-2", frActiveMissingReason("F-9", "2025-02-01")))
			},
			wantAll:  []string{"A-2", "F-9", "冻结原因"},
			wantNone: []string{"A-1", "F-1", "解除"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			content := tc.build()
			dir := t.TempDir()
			writeStateFile(t, dir, content)
			s, err := Open(dir)
			if err == nil {
				s.Close()
			}
			assertCorruptFreezeError(t, err, tc.wantAll, tc.wantNone)
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}
}

// 同一条冻结同时缺少原冻结原因与冻结日期时，两项都必须说明；
// 解除信息再完整也不能代替原冻结信息。
func TestFreezeMissingBothOriginItemsReportsBoth(t *testing.T) {
	corrupt := map[string]struct {
		build    func() string
		wantAll  []string
		wantNone []string
	}{
		"未解除冻结两项都缺": {
			build: func() string {
				return frState(frArchive("A-1", `{"id":"F-1","released":false}`))
			},
			wantAll: []string{"A-1", "F-1", "冻结原因", "冻结日期"},
		},
		"已解除且解除信息完整_两项原信息仍都缺": {
			// 解除原因、解除日期完整不能顶替冻结原因与冻结日期。
			build: func() string {
				return frState(frArchive("A-1", frReleasedMissingBothOrigin("F-1", "2025-01-06")))
			},
			wantAll:  []string{"A-1", "F-1", "冻结原因", "冻结日期"},
			wantNone: []string{"解除原因", "解除日期"},
		},
		"该记录与另一档案的解除问题并存_仍先报这条原信息缺项": {
			build: func() string {
				return frState(
					frArchive("A-1", frReleasedMissingReason("F-7", "2025-01-05", "2025-01-06")) + "," +
						frArchive("A-2", frReleasedMissingBothOrigin("F-1", "2025-02-06")))
			},
			wantAll:  []string{"A-2", "F-1", "冻结原因", "冻结日期"},
			wantNone: []string{"A-1", "F-7", "解除"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			content := tc.build()
			dir := t.TempDir()
			writeStateFile(t, dir, content)
			s, err := Open(dir)
			if err == nil {
				s.Close()
			}
			assertCorruptFreezeError(t, err, tc.wantAll, tc.wantNone)
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}
}

// 同类原冻结信息缺项在多处出现时，按档案编号文本升序、再按冻结登记顺序选出
// 首个问题：冻结编号大小与冻结日期先后都不能替代登记顺序。
func TestFreezeOriginDefectSelectionOrdering(t *testing.T) {
	corrupt := map[string]struct {
		build    func() string
		wantAll  []string
		wantNone []string
	}{
		"两份档案都缺冻结日期_取文本最前者": {
			build: func() string {
				return frState(
					frArchive("A-1", frActiveMissingDate("F-1")) + "," +
						frArchive("A-2", frActiveMissingDate("F-1")))
			},
			wantAll:  []string{"A-1", "F-1", "冻结日期"},
			wantNone: []string{"A-2"},
		},
		"文本升序而非数字升序_A-10先于A-2": {
			// 按文本比较 "A-10" 在 "A-2" 之前，不能按数字大小挑选。
			build: func() string {
				return frState(
					frArchive("A-2", frActiveMissingDate("F-1")) + "," +
						frArchive("A-10", frActiveMissingDate("F-1")))
			},
			wantAll:  []string{"A-10", "F-1", "冻结日期"},
			wantNone: []string{"A-2"},
		},
		"同一档案_登记顺序先于冻结编号与日期": {
			// 先登记的 F-9（冻结日期更晚）缺原因，后登记的 F-1（冻结日期更早、
			// 编号更小）也缺原因：必须报先登记的 F-9。
			build: func() string {
				return frState(frArchive("A-1",
					frActiveMissingReason("F-9", "2025-03-01"),
					frActiveMissingReason("F-1", "2025-01-01")))
			},
			wantAll:  []string{"A-1", "F-9", "冻结原因"},
			wantNone: []string{"F-1"},
		},
		"先登记缺日期_后登记缺原因_仍按登记顺序选首条": {
			build: func() string {
				return frState(frArchive("A-1",
					frActiveMissingDate("F-2"),
					frActiveMissingReason("F-1", "2025-01-01")))
			},
			wantAll:  []string{"A-1", "F-2", "冻结日期"},
			wantNone: []string{"F-1"},
		},
		"只改变档案在保存内容中的排列_报告一致": {
			build: func() string {
				return frState(
					frArchive("A-2", frActiveMissingDate("F-1")) + "," +
						frArchive("A-1", frActiveMissingDate("F-1")))
			},
			wantAll:  []string{"A-1", "F-1", "冻结日期"},
			wantNone: []string{"A-2"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			content := tc.build()
			dir := t.TempDir()
			writeStateFile(t, dir, content)
			s, err := Open(dir)
			if err == nil {
				s.Close()
			}
			assertCorruptFreezeError(t, err, tc.wantAll, tc.wantNone)
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}
}

// 全部原冻结信息齐备后，才报告解除信息错误；解除错误同样按档案编号文本升序、
// 再按冻结登记顺序选出首个。同一条已解除记录同时缺解除原因与日期时先说明
// 解除原因；日期倒置时错误同时给出解除日期与冻结日期。
func TestReleaseErrorSelectionAfterOriginsComplete(t *testing.T) {
	corrupt := map[string]struct {
		build    func() string
		wantAll  []string
		wantNone []string
	}{
		"两份档案都有解除问题_取文本最前者": {
			// A-1 解除日期倒置，A-2 缺解除原因：先报文本更靠前的 A-1，
			// 且倒置信息同时给出解除日期与冻结日期。
			build: func() string {
				return frState(
					frArchive("A-2", frReleasedMissingReason("F-1", "2025-02-05", "2025-02-06")) + "," +
						frArchive("A-1", frReleasedBeforeFreeze("F-1", "2025-01-05", "2025-01-04")))
			},
			wantAll:  []string{"A-1", "F-1", "2025-01-04", "2025-01-05"},
			wantNone: []string{"A-2", "F-9"},
		},
		"文本升序而非数字升序_A-10先于A-2": {
			build: func() string {
				return frState(
					frArchive("A-2", frReleasedMissingReason("F-1", "2025-02-05", "2025-02-06")) + "," +
						frArchive("A-10", frReleasedMissingDate("F-1", "2025-03-01")))
			},
			wantAll:  []string{"A-10", "F-1", "解除日期"},
			wantNone: []string{"A-2"},
		},
		"同一档案_登记顺序先于冻结编号": {
			// 先登记的 F-9 缺解除原因，后登记的 F-1 解除日期倒置：
			// 报先登记的 F-9，不按编号大小选 F-1。
			build: func() string {
				return frState(frArchive("A-1",
					frReleasedMissingReason("F-9", "2025-01-05", "2025-01-06"),
					frReleasedBeforeFreeze("F-1", "2025-01-07", "2025-01-06")))
			},
			wantAll:  []string{"A-1", "F-9", "解除原因"},
			wantNone: []string{"F-1"},
		},
		"同一条已解除记录同时缺解除原因与日期_先说明原因": {
			build: func() string {
				return frState(frArchive("A-1", frReleasedMissingReasonAndDate("F-1", "2025-01-05")))
			},
			wantAll:  []string{"A-1", "F-1", "解除原因"},
			wantNone: []string{"解除日期"},
		},
		"只改变档案在保存内容中的排列_解除错误报告一致": {
			build: func() string {
				return frState(
					frArchive("A-1", frReleasedBeforeFreeze("F-1", "2025-01-05", "2025-01-04")) + "," +
						frArchive("A-2", frReleasedMissingReason("F-1", "2025-02-05", "2025-02-06")))
			},
			wantAll:  []string{"A-1", "F-1", "2025-01-04", "2025-01-05"},
			wantNone: []string{"A-2"},
		},
	}
	for name, tc := range corrupt {
		t.Run(name, func(t *testing.T) {
			content := tc.build()
			dir := t.TempDir()
			writeStateFile(t, dir, content)
			s, err := Open(dir)
			if err == nil {
				s.Close()
			}
			assertCorruptFreezeError(t, err, tc.wantAll, tc.wantNone)
			if got := readStateFile(t, dir); got != content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}
}

// 保管库打开后保存内容才同时出现原冻结信息缺项与解除信息错误：重新打开必须
// 失败且按先后关系报告原冻结信息缺项；已有实例的任何读取与办理（含操作另一
// 份无关的正常档案）都必须按整库损坏失败，不返回部分历史、清册或部分核对
// 报告，不产生业务变更，原保存内容保持原样。
func TestFreezeMixedErrorsAfterOpenFailsAllOperations(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2026-01-10")
	reg(t, s, "A-2", "凭证", "2020-01-01", "2026-06-30")
	reg(t, s, "A-3", "单据", "2020-01-01", "2026-06-30")
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-06"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-2", FreezeID: "F-1", Reason: "协查", FrozenOn: MustParseDate("2025-02-05"),
	}); err != nil {
		t.Fatal(err)
	}

	// 同时制造两类问题：A-1 的已解除冻结删掉解除原因（解除信息错误），
	// A-2 的未解除冻结删掉冻结日期（原冻结信息缺项）。
	rewriteState(t, dir, func(doc map[string]any) {
		a1f := doc["archives"].(map[string]any)["A-1"].(map[string]any)["freezes"].([]any)[0].(map[string]any)
		delete(a1f, "release_reason")
		a2f := doc["archives"].(map[string]any)["A-2"].(map[string]any)["freezes"].([]any)[0].(map[string]any)
		delete(a2f, "frozen_on")
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败，报告的是 A-2/F-1 缺少冻结日期，而不是 A-1 的解除问题。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("两类冻结问题并存时打开必须失败")
	}
	if !errors.Is(err, ErrCorruptState) {
		t.Fatalf("打开错误应为 ErrCorruptState，得到 %v", err)
	}
	for _, want := range []string{"A-2", "F-1", "冻结日期"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("打开错误应包含 %q，得到 %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "A-1") || strings.Contains(err.Error(), "解除") {
		t.Fatalf("不应先报告 A-1 的解除问题，得到 %v", err)
	}

	// 已有实例：损坏档案、带解除问题的档案、无关的正常档案 A-3 全部失败，
	// 且不返回部分历史。
	for _, id := range []string{"A-1", "A-2", "A-3"} {
		if h, found, herr := s.History(id); !errors.Is(herr, ErrCorruptState) || found || h.ID != "" {
			t.Fatalf("损坏后 History(%s) 必须失败且无部分历史: found=%v err=%v", id, found, herr)
		}
	}
	if m, found, err := s.GetManifest("APP-1"); !errors.Is(err, ErrCorruptState) || found || m.ApplicationID != "" {
		t.Fatalf("损坏后 GetManifest 必须失败: found=%v err=%v %+v", found, err, m)
	}
	if r, err := s.Check(CheckRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2026-06-30"), ArchiveIDs: []string{"A-3"},
	}); !errors.Is(err, ErrCorruptState) || r.Status != "" || len(r.Results) != 0 {
		t.Fatalf("损坏后 Check 必须失败且不得给出部分报告: %+v err=%v", r, err)
	}
	if m, err := s.Destroy(DestructionRequest{
		ApplicationID: "APP-9", ProcessedOn: MustParseDate("2026-06-30"), ArchiveIDs: []string{"A-3"},
	}); !errors.Is(err, ErrCorruptState) || m.ApplicationID != "" {
		t.Fatalf("损坏后 Destroy 必须失败且不得生成清册: %+v err=%v", m, err)
	}
	if err := s.Register(RegisterInput{
		ID: "A-4", Category: "凭证", Start: MustParseDate("2020-01-01"), End: MustParseDate("2026-06-30"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Register 必须失败: %v", err)
	}
	if err := s.Freeze(FreezeInput{
		ArchiveID: "A-3", FreezeID: "F-X", Reason: "诉讼", FrozenOn: MustParseDate("2026-01-01"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Freeze 必须失败: %v", err)
	}
	if err := s.Release(ReleaseInput{
		ArchiveID: "A-1", FreezeID: "F-1", Reason: "重新结案", ReleasedOn: MustParseDate("2026-01-02"),
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Release 必须失败: %v", err)
	}
	if _, err := s.Revise(ReviseInput{
		RevisionID: "R-9", ArchiveID: "A-3",
		OriginalEnd: MustParseDate("2026-06-30"), NewEnd: MustParseDate("2027-06-30"),
		RevisedOn: MustParseDate("2026-01-01"), Reason: "延期",
	}); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("损坏后 Revise 必须失败: %v", err)
	}

	// 失败不得触发修复或借办理重新保存，原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("失败后原保存内容被改动:\n%q", got)
	}
}

// 合法情形对照组：未解除冻结没有解除信息仍是正常情况；解除日期等于冻结日期且
// 信息完整的记录可以读取，全部冻结历史按登记顺序保持完整。
func TestValidFreezeHistoryUnchangedByPrecedenceChecks(t *testing.T) {
	t.Run("未解除冻结没有解除信息_合法", func(t *testing.T) {
		content := frState(frArchive("A-1", frActive("F-1", "2025-01-05")))
		dir := t.TempDir()
		writeStateFile(t, dir, content)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("未解除冻结无解除信息应能打开: %v", err)
		}
		defer s.Close()
		h, found, err := s.History("A-1")
		if err != nil || !found {
			t.Fatalf("历史应可查: found=%v err=%v", found, err)
		}
		if len(h.Freezes) != 1 || h.Freezes[0].Released {
			t.Fatalf("未解除冻结应原样保留: %+v", h.Freezes)
		}
	})

	t.Run("解除日期等于冻结日期_合法且历史完整", func(t *testing.T) {
		// 一条解除日期等于冻结日期的已解除冻结，加一条更晚登记的未解除冻结，
		// 再放一份带冻结的正常档案：全部记录都应按登记顺序完整保留。
		content := frState(
			frArchive("A-1",
				frReleasedValid("F-1", "2025-01-05", "2025-01-05"),
				frActive("F-2", "2025-01-07")) + "," +
				frArchive("A-2", frActive("F-1", "2025-02-01")))
		dir := t.TempDir()
		writeStateFile(t, dir, content)
		s, err := Open(dir)
		if err != nil {
			t.Fatalf("解除日期等于冻结日期的完整记录应能打开: %v", err)
		}
		defer s.Close()

		h1, found, err := s.History("A-1")
		if err != nil || !found {
			t.Fatalf("A-1 历史应可查: found=%v err=%v", found, err)
		}
		if len(h1.Freezes) != 2 {
			t.Fatalf("A-1 的全部冻结历史都应保留，得到 %d 条", len(h1.Freezes))
		}
		// 登记顺序不变：先 F-1（已解除，解除日期等于冻结日期），再 F-2（未解除）。
		f1 := h1.Freezes[0]
		if f1.ID != "F-1" || !f1.Released ||
			!f1.FrozenOn.Equal(MustParseDate("2025-01-05")) ||
			!f1.ReleasedOn.Equal(MustParseDate("2025-01-05")) ||
			f1.ReleaseReason != "结案" {
			t.Fatalf("已解除冻结信息应完整保留: %+v", f1)
		}
		f2 := h1.Freezes[1]
		if f2.ID != "F-2" || f2.Released || f2.Reason != "诉讼" {
			t.Fatalf("后登记的未解除冻结应原样保留: %+v", f2)
		}
		if len(h1.ActiveFreezes) != 1 || h1.ActiveFreezes[0].ID != "F-2" {
			t.Fatalf("未解除冻结清单应只含 F-2: %+v", h1.ActiveFreezes)
		}

		h2, found, err := s.History("A-2")
		if err != nil || !found || len(h2.Freezes) != 1 || h2.Freezes[0].ID != "F-1" {
			t.Fatalf("A-2 冻结历史应完整: found=%v err=%v %+v", found, err, h2.Freezes)
		}
	})
}
