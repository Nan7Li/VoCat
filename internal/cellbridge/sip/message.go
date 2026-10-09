// Adapted from CellBridge gateway/internal/sip.
// Upstream commit 2ee9d6b05d8a4391f782e6ce8a6d23ef57015fda
// Copyright (c) 2026 CellBridge contributors
// License: MIT (LICENSES/cellbridge-MIT.txt)
//
// Local changes: the parser keeps Via order, dialog tags, and Digest
// parameters. Responses are built from the original request so a
// retransmission can be answered from cache.

package sip

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

type header struct {
	name  string
	value string
}

type sipMessage struct {
	isResponse bool
	method     string
	requestURI string
	status     int
	reason     string

	headers []header
	values  map[string][]string
	body    string

	callID     string
	cseqNum    int
	cseqMethod string
	from       string
	fromUser   string
	fromTag    string
	to         string
	toUser     string
	toTag      string
	vias       []string
	branch     string
	contact    string
	expires    int
	expiresSet bool
}

var compactHeaders = map[string]string{
	"i": "call-id",
	"m": "contact",
	"e": "content-encoding",
	"l": "content-length",
	"c": "content-type",
	"f": "from",
	"s": "subject",
	"k": "supported",
	"t": "to",
	"v": "via",
}

func parseMessage(packet []byte) (*sipMessage, error) {
	if len(packet) == 0 {
		return nil, errBadMessage
	}
	raw := string(packet)
	head, body := splitHeadBody(raw)
	lines := unfold(head)
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return nil, errBadMessage
	}
	msg := &sipMessage{values: make(map[string][]string), body: body}
	if err := parseStartLine(strings.TrimSpace(lines[0]), msg); err != nil {
		return nil, err
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, errBadMessage
		}
		name = canonicalHeader(name)
		value = strings.TrimSpace(value)
		if name == "" || strings.ContainsAny(value, "\r\n") {
			return nil, errBadMessage
		}
		msg.headers = append(msg.headers, header{name: name, value: value})
		msg.values[name] = append(msg.values[name], value)
		if len(msg.headers) > 64 {
			return nil, errBadMessage
		}
	}
	if rawLen, ok := msg.first("content-length"); ok {
		n, err := strconv.Atoi(strings.TrimSpace(rawLen))
		if err != nil || n < 0 || n > len(msg.body) {
			return nil, errBadMessage
		}
		msg.body = msg.body[:n]
	}
	msg.callID = firstValue(msg, "call-id")
	if cseq := firstValue(msg, "cseq"); cseq != "" {
		fields := strings.Fields(cseq)
		if len(fields) != 2 {
			return nil, errBadMessage
		}
		n, err := strconv.Atoi(fields[0])
		if err != nil || n < 0 {
			return nil, errBadMessage
		}
		msg.cseqNum = n
		msg.cseqMethod = strings.ToUpper(fields[1])
	}
	msg.from = firstValue(msg, "from")
	msg.fromUser, msg.fromTag = parseNameAddr(msg.from)
	msg.to = firstValue(msg, "to")
	msg.toUser, msg.toTag = parseNameAddr(msg.to)
	msg.vias = append([]string(nil), msg.values["via"]...)
	if len(msg.vias) > 0 {
		msg.branch = headerParam(msg.vias[0], "branch")
	}
	msg.contact = firstValue(msg, "contact")
	if exp := firstValue(msg, "expires"); exp != "" {
		n, err := strconv.Atoi(strings.TrimSpace(exp))
		if err != nil {
			return nil, errBadMessage
		}
		msg.expires = n
		msg.expiresSet = true
	}
	if contactExp, ok := contactExpires(msg.contact); ok {
		msg.expires = contactExp
		msg.expiresSet = true
	}
	return msg, nil
}

func splitHeadBody(raw string) (string, string) {
	if i := strings.Index(raw, "\r\n\r\n"); i >= 0 {
		return raw[:i], raw[i+4:]
	}
	if i := strings.Index(raw, "\n\n"); i >= 0 {
		return raw[:i], raw[i+2:]
	}
	return raw, ""
}

func unfold(head string) []string {
	rawLines := strings.Split(strings.ReplaceAll(head, "\r\n", "\n"), "\n")
	var lines []string
	for _, line := range rawLines {
		if len(lines) > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			lines[len(lines)-1] += " " + strings.TrimSpace(line)
			continue
		}
		lines = append(lines, strings.TrimRight(line, "\r"))
	}
	return lines
}

func parseStartLine(line string, msg *sipMessage) error {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return errBadMessage
	}
	if strings.HasPrefix(strings.ToUpper(fields[0]), "SIP/") {
		code, err := strconv.Atoi(fields[1])
		if err != nil || code < 100 || code > 699 {
			return errBadMessage
		}
		msg.isResponse = true
		msg.status = code
		msg.reason = strings.Join(fields[2:], " ")
		return nil
	}
	if !strings.HasPrefix(strings.ToUpper(fields[2]), "SIP/") {
		return errBadMessage
	}
	msg.method = strings.ToUpper(fields[0])
	msg.requestURI = fields[1]
	return nil
}

func canonicalHeader(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if full, ok := compactHeaders[name]; ok {
		return full
	}
	return name
}

func (msg *sipMessage) first(name string) (string, bool) {
	values := msg.values[canonicalHeader(name)]
	if len(values) == 0 {
		return "", false
	}
	return values[0], true
}

func firstValue(msg *sipMessage, name string) string {
	value, _ := msg.first(name)
	return value
}

func parseNameAddr(value string) (user, tag string) {
	tag = headerParam(value, "tag")
	uri := strings.TrimSpace(value)
	if start := strings.Index(value, "<"); start >= 0 {
		if end := strings.Index(value[start:], ">"); end >= 0 {
			uri = value[start+1 : start+end]
		}
	} else if semi := strings.Index(uri, ";"); semi >= 0 {
		uri = uri[:semi]
	}
	return uriUser(uri), tag
}

func uriUser(uri string) string {
	uri = strings.TrimSpace(uri)
	if i := strings.Index(uri, ";"); i >= 0 {
		uri = uri[:i]
	}
	lower := strings.ToLower(uri)
	switch {
	case strings.HasPrefix(lower, "sip:"), strings.HasPrefix(lower, "sips:"), strings.HasPrefix(lower, "tel:"):
		uri = uri[strings.Index(uri, ":")+1:]
	}
	if i := strings.Index(uri, "@"); i >= 0 {
		return uri[:i]
	}
	return uri
}

func headerParam(value, name string) string {
	key := ";" + strings.ToLower(name) + "="
	lower := strings.ToLower(value)
	at := strings.Index(lower, key)
	if at < 0 {
		return ""
	}
	rest := value[at+len(key):]
	if strings.HasPrefix(rest, "\"") {
		rest = rest[1:]
		if end := strings.Index(rest, "\""); end >= 0 {
			return rest[:end]
		}
		return ""
	}
	if end := strings.IndexAny(rest, ";, >"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

func dialogURI(value string) string {
	uri := strings.TrimSpace(value)
	if start := strings.Index(uri, "<"); start >= 0 {
		end := strings.Index(uri[start:], ">")
		if end < 0 {
			return ""
		}
		uri = uri[start+1 : start+end]
	} else if semi := strings.Index(uri, ";"); semi >= 0 {
		uri = uri[:semi]
	}
	uri = strings.TrimSpace(uri)
	if uri == "" || strings.ContainsAny(uri, " \t\r\n<>\"") {
		return ""
	}
	lower := strings.ToLower(uri)
	if !strings.HasPrefix(lower, "sip:") && !strings.HasPrefix(lower, "sips:") {
		return ""
	}
	return uri
}

func contactExpires(contact string) (int, bool) {
	if contact == "" {
		return 0, false
	}
	raw := headerParam(contact, "expires")
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return n, true
}

func ensureTag(value, tag string) string {
	if tag == "" || headerParam(value, "tag") != "" {
		return value
	}
	return strings.TrimSpace(value) + ";tag=" + tag
}

func buildResponse(msg *sipMessage, code int, reason, toTag string, extra []string, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "SIP/2.0 %d %s\r\n", code, reason)
	if len(msg.vias) == 0 {
		b.WriteString("Via: SIP/2.0/UDP 0.0.0.0;branch=z9hG4bK0\r\n")
	}
	for _, via := range msg.vias {
		fmt.Fprintf(&b, "Via: %s\r\n", via)
	}
	if msg.from != "" {
		fmt.Fprintf(&b, "From: %s\r\n", msg.from)
	}
	if msg.to != "" {
		fmt.Fprintf(&b, "To: %s\r\n", ensureTag(msg.to, toTag))
	}
	if msg.callID != "" {
		fmt.Fprintf(&b, "Call-ID: %s\r\n", msg.callID)
	}
	if cseq := firstValue(msg, "cseq"); cseq != "" {
		fmt.Fprintf(&b, "CSeq: %s\r\n", cseq)
	}
	for _, line := range extra {
		if line != "" {
			fmt.Fprintf(&b, "%s\r\n", line)
		}
	}
	fmt.Fprintf(&b, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return []byte(b.String())
}

// txKey identifies one SIP server transaction without string concatenation.
// Remote, Call-ID, branch, CSeq, method, and request-URI are separate fields
// so a value inside one field cannot alias another field.
type txKey struct {
	ip     string
	zone   string
	port   int
	callID string
	branch string
	cseq   int
	method string
	uri    string
}

func makeTxKey(msg *sipMessage, remote *net.UDPAddr) (txKey, bool) {
	if msg == nil || remote == nil || msg.branch == "" || msg.callID == "" || msg.cseqNum < 0 {
		return txKey{}, false
	}
	method := msg.method
	if method == "" {
		method = msg.cseqMethod
	}
	method = strings.ToUpper(method)
	if method == "" {
		return txKey{}, false
	}
	ip := ""
	if parsed := normalizeIP(remote.IP); parsed != nil {
		ip = parsed.String()
	}
	return txKey{
		ip:     ip,
		zone:   remote.Zone,
		port:   remote.Port,
		callID: msg.callID,
		branch: msg.branch,
		cseq:   msg.cseqNum,
		method: method,
		uri:    msg.requestURI,
	}, true
}
