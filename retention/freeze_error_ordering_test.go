package retention

import (
	"errors"
	"strings"
	"testing"
)

// 多条冻结同时存在问题时，调用者得到的错误说明必须遵守既有先后关系：
//
//   - 原冻结信息缺项（缺少冻结原因或冻结日期）整体优先于解除信息错误
//     （已解除却缺少解除原因、解除日期，或解除日期早于冻结日期）：即使解除
//     问题属于编号更靠前的档案，或出现在同一档案更早登记的冻结中，也必须先
//     报告按既定顺序选出的原冻结信息缺项；
//   - 同类问题出现多处时，按档案编号的文本升序、再按该档案冻结历史的登记
//     顺序选出首个问题——冻结编号大小与日期先后都不能替代登记顺序，不同档案
//     在保存内容中的排列位置也不能改变报告；
//   - 同一条冻结同时缺少原冻结原因与冻结日期时两项都说明，完整的解除信息
//     不能代替原冻结信息；同一条已解除记录同时缺少解除原因与解除日期时先
//     说明解除原因；日期倒置时同时给出解除日期与冻结日期。
//
// 本文件只补充这些错误共同出现时的回归保障，不改变任何办理规则与错误类别：
// 命中时仍是 ErrCorruptState，错误仍带档案编号、冻结编号与具体问题。

// frz 拼装一条冻结记录，body 是紧跟在 id 之后的字段片段（需自带前导逗号）。
func frz(id, body string) string {
	return `{"id":"` + id + `",` + body + `}`
}

// freezeErrorState 拼装一个只含未销毁档案、不含清册的保存记录，
// archives 按传入顺序排列，用于验证档案在保存内容中的排列位置不影响报告。
func freezeErrorState(archives ...string) string {
	return `{"version":1,"archives":{` + strings.Join(archives, ",") + `},"manifests":{}}`
}

func freezeErrorArchive(id string, freezes ...string) string {
	return `"` + id + `":{"id":"` + id +
		`","category":"合同","start":"2020-01-01","end":"2026-12-31",` +
		`"initial_end":"2026-12-31","destroyed":false,"freezes":[` +
		strings.Join(freezes, ",") + `]}`
}

// 原冻结信息缺项与解除信息错误共同出现时，必须先报告原冻结信息缺项。
func TestFreezeCorruptionOriginErrorsTakePrecedence(t *testing.T) {
	const (
		// 已解除但缺少解除原因（解除信息错误）。
		releasedMissingReason = `"reason":"诉讼","frozen_on":"2025-01-05","released":true,"released_on":"2025-01-06"`
		// 已解除但缺少解除日期（解除信息错误）。
		releasedMissingDate = `"reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案"`
		// 解除日期早于冻结日期（解除信息错误）。
		releasedBeforeFrozen = `"reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-04"`
		// 缺少冻结日期的未解除冻结（原冻结信息缺项）。
		missingFrozenDate = `"reason":"诉讼","released":false`
		// 同时缺少冻结原因与冻结日期，但解除信息完整：解除信息不能补齐原冻结信息。
		releasedMissingBothOrigin = `"released":true,"release_reason":"结案","released_on":"2025-01-06"`
	)

	cases := map[string]struct {
		content  string
		wantAll  []string // 错误信息必须全部包含
		wantNone []string // 错误信息不得包含（被优先级压下的问题）
	}{
		"题目主例：编号靠前档案的解除问题让位于编号靠后档案的原冻结缺项": {
			// A-1 的 F-9 缺少解除原因，A-2 的 F-1 缺少冻结日期：
			// 必须指出 A-2、F-1 和缺少冻结日期，不能先报告 A-1 的解除问题。
			content: freezeErrorState(
				freezeErrorArchive("A-1", frz("F-9", releasedMissingReason)),
				freezeErrorArchive("A-2", frz("F-1", missingFrozenDate)),
			),
			wantAll:  []string{"A-2", "F-1", "冻结日期"},
			wantNone: []string{"A-1", "F-9", "解除"},
		},
		"调换两份档案在保存内容中的先后不改变报告": {
			// 与主例只有档案排列位置不同（A-2 写在前面），报告必须一致。
			content: freezeErrorState(
				freezeErrorArchive("A-2", frz("F-1", missingFrozenDate)),
				freezeErrorArchive("A-1", frz("F-9", releasedMissingReason)),
			),
			wantAll:  []string{"A-2", "F-1", "冻结日期"},
			wantNone: []string{"A-1", "F-9", "解除"},
		},
		"同一档案更早登记冻结的解除问题也要让位于后登记冻结的原冻结缺项": {
			// F-1 登记在前且已解除但缺少解除日期，F-2 登记在后且缺少冻结日期：
			// 必须报告 F-2 的原冻结缺项，不能按登记顺序先报 F-1 的解除问题。
			content: freezeErrorState(
				freezeErrorArchive("A-1",
					frz("F-1", releasedMissingDate),
					frz("F-2", missingFrozenDate),
				),
			),
			wantAll:  []string{"A-1", "F-2", "冻结日期"},
			wantNone: []string{"F-1", "解除"},
		},
		"同一档案更早登记冻结的日期倒置同样让位于后登记冻结的原冻结缺项": {
			content: freezeErrorState(
				freezeErrorArchive("A-1",
					frz("F-1", releasedBeforeFrozen),
					frz("F-2", `"frozen_on":"2025-02-01","released":false`),
				),
			),
			// F-2 缺少冻结原因。
			wantAll:  []string{"A-1", "F-2", "冻结原因"},
			wantNone: []string{"F-1", "解除", "2025-01-04"},
		},
		"完整的解除信息不能代替同一条冻结缺失的原冻结原因与日期": {
			// 已解除、解除原因与解除日期都完整，但原冻结原因与冻结日期都缺失：
			// 两项原冻结缺项都要说明，不能被解除信息顶替。
			content: freezeErrorState(
				freezeErrorArchive("A-1", frz("F-1", releasedMissingBothOrigin)),
			),
			wantAll:  []string{"A-1", "F-1", "冻结原因", "冻结日期"},
			wantNone: []string{},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.content)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("原冻结信息缺项与解除信息错误共同出现时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			for _, want := range tc.wantAll {
				if !strings.Contains(msg, want) {
					t.Fatalf("错误信息应包含 %q，得到 %v", want, err)
				}
			}
			for _, banned := range tc.wantNone {
				if strings.Contains(msg, banned) {
					t.Fatalf("错误信息不应包含被优先级压下的 %q，得到 %v", banned, err)
				}
			}
			// 打开失败不交付可用保管库，原保存内容保持原样。
			if got := readStateFile(t, dir); got != tc.content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}
}

// 同类问题出现多处时，首个问题按档案编号文本升序、再按冻结历史登记顺序选出：
// 冻结编号大小、冻结日期先后与档案在保存内容中的排列位置都不能替代这一顺序。
func TestFreezeCorruptionFirstErrorOrdering(t *testing.T) {
	cases := map[string]struct {
		content  string
		wantAll  []string
		wantNone []string
	}{
		// —— 原冻结信息缺项之间的顺序 ——
		"原冻结缺项按登记顺序而非冻结编号大小": {
			// F-9 登记在前（缺原因），F-1 登记在后（缺日期）：
			// 按编号文本会先选 F-1，按登记顺序必须选 F-9。
			content: freezeErrorState(
				freezeErrorArchive("A-1",
					frz("F-9", `"frozen_on":"2025-01-05","released":false`),
					frz("F-1", `"reason":"诉讼","released":false`),
				),
			),
			wantAll:  []string{"A-1", "F-9", "冻结原因"},
			wantNone: []string{"F-1"},
		},
		"原冻结缺项按登记顺序而非冻结日期先后": {
			// 登记在前的冻结日期反而更晚（缺原因），日期更早的冻结登记在后
			// （缺日期）：不能按日期挑出后者。
			content: freezeErrorState(
				freezeErrorArchive("A-1",
					frz("F-1", `"reason":"诉讼","frozen_on":"2025-03-01","released":false`),
					frz("F-2", `"reason":"诉讼","released":false`),
				),
			),
			wantAll:  []string{"A-1", "F-2", "冻结日期"},
			wantNone: []string{"F-1"},
		},
		"原冻结缺项跨档案按编号文本升序（保存顺序 A-1 在前）": {
			content: freezeErrorState(
				freezeErrorArchive("A-1", frz("F-1", `"reason":"诉讼","released":false`)),
				freezeErrorArchive("A-2", frz("F-1", `"frozen_on":"2025-01-05","released":false`)),
			),
			wantAll:  []string{"A-1", "F-1", "冻结日期"},
			wantNone: []string{"A-2"},
		},
		"原冻结缺项跨档案按编号文本升序（保存顺序 A-2 在前）": {
			// 仅调换档案排列位置，报告必须与上一例一致。
			content: freezeErrorState(
				freezeErrorArchive("A-2", frz("F-1", `"frozen_on":"2025-01-05","released":false`)),
				freezeErrorArchive("A-1", frz("F-1", `"reason":"诉讼","released":false`)),
			),
			wantAll:  []string{"A-1", "F-1", "冻结日期"},
			wantNone: []string{"A-2"},
		},
		"同档案内缺日期登记在先要先于后登记的缺原因报告": {
			// 原冻结缺项是同一大类：登记顺序先于“原因先于日期”的条内规则，
			// 只有同一条记录两项同缺时才两项都说明。
			content: freezeErrorState(
				freezeErrorArchive("A-1",
					frz("F-1", `"reason":"诉讼","released":false`),
					frz("F-2", `"frozen_on":"2025-01-05","released":false`),
				),
			),
			wantAll:  []string{"A-1", "F-1", "冻结日期"},
			wantNone: []string{"F-2", "冻结原因"},
		},

		// —— 全部原冻结信息齐备后，解除信息错误才按同一顺序报告 ——
		"解除错误按登记顺序而非冻结编号大小": {
			content: freezeErrorState(
				freezeErrorArchive("A-1",
					frz("F-9", `"reason":"诉讼","frozen_on":"2025-01-05","released":true,"released_on":"2025-01-06"`),
					frz("F-1", `"reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-04"`),
				),
			),
			// F-9 登记在前且缺解除原因；F-1 的日期倒置必须被压下。
			wantAll:  []string{"A-1", "F-9", "解除原因"},
			wantNone: []string{"F-1", "早于"},
		},
		"解除错误按登记顺序而非冻结日期先后": {
			content: freezeErrorState(
				freezeErrorArchive("A-1",
					frz("F-1", `"reason":"诉讼","frozen_on":"2025-03-01","released":true,"released_on":"2025-03-02"`),
					frz("F-2", `"reason":"诉讼","frozen_on":"2025-01-01","released":true,"release_reason":"结案","released_on":"2024-12-31"`),
				),
			),
			// F-1 登记在前（缺解除原因），F-2 虽日期更早且倒置也只能先报 F-1。
			wantAll:  []string{"A-1", "F-1", "解除原因"},
			wantNone: []string{"F-2", "早于"},
		},
		"解除错误跨档案按编号文本升序（保存顺序 A-1 在前）": {
			content: freezeErrorState(
				freezeErrorArchive("A-1", frz("F-1", `"reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案"`)),
				freezeErrorArchive("A-2", frz("F-1", `"reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-04"`)),
			),
			// A-1 缺解除日期；A-2 的日期倒置必须被压下。
			wantAll:  []string{"A-1", "F-1", "解除日期"},
			wantNone: []string{"A-2", "早于"},
		},
		"解除错误跨档案按编号文本升序（保存顺序 A-2 在前）": {
			content: freezeErrorState(
				freezeErrorArchive("A-2", frz("F-1", `"reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-04"`)),
				freezeErrorArchive("A-1", frz("F-1", `"reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案"`)),
			),
			wantAll:  []string{"A-1", "F-1", "解除日期"},
			wantNone: []string{"A-2", "早于"},
		},
		"同一条已解除记录同时缺解除原因与日期时先说明解除原因": {
			content: freezeErrorState(
				freezeErrorArchive("A-1",
					frz("F-1", `"reason":"诉讼","frozen_on":"2025-01-05","released":true`),
				),
			),
			wantAll: []string{"A-1", "F-1", "解除原因"},
		},
		"日期倒置错误同时给出解除日期与冻结日期": {
			content: freezeErrorState(
				freezeErrorArchive("A-1",
					frz("F-1", `"reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-04"`),
				),
			),
			wantAll: []string{"A-1", "F-1", "解除日期", "2025-01-04", "冻结日期", "2025-01-05"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeStateFile(t, dir, tc.content)

			s, err := Open(dir)
			if err == nil {
				s.Close()
				t.Fatal("存在同类冻结问题时不应打开成功")
			}
			if !errors.Is(err, ErrCorruptState) {
				t.Fatalf("错误应可判定为 ErrCorruptState，得到 %v", err)
			}
			msg := err.Error()
			for _, want := range tc.wantAll {
				if !strings.Contains(msg, want) {
					t.Fatalf("错误信息应包含 %q，得到 %v", want, err)
				}
			}
			for _, banned := range tc.wantNone {
				if strings.Contains(msg, banned) {
					t.Fatalf("错误信息不应包含顺序靠后的 %q，得到 %v", banned, err)
				}
			}
			if got := readStateFile(t, dir); got != tc.content {
				t.Fatalf("打开失败后原文件被改动:\n%q", got)
			}
		})
	}
}

// 保管库打开后保存内容才同时出现原冻结缺项与解除错误：重新打开必须失败且不
// 交付可用保管库，已有实例读取历史（含查询另一份正常档案）也必须按同样的
// 先后关系失败且不返回部分历史，原保存内容保持原样。
func TestFreezeCorruptionOrderingAfterOpenFailsReads(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	reg(t, s, "A-1", "合同", "2020-01-01", "2026-12-31")
	reg(t, s, "A-2", "合同", "2020-01-01", "2026-12-31")
	reg(t, s, "A-3", "凭证", "2020-01-01", "2026-12-31")
	// A-1：F-1 已合法解除，F-2 未解除。
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ReleaseInput{ArchiveID: "A-1", FreezeID: "F-1", Reason: "结案", ReleasedOn: MustParseDate("2025-01-06")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Freeze(FreezeInput{ArchiveID: "A-1", FreezeID: "F-2", Reason: "审计", FrozenOn: MustParseDate("2025-02-01")}); err != nil {
		t.Fatal(err)
	}
	// A-2：一条合法冻结。
	if err := s.Freeze(FreezeInput{ArchiveID: "A-2", FreezeID: "F-1", Reason: "诉讼", FrozenOn: MustParseDate("2025-01-05")}); err != nil {
		t.Fatal(err)
	}

	// 同时制造两类问题：A-1/F-1 去掉解除原因（解除信息错误），
	// A-2/F-1 去掉冻结日期（原冻结信息缺项）。
	rewriteState(t, dir, func(doc map[string]any) {
		a1 := doc["archives"].(map[string]any)["A-1"].(map[string]any)
		delete(a1["freezes"].([]any)[0].(map[string]any), "release_reason")
		a2 := doc["archives"].(map[string]any)["A-2"].(map[string]any)
		delete(a2["freezes"].([]any)[0].(map[string]any), "frozen_on")
	})
	corruptContent := readStateFile(t, dir)

	// 重新打开必须失败：按先后关系报告 A-2/F-1 的原冻结缺项，
	// 而不是 A-1/F-1 的解除问题，且不交付可用保管库。
	s2, err := Open(dir)
	if err == nil {
		s2.Close()
		t.Fatal("损坏共同出现时重新打开必须失败")
	}
	if s2 != nil {
		t.Fatal("打开失败不应交付可用保管库")
	}
	if !errors.Is(err, ErrCorruptState) {
		t.Fatalf("打开错误应为 ErrCorruptState，得到 %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"A-2", "F-1", "冻结日期"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("打开错误应包含 %q，得到 %v", want, err)
		}
	}
	if strings.Contains(msg, "解除") || strings.Contains(msg, "A-1") {
		t.Fatalf("不应先报告 A-1 的解除问题，得到 %v", err)
	}

	// 已有实例：直接读有问题的两份档案必须失败且不返回部分历史；
	// 查询另一份完全正常的档案 A-3 也同样失败。
	for _, id := range []string{"A-1", "A-2", "A-3"} {
		h, found, err := s.History(id)
		if !errors.Is(err, ErrCorruptState) {
			t.Fatalf("损坏后 History(%s) 必须返回 ErrCorruptState，得到 found=%v err=%v", id, found, err)
		}
		if found || h.ID != "" || len(h.Freezes) != 0 {
			t.Fatalf("损坏后 History(%s) 不得返回部分历史: found=%v history=%+v", id, found, h)
		}
	}

	// 读取失败不得触发重新保存，原保存内容保持原样。
	if got := readStateFile(t, dir); got != corruptContent {
		t.Fatalf("读取失败后原保存内容被改动:\n%q", got)
	}
}

// 合法情形对照组：未解除冻结没有解除信息仍是正常情况；解除日期等于冻结日期
// 且信息完整的记录仍可读取；全部冻结历史（未解除、当天解除、隔日解除）保持完整。
func TestFreezeCorruptionOrderingValidRecordsUnaffected(t *testing.T) {
	content := freezeErrorState(
		freezeErrorArchive("A-1",
			// 未解除：没有 release_reason / released_on 合法。
			frz("F-1", `"reason":"诉讼","frozen_on":"2025-01-05","released":false`),
			// 解除日期等于冻结日期：合法。
			frz("F-2", `"reason":"诉讼","frozen_on":"2025-01-05","released":true,"release_reason":"结案","released_on":"2025-01-05"`),
			// 隔日解除：合法。
			frz("F-3", `"reason":"审计","frozen_on":"2025-02-01","released":true,"release_reason":"完成","released_on":"2025-02-03"`),
		),
	)
	dir := t.TempDir()
	writeStateFile(t, dir, content)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("合法冻结历史应能打开: %v", err)
	}
	defer s.Close()

	h, found, err := s.History("A-1")
	if err != nil || !found {
		t.Fatalf("合法冻结历史应可查: found=%v err=%v", found, err)
	}
	if len(h.Freezes) != 3 {
		t.Fatalf("全部冻结历史应保持完整，得到 %d 条: %+v", len(h.Freezes), h.Freezes)
	}
	if h.Freezes[0].Released || h.Freezes[0].ReleaseReason != "" || !h.Freezes[0].ReleasedOn.IsZero() {
		t.Fatalf("F-1 应保持未解除且无解除信息: %+v", h.Freezes[0])
	}
	if !h.Freezes[1].Released ||
		!h.Freezes[1].ReleasedOn.Equal(h.Freezes[1].FrozenOn) ||
		h.Freezes[1].ReleaseReason != "结案" {
		t.Fatalf("F-2 应保留当天解除的完整信息: %+v", h.Freezes[1])
	}
	if !h.Freezes[2].Released ||
		h.Freezes[2].ReleasedOn.String() != "2025-02-03" ||
		h.Freezes[2].Reason != "审计" {
		t.Fatalf("F-3 应保留隔日解除的完整信息: %+v", h.Freezes[2])
	}
	if len(h.ActiveFreezes) != 1 || h.ActiveFreezes[0].ID != "F-1" {
		t.Fatalf("仅 F-1 仍算作未解除冻结，得到 %+v", h.ActiveFreezes)
	}
}
