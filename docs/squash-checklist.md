# Squash 合并检查清单

> 给**点合并按钮的那个人**。第一周建议只由一人操作，练熟再放开。
> 依据：《要求.md》§5「合并采用按角色 Squash」。

---

## 0. 先说清楚为什么是 Squash

一个分支上可能有十几个「改拼写」「再试一次」「fix ci」的提交。直接合进 main，历史会变成一锅粥，
半年后没人能从 `git log` 看出某个功能是哪次提交做的。

Squash 把这些提交压成**一个**，main 上每个 commit 对应一个完整的、可回滚的改动。
代价是丢掉了分支内的细粒度历史——但对这个项目来说不值钱，三天就合并的分支没什么考古价值。

**不要用另外两个按钮：**

| 按钮 | 为什么不用 |
|---|---|
| Create a merge commit | 会产生一个 "Merge pull request #12" 的空提交，历史里全是分叉 |
| Rebase and merge | 保留分支内每个提交，且重写 SHA。三人零经验，一旦出问题没人能救 |

---

## 1. 合并前：五项检查

按顺序看，任何一项不过就停下。

### 1.1 CI 绿了吗

PR 页面底部 `All checks have passed`。

红色或者黄色转圈都不要点。也不要点 `Merge without waiting for requirements to be met`——
那个选项是给紧急情况准备的，这个项目没有紧急情况。

### 1.2 谁批的

| 改了什么 | 需要几个 Approve |
|---|---|
| 只在自己模块内 | 1 个，且**不能是作者自己** |
| 动了 `pkg/contract/` | 2 个（A 和 B 各一） |

GitHub 不允许自己批自己的 PR，所以「至少 1 名未参与该模块的成员 Approve」是自动满足的。

### 1.3 有冲突吗

PR 页面显示 `This branch has no conflicts with the base branch`。

显示 `This branch has conflicts` 时，让作者去解决，**不要在网页上点 "Resolve conflicts" 硬合**。
网页解决冲突会生成一个 merge commit，虽然 Squash 时会被压掉，但作者本地的分支就和远端不一致了，
下次 push 会莫名其妙失败。

### 1.4 PR 描述填完了吗

不是空模板。至少要有「改了什么」和「怎么验证的」。
模板没填就合并，等于放弃了唯一的变更记录。

### 1.5 分支是最新的吗

PR 页面显示 `This branch is up to date with main` 或者 `behind main by N commits`。

落后几提交没关系，GitHub 能自动合并就直接合。**落后很多**（比如十几提交）时让作者先 rebase，
否则合并进去的东西可能和 main 现在的状态冲突。

---

## 2. 点按钮

1. 点绿色的 **Squash and merge** 按钮右侧的下拉箭头，确认选的是 `Squash and merge`
2. 弹出框里有两个输入框，**两个都要改**：

**标题框**（必改）：

GitHub 默认填的是 `feat: 调度器增加滞回 (#12)`，把 ` (#12)` 删掉。

格式：`<类型>(<模块>): <一句话>`

```text
feat(scheduler): 敏感流量增加 0.8 滞回
fix(socks5): UDP 会话关闭时连接数不减
docs(status-api): 补充前端解析注意事项
test(metrics): 补充计数器重置的边界
chore(ci): 增加 Windows 交叉编译检查
```

**正文框**（建议改）：

默认是所有提交信息的堆叠。手动改成：

```text
原实现只要检测到更优 IP 就切换，导致会议期间 IP 反复横跳。
引入滞回后，当前 IP 健康分不低于最优者 80% 时不切换。

Refs: B3
```

`Refs:` 后面填看板上的任务编号。

3. 点 **Confirm squash and merge**

---

## 3. 合并后：立刻做三件事

**顺序不能反。** 漏掉第 2 步是后面所有混乱的根源。

### 3.1 删远端分支

PR 合并后页面会出现 **Delete branch** 按钮，点它。

### 3.2 通知作者更新本地

作者必须执行：

```bash
git switch main
git pull
git branch -d feat/xxx        # 删本地分支
```

`-d` 而不是 `-D`：如果 Git 提示"分支未合并"，说明有提交没进去，这时候该查而不是强删。

### 3.3 不要在这个分支上继续开发

**这是零经验团队最高频的错误。**

Squash 之后，远端那个分支的提交和 main 上的新提交**内容相同但 SHA 不同**。
如果作者继续在旧分支上改东西再 push：

- 轻则 PR 显示一堆"已经合并过的改动"
- 重则产生冲突，而且冲突的两边是同一份代码
- 最坏情况是有人 `git push --force` 把已合并的内容冲掉

**合并即报废。新任务开新分支。**

---

## 4. 三个出错了的场景

### 4.1 合并后 main 的 CI 挂了

大概率是两个人分别通过 CI、合到一起才出问题（语义冲突）。

**正确处理：**

```bash
# 先本地复现
git switch main && git pull && go test ./...
```

修好后开一个新分支，PR 标题写 `fix: 修复 #12 合并后的 xxx`，正常走流程。

**不要** `git reset --hard` 或 force push main——分支保护会挡住，而且这是破坏性操作。

### 4.2 合完发现合错东西了（比如带进去了密码）

**优先级最高，立刻处理。** 密码已经进了 main，删掉文件不够，历史里还在。

1. 立刻改掉那个密码（改密码比删历史重要）
2. 通知所有人暂停操作
3. 按 GitHub 文档用 `git filter-repo` 清历史，或者最坏情况重建仓库

这一条写在这里是希望它永远用不上。**提 PR 前 `git status` 逐个数文件，能挡掉九成。**

### 4.3 作者说"我本地和远端的 commit 对不上了"

Squash 之后必然对不上，这是正常的。

```bash
git fetch origin
git switch main && git pull
git branch -D feat/xxx        # 本地旧分支直接丢掉，内容已经在 main 里了
```

前提是确认分支上的东西确实都进了 main（看 PR 页面）。

---

## 5. 一页速查

```text
□ CI 全绿（不点 Merge without waiting）
□ Approve 够（普通 1 个 / contract 2 个）
□ 无冲突
□ PR 描述非空
□ ────────────────────────
□ Squash and merge
□ 标题去掉 (#12)，改成 <类型>(<模块>): <一句话>
□ 正文写清动机，带 Refs: <任务编号>
□ Confirm
□ ────────────────────────
□ Delete branch（远端）
□ 作者：switch main → pull → branch -d
□ 提醒作者：这个分支报废了，新任务开新分支
```
