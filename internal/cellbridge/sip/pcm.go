// Adapted from CellBridge gateway/internal/voice/pcm.go
// Upstream commit 2ee9d6b05d8a4391f782e6ce8a6d23ef57015fda
// Copyright (c) 2026 CellBridge contributors
// License: MIT (LICENSES/cellbridge-MIT.txt)
//
// Local changes: frames are not fixed at 160 samples so RTP can carry the
// payload length that arrived on the wire. Packetization still uses 20 ms.

package sip

// pcmuSegmentEndpoints are the G.711 μ-law segment boundaries used by the
// CellBridge linear PCM encoder.
var pcmuSegmentEndpoints = [8]int{0, 256, 512, 1024, 2048, 4096, 8192, 16384}

func encodePCMU(samples []int16) []byte {
	encoded := make([]byte, len(samples))
	for index, sample := range samples {
		encoded[index] = encodePCMUSample(sample)
	}
	return encoded
}

func decodePCMU(payload []byte) []int16 {
	decoded := make([]int16, len(payload))
	for index, value := range payload {
		decoded[index] = decodePCMUSample(value)
	}
	return decoded
}

func encodePCMUSample(sample int16) byte {
	sign := byte(0)
	value := int(sample)
	if value < 0 {
		sign = 0x80
		value = -value
	}
	if value > 32635 {
		value = 32635
	}
	value += 132
	segment := 0
	for segment < len(pcmuSegmentEndpoints)-1 && value >= pcmuSegmentEndpoints[segment+1] {
		segment++
	}
	mantissa := (value >> (segment + 3)) & 0x0f
	return ^(sign | byte(segment<<4) | byte(mantissa))
}

func decodePCMUSample(value byte) int16 {
	value = ^value
	sign := value & 0x80
	segment := (value >> 4) & 0x07
	mantissa := value & 0x0f
	sample := ((int(mantissa) << 3) + 132) << segment
	if sign != 0 {
		return int16(132 - sample)
	}
	return int16(sample - 132)
}
