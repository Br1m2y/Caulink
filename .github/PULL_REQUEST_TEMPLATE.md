# 变更说明

<!-- 一句话说清这个 PR 做了什么 -->

## 类型

- [ ] feat：新功能
- [ ] fix：修缺陷
- [ ] refactor：重构（不改行为）
- [ ] docs：文档
- [ ] chore：构建/流程/依赖
- [ ] contract：*** 改动 pkg/contract 或 api/schema ***

## 涉及模块

- [ ] A 网络与认证（internal/auth、internal/ippool、cmd/phase0probe）
- [ ] B 代理与调度（internal/proxy、internal/scheduler、internal/health、cmd/caulinkd）
- [ ] C 策略与界面（internal/classify、internal/control、desktop）

## 自检

- [ ] 本地 `go vet ./... && go test ./... && go build ./...` 通过
- [ ] 没有跨模块反向依赖（见 `仓库骨架设计.md` §3）
- [ ] 没有改动 `pkg/contract`；如有，PR 标题已带 `contract:` 前缀且说明了 A/B/C 的影响
- [ ] 没有把密钥、校园网账号、第三方二进制提交进仓库

## 契约变更（仅 contract: PR 填写）

- 字段变更：
- 对 A 的影响：
- 对 B 的影响：
- 对 C 的影响：
- 是否需要同步 `api/schema/`：
- 是否需要数据迁移：
