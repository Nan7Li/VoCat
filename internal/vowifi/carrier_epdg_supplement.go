package vowifi

// supplementalEPDGHostnames covers home networks whose 3GPP default ePDG name
// (epdg.epc.mncXXX.mccYYY.pub.3gppnetwork.org) does not resolve or resolves
// only to a loopback placeholder, and whose Apple carrier bundle carries no
// ePDG address. Hostnames come from Samsung's epdg_apns_conf.xml as published
// by sbaresearch/vowifi-epdg-scanning; each was checked against public DNS in
// October 2026 (default name failing, supplemental name resolving publicly).
// A bundle-supplied hostname always takes precedence over this table.
var supplementalEPDGHostnames = map[string]string{
	"311480": "wo.vzwwo.com",           // Verizon (default name is NXDOMAIN)
	"455003": "wlan.three.com.mo",      // 3 Macau (default name is 127.0.0.1)
	"515002": "weconnect.globe.com.ph", // Globe (default name is NXDOMAIN)
}

// homeEPDGHostname returns the ePDG for a home PLMN when no carrier profile
// supplies one: a verified supplemental hostname, else the 3GPP default.
func homeEPDGHostname(mcc, mnc string) string {
	if hostname := supplementalEPDGHostnames[canonicalPLMN(mcc, mnc)]; hostname != "" {
		return hostname
	}
	return standardEPDGHostname(mcc, mnc)
}
