package grpc

import (
	"errors"
	"io"
	"log"
	"time"

	"github.com/iliya/crm-service/internal/domain"
	"github.com/iliya/crm-service/internal/ingest"
	logpb "github.com/iliya/crm-service/proto/logpb"
)

// LogHandler implements the generated LogServiceServer interface. Like
// CustomerHandler, it contains no business logic - here that means no
// batching, no retries, no SQL. It only converts wire messages to domain
// values and hands them to the ingester.
type LogHandler struct {
	logpb.UnimplementedLogServiceServer
	ingester ingest.Submitter
}

func NewLogHandler(ingester ingest.Submitter) *LogHandler {
	return &LogHandler{ingester: ingester}
}

// IngestLogs is client streaming: one call, many incoming messages, exactly
// one response at the end. Contrast with ListCustomers, where stream was on
// the response side - here it is the opposite direction.
func (h *LogHandler) IngestLogs(stream logpb.LogService_IngestLogsServer) error {
	// The context lives on the stream, same as the server-streaming handler -
	// there is no ctx parameter here either.
	ctx := stream.Context()

	var accepted int64
	for {
		req, err := stream.Recv()

		// io.EOF is not a failure. It is the client saying "I am done
		// sending" - the single most important detail in client streaming.
		// SendAndClose delivers the one response and ends the RPC.
		if errors.Is(err, io.EOF) {
			return stream.SendAndClose(&logpb.IngestSummary{Accepted: accepted})
		}
		if err != nil {
			return err
		}

		occurredAt, err := time.Parse(time.RFC3339, req.GetTimestamp())
		if err != nil {
			// One malformed timestamp must not abort a stream that may carry
			// thousands of otherwise-valid entries. Fall back to "now" and
			// keep going, rather than failing the whole call.
			occurredAt = time.Now()
		}

		entry := domain.LogEntry{
			Level:      req.GetLevel(),
			Message:    req.GetMessage(),
			Service:    req.GetService(),
			OccurredAt: occurredAt,
		}

		if err := h.ingester.Submit(ctx, entry); err != nil {
			// The buffer is full (or ctx was cancelled). Same principle as
			// above: log it and continue, rather than failing every
			// remaining entry in the stream over this one.
			log.Printf("IngestLogs: dropping one entry: %v", err)
			continue
		}
		accepted++
	}
}
