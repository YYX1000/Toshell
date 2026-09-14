# 使用声明 / Usage Disclaimer

> 本文件是 ToShell 的**使用范围声明**，与 [LICENSE](LICENSE)（MIT）配套阅读。
> This file states the **permitted scope of use** for ToShell and should be read together with
> the [MIT License](LICENSE).

## 中文

ToShell 是一款面向**授权安全测试**的命令与控制（C2）框架，用途包括：

- 你拥有或已获得**书面授权**的目标系统的渗透测试、红队演练与对抗验证；
- 自建实验环境（靶场、虚拟机、隔离网络）中的技术研究与教学演示；
- 安全防护方在自有环境中验证检测与阻断能力。

**禁止**用于：任何未授权的入侵、攻击、横向移动、数据窃取、破坏、勒索或骚扰行为；任何
违反所在地法律法规的用途；以及任何未取得授权的第三方系统。

使用者须**自行确保授权充分且可举证**，并对自己的行为及后果承担全部责任。项目作者与贡献者
不对因使用本软件产生的任何直接或间接损失负责。

发布包内捆绑的第三方组件（UPX、第三方签名驱动等）**不受 MIT 许可覆盖**，各自遵循原许可，
详见 [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md)。其中内置的 BYOVD 驱动存在已知设计
缺陷（暴露无鉴权进程终止 IOCTL），**只应在你有授权的测试环境中加载**，用完请立即卸载。

## English

ToShell is a command-and-control (C2) framework intended **solely for authorized security testing**:

- penetration testing, red-team exercises and defensive validation against systems you own or have
  **explicit written authorization** to test;
- research and teaching in your own lab environment (ranges, VMs, isolated networks);
- detection and blocking validation performed by defenders in their own environment.

**Prohibited**: any unauthorized intrusion, attack, lateral movement, data theft, damage, extortion
or harassment; any use that violates applicable laws; any use against third-party systems without
authorization.

You are responsible for **ensuring and being able to demonstrate** that you hold sufficient
authorization, and you bear full responsibility for your actions and their consequences. The authors
and contributors accept no liability for any direct or indirect damage arising from the use of this
software.

Third-party components bundled in the release packages (UPX, signed third-party drivers, etc.) are
**not covered by the MIT license** and remain under their own terms — see
[THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md). The bundled BYOVD driver has a known design flaw
(it exposes an unauthenticated process-termination IOCTL): **load it only in environments you are
authorized to test**, and unload it immediately afterwards.
