//go:build windows

package pcsc

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	nativepcsc "github.com/telesma-app/pcsc"
)

// windowsBackend uses the Windows PC/SC service through winscard.dll. The
// upstream package is cgo-free, so the Halo executable remains self-contained
// and does not need a separate MSYS2/pcsc-lite installation.
type windowsBackend struct{}

func newNativeBackend() Backend { return windowsBackend{} }

func (windowsBackend) Readers(context.Context) ([]Reader, error) {
	readers := make([]Reader, 0)
	for info, err := range nativepcsc.Enumerate() {
		if err != nil {
			return nil, fmt.Errorf("%w: enumerate Windows PC/SC readers: %v", ErrUnavailable, err)
		}
		if info == nil || strings.TrimSpace(info.Name) == "" {
			continue
		}
		readers = append(readers, Reader{
			Name:        info.Name,
			USBPath:     "pcsc:" + info.Name,
			Product:     strings.TrimSpace(info.Name),
			CardPresent: info.State&nativepcsc.ReaderStatePresent != 0,
			ATR:         strings.ToUpper(hex.EncodeToString(info.ATR)),
		})
	}
	return readers, nil
}

func (backend windowsBackend) Open(ctx context.Context, selector Selector) (Card, error) {
	reader, err := backend.readerName(ctx, selector)
	if err != nil {
		return nil, err
	}
	card, err := nativepcsc.Open(reader)
	if err != nil {
		return nil, fmt.Errorf("%w: open Windows PC/SC reader %q: %v", ErrUnavailable, reader, err)
	}
	return &windowsCard{card: card}, nil
}

func (backend windowsBackend) readerName(ctx context.Context, selector Selector) (string, error) {
	if name := strings.TrimSpace(selector.ReaderName); name != "" {
		return name, nil
	}
	if path := strings.TrimSpace(selector.USBPath); strings.HasPrefix(path, "pcsc:") {
		if name := strings.TrimSpace(strings.TrimPrefix(path, "pcsc:")); name != "" {
			return name, nil
		}
	}
	if strings.TrimSpace(selector.USBPath) == "" {
		return "", ErrReaderNotFound
	}
	readers, err := backend.Readers(ctx)
	if err != nil {
		return "", err
	}
	for _, reader := range readers {
		if reader.USBPath == selector.USBPath {
			return reader.Name, nil
		}
	}
	return "", ErrReaderNotFound
}

type windowsCard struct {
	card *nativepcsc.Card
}

func (card *windowsCard) Transmit(ctx context.Context, command []byte) ([]byte, uint16, error) {
	response, err := card.card.Transmit(ctx, command)
	if err != nil {
		return nil, 0, err
	}
	return splitAPDUResponse(response)
}

func (card *windowsCard) Close() error {
	if card == nil || card.card == nil {
		return nil
	}
	return card.card.Close()
}
