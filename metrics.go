package cassette

import (
	"context"
	"errors"
	"net/http"
	"net/http/pprof"
	"runtime"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric/instrument"
	"go.opentelemetry.io/otel/sdk/metric"
)

type metrics struct {
	c        *Cassette
	server   *http.Server
	exporter *prometheus.Exporter

	lookupRequestCounter               instrument.Int64Counter
	lookupResponseTTFPHistogram        instrument.Int64Histogram
	lookupResponseResultCountHistogram instrument.Int64Histogram
	lookupResponseLatencyHistogram     instrument.Int64Histogram

	broadcastInFlightTimeHistogram   instrument.Int64Histogram
	broadcastBatchSizeHistogram      instrument.Int64Histogram
	broadcastSkipCounter             instrument.Int64Counter
	broadcastFailureCounter          instrument.Int64Counter
	broadcastRecipientsUpDownCounter instrument.Int64UpDownCounter
	broadcastInFlightUpDownCounter   instrument.Int64UpDownCounter

	receiverErrorCounter            instrument.Int64Counter
	receiverConnectionUpDownCounter instrument.Int64UpDownCounter
}

func newMetrics(c *Cassette) (*metrics, error) {
	m := metrics{
		c: c,
		server: &http.Server{
			Addr: c.metricsHttpListenAddr,
			// TODO add other metrics server options.
		},
	}
	return &m, nil
}

func (m *metrics) Start(ctx context.Context) error {
	var err error
	if m.exporter, err = prometheus.New(
		prometheus.WithoutUnits(),
		prometheus.WithoutScopeInfo(),
		prometheus.WithoutTargetInfo()); err != nil {
		return err
	}
	provider := metric.NewMeterProvider(metric.WithReader(m.exporter))
	meter := provider.Meter("ipni/cassette")

	if m.lookupRequestCounter, err = meter.Int64Counter(
		"ipni/cassette/lookup_request_count",
		instrument.WithUnit("1"),
		instrument.WithDescription("The number of lookup requests received."),
	); err != nil {
		return err
	}
	if m.lookupResponseTTFPHistogram, err = meter.Int64Histogram(
		"ipni/cassette/lookup_response_first_provider_time",
		instrument.WithUnit("ms"),
		instrument.WithDescription("The elapsed to find the first provider in milliseconds."),
	); err != nil {
		return err
	}
	if m.lookupResponseResultCountHistogram, err = meter.Int64Histogram(
		"ipni/cassette/lookup_response_result_count",
		instrument.WithUnit("1"),
		instrument.WithDescription("The number of providers found per lookup."),
	); err != nil {
		return err
	}
	if m.lookupResponseLatencyHistogram, err = meter.Int64Histogram(
		"ipni/cassette/lookup_response_latency",
		instrument.WithUnit("ms"),
		instrument.WithDescription("The lookup response latency."),
	); err != nil {
		return err
	}
	if m.broadcastInFlightTimeHistogram, err = meter.Int64Histogram(
		"ipni/cassette/broadcast_in_flight_time",
		instrument.WithUnit("ms"),
		instrument.WithDescription("The elapsed time between broadcast requested and broadcast sent."),
	); err != nil {
		return err
	}
	if m.broadcastBatchSizeHistogram, err = meter.Int64Histogram(
		"ipni/cassette/broadcast_batch_size",
		instrument.WithUnit("1"),
		instrument.WithDescription("The histogram of the number of CIDs in each broadcast message."),
	); err != nil {
		return err
	}
	if m.broadcastSkipCounter, err = meter.Int64Counter(
		"ipni/cassette/broadcast_skipped_count",
		instrument.WithUnit("1"),
		instrument.WithDescription("The number of CIDs skipped broadcast."),
	); err != nil {
		return err
	}
	if m.broadcastFailureCounter, err = meter.Int64Counter(
		"ipni/cassette/broadcast_failed_count",
		instrument.WithUnit("1"),
		instrument.WithDescription("The number of CIDs that failed broadcast."),
	); err != nil {
		return err
	}
	if m.broadcastRecipientsUpDownCounter, err = meter.Int64UpDownCounter(
		"ipni/cassette/broadcast_recipients_count",
		instrument.WithUnit("1"),
		instrument.WithDescription("The number of broadcast recipients."),
	); err != nil {
		return err
	}
	if m.broadcastInFlightUpDownCounter, err = meter.Int64UpDownCounter(
		"ipni/cassette/broadcast_in_flight_count",
		instrument.WithUnit("1"),
		instrument.WithDescription("The number of in-flight CIDs awaiting broadcast."),
	); err != nil {
		return err
	}
	if m.receiverErrorCounter, err = meter.Int64Counter(
		"ipni/cassette/receiver_error_count",
		instrument.WithUnit("1"),
		instrument.WithDescription("The number of errors observed by BitSwap receiver."),
	); err != nil {
		return err
	}
	if m.receiverConnectionUpDownCounter, err = meter.Int64UpDownCounter(
		"ipni/cassette/receiver_connection_count",
		instrument.WithUnit("1"),
		instrument.WithDescription("The number of connections  observed by BitSwap receiver."),
	); err != nil {
		return err
	}

	m.server.Handler = m.serveMux()
	go func() { _ = m.server.ListenAndServe() }()
	m.server.RegisterOnShutdown(func() {
		// TODO add timeout to exporter shutdown
		if err := m.exporter.Shutdown(context.TODO()); err != nil {
			logger.Errorw("Failed to shut down Prometheus exporter", "err", err)
		}
	})
	logger.Infow("Metric server started", "addr", m.server.Addr)
	return nil
}

func (m *metrics) serveMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	if m.c.metricsEnablePprofDebug {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		mux.HandleFunc("/debug/pprof/gc",
			func(w http.ResponseWriter, req *http.Request) {
				runtime.GC()
			},
		)
	}
	return mux
}

func (m *metrics) notifyBroadcastSkipped(ctx context.Context, batchSize int64, inFlightTime time.Duration) {
	m.broadcastInFlightTimeHistogram.Record(ctx, inFlightTime.Milliseconds(), attribute.String("status", "skipped"))
	m.broadcastSkipCounter.Add(ctx, batchSize)
	m.broadcastInFlightUpDownCounter.Add(ctx, -batchSize)
}

func (m *metrics) notifyBroadcastFailed(ctx context.Context, batchSize int64, err error, inFlightTime time.Duration) {
	errKindAttr := errKindAttribute(err)
	m.broadcastInFlightTimeHistogram.Record(ctx, inFlightTime.Milliseconds(), attribute.String("status", "failed"), errKindAttr)
	m.broadcastFailureCounter.Add(ctx, batchSize, errKindAttr)
	m.broadcastInFlightUpDownCounter.Add(ctx, -batchSize)
}

func (m *metrics) notifyBroadcastSucceeded(ctx context.Context, batchSize int64, wantHaves bool, inFlightTime time.Duration) {
	m.broadcastInFlightTimeHistogram.Record(ctx, inFlightTime.Milliseconds(), attribute.String("status", "succeeded"))
	m.broadcastBatchSizeHistogram.Record(ctx, batchSize)
	m.broadcastInFlightUpDownCounter.Add(ctx, -batchSize)
}

func (m *metrics) notifyBroadcastRequested(ctx context.Context, cidCount int64) {
	m.broadcastInFlightUpDownCounter.Add(ctx, cidCount)
}

func (m *metrics) notifyBroadcastRecipientAdded(ctx context.Context) {
	m.broadcastRecipientsUpDownCounter.Add(ctx, 1)
}
func (m *metrics) notifyBroadcastRecipientRemoved(ctx context.Context) {
	m.broadcastRecipientsUpDownCounter.Add(ctx, -1)
}

func (m *metrics) notifyReceiverErrored(ctx context.Context, err error) {
	m.receiverErrorCounter.Add(ctx, 1, errKindAttribute(err))
}

func (m *metrics) notifyReceiverConnected(ctx context.Context) {
	m.receiverConnectionUpDownCounter.Add(ctx, 1)
}

func (m *metrics) notifyReceiverDisconnected(ctx context.Context) {
	m.receiverConnectionUpDownCounter.Add(ctx, -1)
}

func (m *metrics) notifyLookupRequested(ctx context.Context) {
	m.lookupRequestCounter.Add(ctx, 1)
}

func (m *metrics) notifyLookupResponded(ctx context.Context, resultCount int64, timeToFirstResult time.Duration, latency time.Duration) {
	if resultCount > 0 {
		m.lookupResponseTTFPHistogram.Record(ctx, timeToFirstResult.Milliseconds())
	}
	m.lookupResponseResultCountHistogram.Record(ctx, resultCount)
	m.lookupResponseLatencyHistogram.Record(ctx, latency.Milliseconds())
}

func errKindAttribute(err error) attribute.KeyValue {
	// TODO check logs for other popular error kinds we might care about.
	var errKind string
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		errKind = "deadline-exceeded"
	case errors.Is(err, context.Canceled):
		errKind = "canceled"
	case errors.Is(err, network.ErrReset):
		errKind = "stream-reset"
	case errors.Is(err, network.ErrResourceLimitExceeded):
		errKind = "resource-limit"
	default:
		errKind = "other"
	}
	return attribute.String("error-kind", errKind)
}

func (m *metrics) Shutdown(ctx context.Context) error {
	return m.server.Shutdown(ctx)
}