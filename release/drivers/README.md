# 内置 BYOVD 驱动（随服务端发布的副本）

本目录下的 kgameprotect.sys 与服务端二进制内嵌的驱动是同一份文件（服务端启动后也可用
`GET /api/v1/drivers/{name}/raw` 取回），放一份在这里是为了方便操作员**在加载前离线复核**：

| 项 | 值 |
|---|---|
| 文件 | kgameprotect.sys |
| 大小 | 59,592 字节 |
| SHA-256 | 6c1d596d18213e24f0c88d58ea7f3ca24114eded806b6198a8abc701251126ee |
| 签名 | Authenticode Valid —— CN=Microsoft Windows Hardware Compatibility Publisher（WHQL 认证签名），PE32+ / AMD64 |
| 设备 | \\.\kgameprotect |
| 用途 | 无鉴权**进程终止** IOCTL 0x222048（METHOD_BUFFERED + FILE_ANY_ACCESS，入参首个 DWORD = PID） |
| 来源 | LOLDrivers PR #428（Add vulnerable kgameprotect.sys process-termination driver） |

复核命令：

`powershell
Get-FileHash .\kgameprotect.sys -Algorithm SHA256
Get-AuthenticodeSignature .\kgameprotect.sys | Select-Object Status, SignerCertificate
# 或： signtool verify /pa /all .\kgameprotect.sys
`

⚠️ 仅限**授权**红队演练 / 渗透测试环境使用。该驱动只提供进程终止能力（**不具备任意内核读写**，
因此无法用于清除 PPL 保护进程）；对 PPL 保护进程请用控制台的「PPL 击杀」（句柄窃取路线）。
加载后请及时在「杀软对抗」页点「卸载驱动」清理内核服务与文件。

注：RTCore64.sys（MSI Afterburner，CVE-2019-16098）自 v1.3.3 起**不再内置**——它被微软
易受攻击驱动黑名单与绝大多数杀软重点标记；本仓库 v1.3.3 首发 zip 中曾残留该文件的副本，已移除。
