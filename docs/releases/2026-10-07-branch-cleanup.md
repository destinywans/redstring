# main 合并与分支清理

2026-10-07，WAN-117。用户明确授权把已验证代码合入 main，并清理本地及远端旧分支。已完成 Git 整理；这不是部署或完整产品验收。

## main 与代码

`main` / `origin/main` 从 `a1d8391dd2a01ad91617461f45ff088998c9467b` 快进到已验证的 `bde30d557471728ff59facd990c2d0b2bcada4da`，包含此前 V3 前后端/集成、testlab 修复、成员授权/MCP/调查入口及记忆/评测/治理。本次未做冲突解析、重写历史或把旧分支变体覆盖回产品代码。合并时 tree 精确保持 `892951ae0c2a9c29e1f09f4631685c0e75981d65`；本记录所在后续提交只更新整理文档。

复用[最近实现的验证证据](2026-10-07-runtime-production.md)，未重复运行产品测试、迁移或启动外部环境。此前未验/暂停项仍保持原状态；归档标签不表示对应变体已经合入 main。

## 清理结果与保护

| 项目 | 清理前 | 清理后 |
| --- | ---: | ---: |
| 本地分支 | 165 | 1（main） |
| GitHub 分支 | 144 | 1（main） |
| 本次远端恢复标签 | 0 | 30 |

删除本地 164、远端 143 个旧分支。本地分类为 46 个祖先、89 个非 merge 补丁等价、30 个不同历史补丁；远端为 37/77/30（祖先计数含 main）。所有分支原名、tip、分类、删除范围及归档名见[机器清单](2026-10-07-branch-cleanup.json)。补丁等价证明的是改动曾被整合，不等于旧分支最终文件树与当前 main 相同。

30 组不同历史包含 40 个 `git cherry +` 提交，其中 20 个在目标历史有同标题替代提交；这不足以证明剩余变体应该合入。已先为全部 30 组创建并推送 `archive/2026-10-07/<原分支名>` annotated tags，分别回读 tag 对象及 peeled commit SHA 后才删除旧分支。没有把无法确认的旧实现直接合入 main。

远端删除使用 atomic 和每个 ref 的精确旧 tip 条件；本地使用事务 compare-and-swap，main 被明确排除。独立复核重跑全部 164 个不同 tip 的祖先/补丁/merge 检查，确认清单与归档对应。失败前提不会降级为无条件删除。最终回读确认仅 main、30 个归档均可恢复，main 跟踪配置保留，旧分支跟踪配置已清理。

原工作区 14 项未提交文件在合并、切换及删除后逐一 SHA256 一致，tracked/index patch 保持，未混入提交。此次 PLAN/CHECKPOINT 新记录仅按前缀单独提交，其旧修改继续留在工作区。另一干净 worktree `/Users/wans/.codex/worktrees/fd34/aiops` 仅 detach 到原 `20ffef7dc93afc61310987c1b5dcdccf092ffe7a`；目录、原提交及索引保持，未删除工作文件。

## 恢复与历史

私有本地备份在 `.local/branch-cleanup-20261007/before-cleanup.bundle`，72,398,121 bytes，SHA256 `024397a34133f05e8fffdeccd369b884e0ec9d5f995addcc0c72db8ac3bd4b2f`。bundle 覆盖全部 309 个原始本地/远端 branch refs，验证无外部 prerequisite，PACK checksum 正确。备份含本地历史，仅存于本地，不提交或上传。该目录另保留完整 refs、分类、原 dirty 指纹、执行日志及最终回读。

恢复某条差异历史时，可以先 `git fetch origin --tags`，再按清单的归档标签创建工作分支，例如：

```sh
git switch -c codex/review-old-events archive/2026-10-07/codex/wan-118-v3events-revocation
```

若需要已等价分支的原始提交图，可从本地 bundle 恢复清单中的指定 ref。当前工作区仍有未提交文件，恢复前使用独立 worktree 或先妥善保存它们。归档只是可恢复历史，仍需比较与评审后再决定是否采用变体。
