package qdc507

import (
	"context"
	"fmt"
	"strings"
)

func verifyCardZero(ctx context.Context, runner Runner, cfg Config, transport string) error {
	cards, err := runADB(ctx, runner, cfg, "-t", transport, "shell", "cat /proc/asound/cards")
	if err != nil {
		return fmt.Errorf("QDC507 声卡识别失败: %w", err)
	}
	for _, line := range strings.Split(cards, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == "0" && strings.Contains(line, expectedCardName) {
			return nil
		}
	}
	return fmt.Errorf("QDC507 card0 不匹配: 需要 %s", expectedCardName)
}
