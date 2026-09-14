# Security Policy

## 支持版本

| 版本 | 支持 |
|---|---|
| v1.2.x | ✅ 当前稳定版 |
| v1.1.x | ⚠️ 仅安全修复 |
| < v1.1.0 | ❌ 不支持 |

## 安全声明

ToShell 是**仅供授权红队演练 / 渗透测试 / 安全研究**的自托管 C2 框架。它不是漏洞扫描产品，不提供可修复的"产品漏洞"意义上的安全补丁服务。请仅在你的授权范围内使用。

## 报告安全 / 滥用问题

如果你发现：

- **代码安全缺陷**（如凭据硬编码、鉴权绕过、注入等），或
- **滥用 / 误用 / 违规分发**（本工具被用于未授权攻击），

请通过以下渠道报告，**不要**在公开 Issue 中披露可用于攻击的细节：

| 渠道 | 地址 |
|---|---|
| 邮箱（推荐） | [qingshan@88.com](mailto:qingshan@88.com) |
| GitHub 私密报告 | [Security Advisories](https://github.com/iQingshan/Toshell/security/advisories/new)（"Report a vulnerability"） |

建议在报告中附上：影响版本、复现步骤或 PoC 要点、影响面评估、以及你希望的披露时间线。我们会在确认后同步修复计划，并在修复版本发布时致谢（如你同意署名）。

对于**授权测试中出现的功能缺陷或误报**，请直接提 [Issue](https://github.com/iQingshan/Toshell/issues)（附复现步骤、版本、系统环境）。

## 加固提示（部署）

1. 使用 `configs/server.yaml.example` 为模板，**替换所有默认凭据**（`admin_password` / `api_keys` / `jwt_key` / `listener.encryption_key` / `public_host`）。
2. 更换 `listener.encryption_key` 后必须**重新生成全部植入端**。
3. 不要提交真实的 `configs/server.yaml`、`release/configs/server.yaml`、`data/`、`*.db` 到公开仓库。
