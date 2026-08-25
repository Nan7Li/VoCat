export type ApiStatus = "ok" | "error";

export type DeviceType = "wifi_410" | "dji_4g" | "pcie_ec20_ec25" | "usb_sim_reader";

export interface Session {
  authenticated: boolean;
  username: string;
  role: string;
  expiresAt?: string;
  csrfToken?: string;
}

export interface LoginResponse {
  status: ApiStatus;
  username?: string;
  role?: string;
  expiresAt?: string;
  csrfToken?: string;
}

export interface ApiErrorBody {
  status?: string;
  code?: string;
  error?: string;
  message?: string;
  requestId?: string;
  warning?: string;
  busy?: boolean;
}

export interface VoWiFiCall {
  id: string;
  number?: string;
  direction: string;
  state: string;
  startedAt?: string;
  answeredAt?: string;
  mediaReady?: boolean;
  codec?: string;
  endedAt?: string;
  recording?: string;
}

// One persisted row of the phone page's call history.
export interface CallRecord {
  id: number;
  deviceId: string;
  number: string;
  direction: "incoming" | "outgoing";
  state: "active" | "answered" | "missed" | "failed" | "ended";
  startedAt?: string;
  answeredAt?: string | null;
  endedAt?: string | null;
  durationSeconds: number;
  transport: "vowifi" | "cellular";
  recording?: string;
}

// Last ePDG UDP/500+4500 health-check outcome for a device.
export interface EPDGProbeStatus {
  deviceId: string;
  iccid?: string;
  epdg?: string;
  port500Ok: boolean;
  port4500Ok: boolean;
  rtt500Ms?: number;
  rtt4500Ms?: number;
  error?: string;
  checkedAt?: string;
  lastSuccessAt?: string | null;
  lastFailureAt?: string | null;
  disabledVoWiFi: boolean;
}

export interface CallRecordsResponse {
  records: CallRecord[];
}

export interface VoWiFiRuntime {
  deviceId: string;
  phase: string;
  enabled?: boolean;
  active?: boolean;
  carrierProfile?: string;
  carrierProfileFrom?: string;
  dataplaneMode: string;
  iccid: string;
  imsi: string;
  simReady: boolean;
  accessReady: boolean;
  tunnelReady: boolean;
  imsReady: boolean;
  smsReady: boolean;
  regStatus: number;
  regStatusText: string;
  networkMode: string;
  localPhone?: string;
  phoneNumberSource?: string;
  lastErrorClass: string;
  lastError: string;
  lastReason: string;
  updatedAt: string;
  tunnel?: Record<string, unknown>;
  imscore?: Record<string, unknown>;
  smsip?: Record<string, unknown>;
}

export interface ModemSummary {
  operator: string;
  nativeMcc: string;
  nativeMnc: string;
  operatorCountryCode?: string;
  nativeSpn?: string;
  cardMcc?: string;
  cardMnc?: string;
  cardCountry?: string;
  homeCarrierName?: string;
  homeCarrierPlmn?: string;
  homeCarrierCountryCode?: string;
  serviceBlocked?: boolean;
  blockedReason?: string;
  networkMode: string;
  networkDuplex?: string;
  radioBand: string;
  radioChannel: number;
  signalDbm: number;
  signalRsrp?: number;
  signalRsrq?: number;
  signalSinr: number;
  imei: string;
  iccid: string;
  imsi?: string;
  firmware?: string;
  model?: string;
  regStatus: number;
  regStatusText?: string;
  psAttached?: boolean;
  simInserted?: boolean;
  operatingMode?: number;
  phoneNumber?: string;
  phoneNumberSource?: string;
}

export interface PublicIPInfo {
  detected?: boolean;
  ip: string;
  countryCode: string;
  region?: string;
  city?: string;
  organization?: string;
}

export interface DeviceListItem {
  id: string;
  name: string;
  deviceType: DeviceType;
  running: boolean;
  healthy: boolean;
  controlOnline: boolean;
  physicalPresent?: boolean;
  workerRunning?: boolean;
  dataConnected?: boolean;
  radioRegistered?: boolean;
  lifecyclePhase?: string;
  lifecycleReason?: string;
  publicIp: string;
  privateIp?: string;
  interface: string;
  esimTransport: string;
  smsEnabled: boolean;
	  networkEnabled: boolean;
  vowifiEnabled: boolean;
  vowifiActive?: boolean;
  radioMode?: "cellular" | "airplane" | "vowifi" | "transition" | "offline";
  vowifiRuntime: VoWiFiRuntime;
  modem: ModemSummary;
  networkConnected: boolean;
	  networkPhase?: "unknown" | "starting" | "connected" | "stopping" | "recovering" | "disabled" | "failed";
	  networkError?: string;
	  modemPhase?: "rebooting" | "";
	  publicIpInfo?: PublicIPInfo;
  registrationStateLabel: "registered" | "searching" | "denied" | "unknown";
  flightMode?: boolean;
  epdgProbe?: EPDGProbeStatus;
}

export interface DevicesResponse {
  deviceLimit: number;
  devices: DeviceListItem[];
}

export interface DashboardDevice {
  id: string;
  name: string;
  deviceType: DeviceType;
  interface: string;
  proxyPort: number;
  publicIp: string;
  healthy: boolean;
  operator: string;
  signalDbm: number;
  networkMode: string;
  networkDuplex?: string;
  vowifiActive: boolean;
  vowifiRuntime: VoWiFiRuntime;
  networkConnected: boolean;
  model?: string;
}

export interface DashboardHostInfo {
  cpuModel: string;
  boardModel: string;
  memoryModel: string;
  diskModel: string;
}

export interface DashboardHostPerf {
  cpuPercent: number;
  memoryPercent: number;
  memoryUsedBytes: number;
  memoryTotalBytes: number;
  diskPercent: number;
  diskUsedBytes: number;
  diskTotalBytes: number;
  netRxBps: number;
  netTxBps: number;
}

export interface DashboardHost {
  host: DashboardHostInfo;
  perf: DashboardHostPerf;
}

// 仪表盘定时任务卡只关心名字与下次执行时间。
export interface DashboardUpcomingTask {
  id: number;
  name: string;
  enabled: boolean;
  nextRunAt: string;
}

export interface DeviceOverview extends DeviceListItem {
  atPort?: string;
  audioDevice?: string;
  backendMode?: string;
  controlDevice?: string;
  radioLiveOk?: boolean | null;
  traffic?: Record<string, string>;
  trafficRaw?: Record<string, number>;
  trafficMeta?: Record<string, unknown>;
}

export interface DeviceStatus {
  id: string;
  name: string;
  healthy: boolean;
  interface: string;
  publicIp: string;
  proxyPort: number;
  lastHardwareRefresh?: string;
  networkConnected: boolean;
  modem: ModemSummary;
  vowifi?: Record<string, unknown>;
  simServiceTable?: Record<string, unknown>;
  pnn?: Array<Record<string, unknown>>;
  opl?: Array<Record<string, unknown>>;
}

export interface DiscoveredDevice {
	 hardwareKind?: string;
	 readerName?: string;
  deviceType?: DeviceType;
  discoveryKey: string;
  controlPath: string;
  netInterface: string;
  usbPath: string;
  vendorId: number;
  productId: number;
  driverName: string;
  atPorts: string[];
  atPort: string;
  audioDevice?: string;
  imei?: string;
  mode: string;
  networkCapable: boolean;
  configured: boolean;
  configuredId?: string;
  degraded?: boolean;
  discoveryIssue?: "pcsc_service_unavailable" | "pcsc_driver_missing" | string;
  usbnetMode?: number | null;
}

export interface DeviceConfig {
  id: string;
  name: string;
  deviceType: DeviceType;
  interface: string;
  controlDevice: string;
  atPort: string;
  usbPath: string;
  audioDevice?: string;
  modemImei?: string;
	 simPin?: string;
  apn: string;
  proxyPort: number;
  baudRate: number;
  dataBits: number;
  stopBits: number;
  parity: string;
  deviceBackend: "at" | "qmi" | "pcsc";
  esimTransport: "at" | "qmi" | "pcsc" | "none";
  qmiUseProxy: boolean;
  qmiProxyPath?: string;
  qmiProxyExecutable?: string;
  smsEnabled: boolean;
  vowifiEnabled: boolean;
}

export interface USBNetMode {
  mode: number;
  name: string;
  rebootRequired?: boolean;
}

export interface OperatorSelection {
  mode: number;
  format: number;
  operator: string;
  accessTechnology?: string;
}

export interface CardPolicy {
  iccid: string;
  vowifiEnabled: boolean;
  airplaneEnabled: boolean;
  apn?: string;
  ipVersion?: string;
  customPhoneNumber?: string;
  source?: string;
  createdAt?: string;
  updatedAt?: string;
}

export interface SMSContact {
  deviceId: string;
  deviceName?: string;
  modemImei?: string;
  imsi: string;
  localPhone?: string;
  peer: string;
  displayName?: string;
  lastMessage?: string;
  lastContent?: string;
  lastTimestamp: string;
  direction?: string;
  lastType?: string;
  lastSmsId?: number;
  unreadCount: number;
  messageCount?: number;
}

export interface SMSMessage {
  id: number;
  messageId?: string;
  deviceId: string;
  modemImei?: string;
  imsi: string;
  peer: string;
  direction: "inbound" | "outbound" | "received" | "sent";
  body?: string;
  content?: string;
  sender?: string;
  recipient?: string;
  localPhone?: string;
  deviceName?: string;
  type?: string;
  timestamp: string;
  status: string;
  source?: string;
  deliveryState?: string;
}

export interface UpstreamProxy {
  id: string;
  name: string;
  addr: string;
  username: string;
  password?: string;
  enabled: boolean;
}

export interface UpstreamProxyProbe {
  reachable?: boolean;
  handshakeOk?: boolean;
  udpAssociateOk?: boolean;
  udpExchangeOk?: boolean;
  authMethod?: string;
  relayAddr?: string;
  dnsServer?: string;
  dnsName?: string;
  dnsRcode?: number;
  roundTripMs?: number;
  diagnosis?: string;
  hint?: string;
  error?: string;
}

export interface UpstreamProxySaveResponse {
  status?: string;
  proxy?: UpstreamProxy;
  probe?: UpstreamProxyProbe;
  message?: string;
}

export interface Country {
  countryCode: string;
  countryName: string;
  mccs: string[];
}

export interface CountryRule {
  countryCode: string;
  countryName?: string;
  upstreamProxyId: string;
  enabled: boolean;
}

export interface DeviceProxyBinding {
  deviceId: string;
  iccid: string;
  profileName: string;
  upstreamProxyId: string;
  reconnectRequested?: boolean;
  reconnectError?: string;
}

export interface ProfileProxyCandidate {
  deviceId: string;
  iccid: string;
  profileName: string;
  stateText?: string;
}

export interface LogEntry {
  time: string;
  level: "debug" | "info" | "warn" | "error" | string;
  message: string;
  caller?: string;
  fields?: string | Record<string, unknown>;
}

export interface EsimProfile {
  iccid: string;
  name: string;
  serviceProviderName: string;
  state: number;
  stateText: string;
  classText?: string;
}

export interface EsimEuiccProfiles {
  eid: string;
  aidHex: string;
  profiles: EsimProfile[];
}

export interface EsimOverview {
  available?: boolean;
  reason?: string;
  chipInfo?: {
    serialNumber?: string;
    skuName?: string;
    firmware?: string;
    eids?: Array<Record<string, unknown>>;
  };
  profiles: EsimEuiccProfiles[];
}

export interface NotificationSettings {
  telegram: Record<string, unknown>;
  webhook: Record<string, unknown>;
  bark: Record<string, unknown>;
  email: Record<string, unknown>;
  pushplus: Record<string, unknown>;
  wecom: Record<string, unknown>;
  lark: Record<string, unknown>;
}

// 网络访问控制策略：默认仅放行内网网段，可切换到对公网开放。
export interface SecuritySettings {
  mode: "internal" | "public";
  allowedCidrs: string[];
  trustProxyHeaders: boolean;
  clientIp: string;
  clientAllowed: boolean;
}

// 运行日志保留策略：全局硬上限 10000 条，可配置更严格的条数或天数限制。
export interface LoggingSettings {
  mode: "unlimited" | "count" | "days";
  count: number;
  days: number;
  storedLogs: number;
  maxLogs: number;
}

export interface SystemInfo {
  version: string;
  buildTime: string;
  config: string;
  os?: string;
  architecture?: string;
  uptime?: string;
  developer?: boolean;
}

export interface UpstreamVocatStatus {
  enabled: boolean;
  repository: string;
  syncedVersion: string;
  latestVersion?: string;
  available?: boolean;
  releaseNotes?: string;
  htmlUrl?: string;
  lastCheckAt?: string;
  lastError?: string;
}

export interface AutoUpdateSettings {
  enabled: boolean;
  apply: boolean;
  intervalHours: number;
  lastCheckAt?: string;
  lastAvailable?: boolean;
  lastVersion?: string;
  lastError?: string;
  repository?: string;
  isDocker?: boolean;
}

export interface WireGuardTunnel {
  id: string;
  name: string;
  interface: string;
  config: string;
  autostart: boolean;
  available: boolean;
  running: boolean;
  publicKey?: string;
  listenPort?: number;
  peers?: number;
  transferRx?: number;
  transferTx?: number;
  endpoint?: string;
  latestHandshake?: string;
  error?: string;
  external?: boolean;
  createdAt?: string;
  updatedAt?: string;
}

export interface WireGuardSnapshot {
  available: boolean;
  hint?: string;
  tunnels: WireGuardTunnel[];
}

export interface HTTPSSettings {
  enabled: boolean;
  httpUrl: string;
  httpsUrl: string;
  fingerprint?: string;
  notAfter?: string;
}

export interface DeveloperSettings {
  deviceLimit: number;
  defaultDeviceLimit: number;
  maxDeviceLimit: number;
  smsHourlyLimit: number;
  defaultSmsHourlyLimit: number;
  maxSmsHourlyLimit: number;
}

export type Notice = {
  kind: "success" | "error" | "warning" | "info";
  title: string;
  detail?: string;
} | null;
