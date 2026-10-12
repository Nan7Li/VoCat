package pcsc

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestAPDUResponseContinuationAndLengthCorrection(t *testing.T) {
	for _, firstStatus := range []uint16{0x6119, 0x9F19} {
		card := &scriptedCard{replies: []scriptedReply{
			{data: []byte{0x62}, sw: firstStatus},
			{sw: 0x6C02},
			{data: []byte{0x02, 0x00}, sw: 0x9000},
		}}
		command := []byte{0x02, 0xA4, 0x00, 0x04, 0x02, 0x2F, 0xE2, 0x00}
		data, sw, err := transmitContinued(context.Background(), command, card.Transmit)
		if err != nil || sw != 0x9000 || !bytes.Equal(data, []byte{0x62, 0x02, 0x00}) {
			t.Fatalf("continued response: sw=%04X err=%v", sw, err)
		}
		if len(card.calls) != 3 || !bytes.Equal(card.calls[1], []byte{0x02, 0xC0, 0, 0, 0x19}) || !bytes.Equal(card.calls[2], []byte{0x02, 0xC0, 0, 0, 2}) {
			t.Fatal("GET RESPONSE or corrected Le lost its logical channel")
		}
		if command[len(command)-1] != 0 {
			t.Fatal("caller APDU was mutated")
		}
	}
}

func TestAPDUContinuationBoundAndCancellation(t *testing.T) {
	card := &scriptedCard{replies: make([]scriptedReply, 10)}
	for index := range card.replies {
		card.replies[index].sw = 0x6100
	}
	_, _, err := transmitContinued(context.Background(), []byte{0, 0xB0, 0, 0, 0}, card.Transmit)
	if err == nil || !strings.Contains(err.Error(), "too many") || len(card.calls) != 9 {
		t.Fatal("continuation loop was not bounded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	card.calls = nil
	if _, _, err := transmitContinued(ctx, []byte{0, 0xB0, 0, 0, 0}, card.Transmit); err != context.Canceled || len(card.calls) != 0 {
		t.Fatal("cancelled command accessed the card")
	}
	card.replies = []scriptedReply{{sw: 0x6C02}}
	if _, _, err := transmitContinued(context.Background(), []byte{0, 0xA4, 0, 0, 2, 0x3F, 0}, card.Transmit); err == nil {
		t.Fatal("length correction overwrote a data byte")
	}
}
