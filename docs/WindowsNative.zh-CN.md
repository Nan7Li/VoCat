# Halo Windows 原生蜂窝接口预览

状态：2026-10-12 源码与构建预览，基于 `halo` 分支 `8cf158f`。真实流量、短信和通话仍待实机验收，本预览不能视为稳定版本。

## 已完成的代码

- Windows COM/SetupAPI 发现；一台明确识别的 AT 模块与一个 Windows WWAN 接口自动绑定。多模块场景不猜测对应关系。
- 通过 Windows Mobile Broadband（`netsh mbn`）控制蜂窝数据，使用 v4 XML 临时配置，包含当前 SIM、APN、IP 类型与认证参数。
- 原生 MBN 数据作为正常功能开放；关闭开发者模式和重启不会清除已保存的原生 SIM 流量策略。
- 启用数据前重新读取当前 SIM；随后保存对应策略、退出射频关闭状态、等待分组注册并拨号。VoWiFi 开启时仍拒绝启用蜂窝数据。
- 连接状态同时检查 Windows 报告的连接状态、接口是否启用及可用 IP，避免旧地址造成“已连接”误报。
- Windows WFP/Wintun、PC/SC、主机状态、接口统计、服务入口及 Win32/GDI 桌面入口移植。
- PC/SC 支持 `61`、`9F`、`6C` 响应续传，保留逻辑通道和命令数据。
- IMS 保留同一 SIP 对话的 `183` 媒体应答，以处理最终 `200` 未包含 SDP 的情况；修正受保护 UDP 短信响应的源/目标端口。
- 只宣告当前实际实现的 G.711 PCMA/PCMU 音频能力。
- Windows 服务更新器使用目标同级的临时 `.exe`，移交前只读核对服务及其可执行文件；支持 `.exe` 发布资产名称，保留旧名称兼容。
- 保留新版 Halo 的 NAS、CellBridge 与运营商配置功能；Linux 构建复用公共的隧道路由验证。

## 已验证与未验证

| 检查 | 结果与范围 |
| --- | --- |
| Windows 原生发现 | 实机只读发现 Quectel COM3 与“手机网络”WWAN 接口；未打开 AT 口或改变网络 |
| SIM 对应关系 | Windows 当前 SIM 与 Halo 保存的 VoWiFi 卡记录匹配；未发布任何卡号或身份数据 |
| 前端 | TypeScript/Vite 构建通过；19 项现有前端测试通过 |
| Go 功能测试 | `cmd/vocat`、config、developer、device、modem、pcsc、vowifi 全部子包、desktop、update、exportproxy、wireguard 通过 |
| 数据 API | 原生 MBN 无开发者模式访问、当前 SIM 策略保存、清理后策略保留、实际/期望连接状态区分、VoWiFi 互斥及 PublicIP/CellularData 专项测试通过 |
| 静态检查 | 相关 Go 包以及 server、store 的 `go vet` 通过 |
| 构建 | Windows amd64 后端与桌面构建通过；Linux amd64 交叉编译通过 |
| 完整 Windows 测试 | **未通过**：QDC507 的 Unix 权限、QMI 节点 unlink、ALSA symlink 及 store 备份 POSIX 权限断言仍需平台适配 |
| 真实业务验收 | **未完成**：原生流量、收发短信、拨号、接听、双向音频与断线恢复 |

## 使用限制

原生流量需要 Windows 已识别的 WWAN 蜂窝模块。普通 USB 智能卡读卡器只有 SIM 访问能力，不提供 Windows 蜂窝流量接口。Windows 原生蜂窝数据不会替代 Halo 的 ePDG/IMS Wi-Fi Calling。

AMR/AMR-WB 编解码尚未实现。只接受 AMR 的运营商仍可能无法通话，不能把 IMS 已注册当成语音可用。QDC507/ALSA 的 Windows 音频能力也不在本次 EC20 验证范围内。

自动更新器仅支持已安装、且路径匹配的 Windows SCM 服务；Electron/便携模式没有该服务时会明确报错。本预览没有安装服务、改动当前用户的 APN/CFUN 或完成真实更新验收。

## 预览包与源码构建

预览包包含 `halo-windows-amd64.exe`（内嵌新版网页）、`Halo-Native.exe`（Win32/GDI 入口）及签名的 `wintun.dll`。原生桌面入口完成编译和单元测试，尚未进行目视与硬件验收；已有本地 Halo 后端正在运行时，它可能连接到已有实例。

运行前应正常退出现有 Halo 并做一致性数据库备份。默认 Windows 数据库位于 `%APPDATA%\Halo\data\vocat.db`；仍在运行时只复制主 SQLite 文件可能遗漏 WAL 数据。新版数据库结构升级后，回滚需要恢复对应备份。此次上传没有替换当前安装。

源码构建：

```powershell
npm ci --prefix web
npm run build --prefix web
go build ./cmd/vocat ./cmd/vocat-desktop
```

需要输出可运行文件时，分别构建：

```powershell
go build -o halo-windows-amd64.exe ./cmd/vocat
go build -ldflags "-H=windowsgui" -o Halo-Native.exe ./cmd/vocat-desktop
```

功能测试、构建和只读发现都不能替代真实 SIM/运营商业务验收。后续需分别记录数据拨号与指定接口出站、VoWiFi 短信及通话结果，并补齐 Windows 平台测试。

协作记录：Windows 更新器由 Grok 在限定副本实际修改，经 Codex 审查合入、修正并测试；agy 的说明文件委派因 API 地区限制失败，本文由 Codex 完成。外部判断和模拟测试均不视为硬件验收。

最近一次 Jev 完整移植审查返回 `incomplete`（置信度 0.56、需要进一步审查）。本次只保存已完成的源码、测试结果和构建预览，不代表整个 Windows 移植或真实业务验收已通过。
