# Attribution

Halo 1.1.14 is a personal interface and release. It is **based on VoCat**.

- Upstream project: [MengMengCode/VoCat](https://github.com/MengMengCode/VoCat)
- Synced through: VoCat v0.3.15 (`41b6ac6`)
- Copyright: Copyright (c) 2026 Vocat Project Authors
- License: [Vocat Research & Evaluation License](LICENSE)

Halo does not replace VoCat and is not an official Vocat product. The Vocat name and marks stay with the original authors.

## What this branch changes

- Halo name and icon (so this build is not presented as official VoCat)
- A different web UI and a user-selectable accent color
- Local preview helpers
- Bugfix for Vodafone UK MT SMS, sent upstream as [VoCat#39](https://github.com/MengMengCode/VoCat/pull/39)
- Halo phone page, call history/recording, ePDG probe UI, radio triad, diagnostics pack

The modem control, IMS / WiFi Calling, SMS, eSIM, proxy and notification foundations come from VoCat; this branch additionally integrates the optional CellBridge call bridge described below. Halo 1.1.14 includes VoCat v0.3.15 (`41b6ac6`).

## CellBridge

The optional call bridge adapts CellBridge under the MIT license. It does not replace the Vocat Research & Evaluation License that covers Halo and VoCat.

- Upstream project: [mccding/CellBridge](https://github.com/mccding/CellBridge)
- Upstream commit: `2ee9d6b05d8a4391f782e6ce8a6d23ef57015fda`
- Copyright: Copyright (c) 2026 CellBridge contributors
- License text: [LICENSES/cellbridge-MIT.txt](LICENSES/cellbridge-MIT.txt)
- Adapted paths: `internal/cellbridge/cellular`, `internal/cellbridge/sip`, `internal/cellbridge/qdc507`, `internal/cellbridge/voice`
- The QDC507 vendor voice payload is not part of this MIT copy. Deployments supply that file separately and check its hash.
- Deployment notes: [docs/CellBridge.zh-CN.md](docs/CellBridge.zh-CN.md)
