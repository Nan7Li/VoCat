# Attribution

Halo 1.1.6 is a personal interface and release. It is **based on VoCat**.

- Upstream project: [MengMengCode/VoCat](https://github.com/MengMengCode/VoCat)
- Synced through: VoCat v0.2.12 (`ee22576`)
- Copyright: Copyright (c) 2026 Vocat Project Authors
- License: [Vocat Research & Evaluation License](LICENSE)

Halo does not replace VoCat and is not an official Vocat product. The Vocat name and marks stay with the original authors.

## What this branch changes

- Halo name and icon (so this build is not presented as official VoCat)
- A different web UI and a user-selectable accent color
- Local preview helpers
- Bugfix for Vodafone UK MT SMS, sent upstream as [VoCat#39](https://github.com/MengMengCode/VoCat/pull/39)
- Halo phone page, call history/recording, ePDG probe UI, radio triad, diagnostics pack

Everything else — modem control, IMS / WiFi Calling, SMS, eSIM, proxy, notifications — comes from VoCat. Halo 1.1.6 includes VoCat v0.2.12 features such as ePDG COOKIE retry, GSMA RSP2 ES9+ trust, incoming-call notifications, IMS USSD, 8-bit SMS PDU, DITO 51566, EG25-GL_D discovery, and the 14-day uptime card.
