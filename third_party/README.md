# third_party

本目录只放**不纳入版本控制**的第三方可执行文件及其校验信息。

## 当前状态：空

认证器已改为**本项目自行实现**的 `cmd/caulink-auth`（Dr.COM ePortal 协议），
不再依赖任何第三方认证二进制。

原因见 [`docs/adr/0004-drcom-auth.md`](../docs/adr/0004-drcom-auth.md)：
农大校园网是 Dr.COM 而不是深澜（Srun），原计划的 `BitSrunLoginGo` 不适用。

### 曾经评估过的：BitSrunLoginGo

- 用途（已废弃）：深澜校园网认证，早期计划以独立子进程调用。
- 许可证：AGPL-3.0。
- 结论：**不适用且已移除**。它的配置格式与 `--interface` 用法曾影响过
  `internal/auth` 的子进程契约（这部分仍保留），但不再需要它的二进制。

---

如果将来引入任何第三方二进制，按以下规则登记：

- 用途、许可证、是否修改源码；
- 版本锁定：`<name>-<version>-<os>-<arch>.sha256` 记录版本、下载地址与校验和；
- **二进制本身不入库**，只提交 `README.md` 与 `.sha256` 文件。