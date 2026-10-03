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

// 销毁前可先只做核对：不销毁、不生成清册、不占用申请编号。
r, _ := s.Check(retention.CheckRequest{
    ApplicationID: "APP-2",
    ProcessedOn:   retention.MustParseDate("2025-01-10"),
    ArchiveIDs:    []string{"A-001"},
})
switch r.Status {
case retention.CheckReady:       // 可以办理
case retention.CheckBlocked:     // 存在阻碍，见 r.Results
case retention.CheckReplayable:  // 可以取回原清册，见 r.Manifest
case retention.CheckConflict:    // 申请编号冲突，r.Manifest 附原清册
}

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
- 修订：尚未销毁的档案（含被冻结的）可延长或缩短保管截止日，不必重新登记，编号、类别、起算日不变；
  新截止日不早于起算日且不等于原截止日。首次办理时所提交的原截止日必须与当前保存值一致，
  否则返回期限已变化错误并附当前截止日。修订编号全库唯一、与销毁申请编号互不占用；
  同编号同内容重试取回首次记录，改任何一项即编号冲突；失败不留历史，失败编号可再提交。
  打开保存位置时还会核对已保存历史：每个成功修订编号只能对应一条保存的修订记录，
  同一档案历史中重复编号、不同档案保存同号修订（即使内容相同）或编号与已关闭清册
  申请编号相同，都按保存记录损坏拒绝打开（ErrCorruptState），不合并、不挑选记录；
  保管库打开后保存内容才出现此类冲突的，下一次查询、核对或办理同样报记录损坏，
  冲突属于整库且原保存内容保持原样，不自动改编号、删除历史或重建清册。
  历史核对同时给出最初截止日、当前截止日和按成功顺序排列的全部修订记录。
- 核对：销毁前的只读核对，一次看清整批所有阻碍。申请编号尚未成功使用时，按提交顺序逐份给出
  类别、起算日、截止日、销毁状态和全部适用阻碍（不存在、已销毁、未到期、仍有未解除冻结，可逐一识别）；
  未到期与多条未解除冻结并列显示，每条冻结带编号、原因和冻结日期；已销毁档案附所属清册申请编号与处理日期；
  全部无阻碍才显示可以办理。申请编号已成功使用时，日期与档案集合相同（顺序无关）显示可以取回原清册，
  改变日期或集合则显示申请编号冲突，二者均附原清册。空白编号/日期、空名单、重复编号或读取记录失败时
  整次核对明确失败，不返回部分报告。
- 销毁：一次可多选；只有全部存在、到期、未销毁、无未解除冻结时才整体成功并生成已关闭清册。
  空名单、名单重复或任一不符条件时整次失败，不留部分销毁或清册。
- 幂等：已成功的申请以相同日期和档案集合（顺序无关）重放返回原清册；沿用编号改日期或集合则失败；
  失败的申请条件改变后可用同编号重试。
- 并发：多个本机程序同时办理时以文件锁决定先后，先成功的冻结挡住销毁，先成功的销毁挡住后续冻结。
  核对在共享锁内读取同一已保存状态，一份报告不会混入操作前后的不同结果；核对之后状态若变化，
  正式提交仍按最新状态判断。
