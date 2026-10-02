# 本地保管期限与销毁清册

这是一个在本机运行的本地保管期限与销毁清册。只登记条目元数据，不读取或保存真实文件。

## 使用

```bash
go test ./...
```

调用者先选择一个本地保存位置并打开，然后即可办理各项业务：

```go
s, _ := retention.Open("/path/to/vault")
defer s.Close()

s.Register(retention.RegisterInput{
    ID: "A-001", Category: "合同",
    Start: retention.MustParseDate("2020-01-01"),
    End:   retention.MustParseDate("2025-01-10"),
})

s.Freeze(retention.FreezeInput{
    ArchiveID: "A-001", FreezeID: "F-1",
    Reason: "诉讼", FrozenOn: retention.MustParseDate("2025-01-08"),
})
s.Release(retention.ReleaseInput{
    ArchiveID: "A-1", FreezeID: "F-1",
    Reason: "结案", ReleasedOn: retention.MustParseDate("2025-01-09"),
})

m, err := s.Destroy(retention.DestructionRequest{
    ApplicationID: "APP-1",
    ProcessedOn:   retention.MustParseDate("2025-01-10"), // 截止日当天即到期
    ArchiveIDs:    []string{"A-001"},
})
_ = m

// 销毁前可先做一次只核对：不销毁档案、不生成清册、不占用申请编号。
report, _ := s.Check(retention.CheckRequest{
    ApplicationID: "APP-2",
    ProcessedOn:   retention.MustParseDate("2025-01-10"),
    ArchiveIDs:    []string{"A-001", "A-002"},
})
_ = report

h, found, _ := s.History("A-001") // 登记内容、全部冻结解除历史、销毁状态与清册
_, found, _ = s.GetManifest("APP-1")
```

## 规则要点

- 日期统一 `YYYY-MM-DD`，按日历日期比较，不受时区或夏令时影响；日期必须真实存在。
- 登记：编号、类别不可空白；截止日不能早于起算日；重复编号明确失败且不影响已有记录。
- 冻结：同一档案可有多条，冻结编号同档案内唯一；解除必须填日期和原因，解除日期不早于冻结日期；
  解除只做标记，记录全部保留；不能给不存在或已销毁的档案新增冻结。
- 销毁：一次可多选；只有全部存在、到期、未销毁、无未解除冻结时才整体成功并生成已关闭清册。
  空名单、名单重复或任一不符条件时整次失败，不留部分销毁或清册。
- 核对（Check）：销毁前只做核对，不销毁档案、不生成或改写清册、不占用申请编号，失败后可沿用该编号继续核对或正式提交。
  申请编号已成功使用且日期、集合与原申请相同（顺序无关）时显示可以取回原清册；沿用编号改日期或集合则显示申请编号冲突并附原清册。
  申请编号未成功使用时按提交顺序逐份核对：已登记档案显示类别、起算日、截止日和销毁状态，不存在的编号单独标明；
  每份档案列出全部适用阻碍（不存在、已销毁、未到期、未解除冻结），未到期与多条冻结并存时两类都显示，每条冻结带编号、原因和冻结日期，顺序沿用冻结历史。
  只有所有档案均无阻碍才显示可以办理。编号去除首尾空白后判断；编号空白、日期缺失或不真实、名单为空或含重复编号（含仅首尾空白不同）时整次核对明确失败，不返回部分报告。
- 幂等：已成功的申请以相同日期和档案集合（顺序无关）重放返回原清册；沿用编号改日期或集合则失败；
  失败的申请条件改变后可用同编号重试。
- 并发：多个本机程序同时办理时以文件锁决定先后，先成功的冻结挡住销毁，先成功的销毁挡住后续冻结。
