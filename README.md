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
- 幂等：已成功的申请以相同日期和档案集合（顺序无关）重放返回原清册；沿用编号改日期或集合则失败；
  失败的申请条件改变后可用同编号重试。
- 并发：多个本机程序同时办理时以文件锁决定先后，先成功的冻结挡住销毁，先成功的销毁挡住后续冻结。
