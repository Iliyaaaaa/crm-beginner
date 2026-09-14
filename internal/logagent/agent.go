package logagent

import (
	"context"
	"errors"

	logpb "github.com/iliya/crm-service/proto/logpb"
)

// Run starts the tailer and the shipper and blocks until ctx is cancelled.
//
// Shutdown order matters. The tailer stops first and the channel is closed
// after it, so the shipper sees the close only once nothing more can arrive:
// it sends what it still holds, saves the positions, and only then does Run
// return.
func Run(ctx context.Context, cfg Config, client logpb.LogServiceClient) error {
	positions, err := LoadPositions(cfg.PositionsFile)
	if err != nil {
		return err
	}

	entries := make(chan Entry, cfg.BatchSize)
	shipper := NewShipper(client, positions, cfg.BatchSize, cfg.FlushInterval)
	shipped := make(chan struct{})
	go func() {
		defer close(shipped)
		shipper.Run(ctx, entries)
	}()

	err = NewTailer(cfg, positions).Run(ctx, entries)
	close(entries)
	<-shipped

	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
