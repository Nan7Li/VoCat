package pcsc

import (
	"context"
	"errors"
)

// transmitContinued implements short ISO 7816 response continuation. Raw eUICC
// sessions bypass this helper so their logical-channel code owns GET RESPONSE.
func transmitContinued(ctx context.Context, command []byte, raw func(context.Context, []byte) ([]byte, uint16, error)) ([]byte, uint16, error) {
	if len(command) < 4 {
		return nil, 0, errors.New("pcsc: APDU command is too short")
	}
	current := append([]byte(nil), command...)
	var collected []byte
	for step := 0; step <= 8; step++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		data, status, err := raw(ctx, current)
		if err != nil {
			return nil, 0, err
		}
		sw1, sw2 := byte(status>>8), byte(status)
		switch sw1 {
		case 0x6C:
			// Case 2 and case 4 short APDUs contain Le. Do not overwrite
			// command data when a case 3 APDU has no Le.
			if len(current) != 5 && (len(current) < 6 || current[4] == 0 || len(current) != int(current[4])+6) {
				return nil, status, errors.New("pcsc: length correction requires a short APDU with Le")
			}
			current[len(current)-1] = sw2
		case 0x61, 0x9F:
			collected = append(collected, data...)
			// Preserve the command's class and logical-channel bits.
			current = []byte{command[0], 0xC0, 0x00, 0x00, sw2}
		default:
			return append(collected, data...), status, ctx.Err()
		}
	}
	return nil, 0, errors.New("pcsc: too many APDU continuations")
}
