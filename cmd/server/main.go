package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"

	"checkout-service/internal/checkout"
	pb "checkout-service/proto"

	"github.com/grafana/pyroscope-go"
	grpcprom "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

var log *logrus.Logger

func init() {
	log = logrus.New()
	log.Formatter = &logrus.JSONFormatter{
		FieldMap: logrus.FieldMap{
			logrus.FieldKeyTime:  "timestamp",
			logrus.FieldKeyLevel: "severity",
			logrus.FieldKeyMsg:   "message",
		},
		TimestampFormat: time.RFC3339Nano,
	}
	log.Out = os.Stdout
}

func main() {
	// --- Tracing ---
	if os.Getenv("ENABLE_TRACING") == "1" {
		if err := initTracing(); err != nil {
			log.Warnf("tracing init failed, continuing without it: %v", err)
		} else {
			log.Info("tracing enabled")
		}
	} else {
		log.Info("tracing disabled — set ENABLE_TRACING=1 to enable")
	}

	// --- Profiling ---
	if os.Getenv("ENABLE_PROFILING") == "1" {
		initProfiling()
	} else {
		log.Info("profiling disabled — set ENABLE_PROFILING=1 to enable")
	}

	port := "5050"
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}

	log.Infof("starting grpc server on :%s", port)
	if err := run(port); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func run(port string) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%s", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %s: %w", port, err)
	}

	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		),
	)

	srv := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.UnaryInterceptor(grpcprom.UnaryServerInterceptor),
		grpc.StreamInterceptor(grpcprom.StreamServerInterceptor),
	)

	// --- Connect to downstream services ---
	svc := &checkout.Service{}

	var connErr error
	if svc.ProductCatalogConn, connErr = dialGRPC(envOrDefault("PRODUCT_CATALOG_SERVICE_ADDR", "product-catalog-service:3550")); connErr != nil {
		return fmt.Errorf("connect to product-catalog: %w", connErr)
	}
	if svc.CartConn, connErr = dialGRPC(envOrDefault("CART_SERVICE_ADDR", "cart-service:7070")); connErr != nil {
		return fmt.Errorf("connect to cart: %w", connErr)
	}
	if svc.CurrencyConn, connErr = dialGRPC(envOrDefault("CURRENCY_SERVICE_ADDR", "currency-service:7000")); connErr != nil {
		return fmt.Errorf("connect to currency: %w", connErr)
	}
	if svc.ShippingConn, connErr = dialGRPC(envOrDefault("SHIPPING_SERVICE_ADDR", "shipping-service:50051")); connErr != nil {
		return fmt.Errorf("connect to shipping: %w", connErr)
	}
	if svc.PaymentConn, connErr = dialGRPC(envOrDefault("PAYMENT_SERVICE_ADDR", "payment-service:50051")); connErr != nil {
		return fmt.Errorf("connect to payment: %w", connErr)
	}
	if svc.EmailConn, connErr = dialGRPC(envOrDefault("EMAIL_SERVICE_ADDR", "email-service:8080")); connErr != nil {
		return fmt.Errorf("connect to email: %w", connErr)
	}

	pb.RegisterCheckoutServiceServer(srv, svc)

	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(srv, healthSrv)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	reflection.Register(srv)

	grpcprom.Register(srv)
	grpcprom.EnableHandlingTimeHistogram()

	// --- Metrics + pprof HTTP server ---
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		mux.Handle("/debug/pprof/", http.DefaultServeMux)
		metricsPort := envOrDefault("METRICS_PORT", "9090")
		log.Infof("metrics + pprof endpoint on :%s", metricsPort)
		if err := http.ListenAndServe(":"+metricsPort, mux); err != nil {
			log.Warnf("metrics server error: %v", err)
		}
	}()

	log.Infof("listening on %s", listener.Addr().String())
	return srv.Serve(listener)
}

func dialGRPC(addr string) (*grpc.ClientConn, error) {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	)
	if err != nil {
		return nil, fmt.Errorf("grpc dial %s: %w", addr, err)
	}
	return conn, nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func initTracing() error {
	collectorAddr := os.Getenv("COLLECTOR_SERVICE_ADDR")
	if collectorAddr == "" {
		return fmt.Errorf("COLLECTOR_SERVICE_ADDR not set")
	}

	ctx := context.Background()

	conn, err := grpc.NewClient(
		collectorAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("failed to connect to collector %s: %w", collectorAddr, err)
	}

	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		return fmt.Errorf("failed to create otlp exporter: %w", err)
	}

	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "checkout-service"
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
		),
	)
	if err != nil {
		return fmt.Errorf("failed to create resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return nil
}

func initProfiling() {
	pyroscopeAddr := os.Getenv("PYROSCOPE_ADDR")
	if pyroscopeAddr == "" {
		pyroscopeAddr = "http://pyroscope:4040"
	}

	_, err := pyroscope.Start(pyroscope.Config{
		ApplicationName: "checkout-service",
		ServerAddress:   pyroscopeAddr,
		Logger:          pyroscope.StandardLogger,
		ProfileTypes: []pyroscope.ProfileType{
			pyroscope.ProfileCPU,
			pyroscope.ProfileAllocObjects,
			pyroscope.ProfileAllocSpace,
			pyroscope.ProfileInuseObjects,
			pyroscope.ProfileInuseSpace,
		},
	})
	if err != nil {
		log.Warnf("pyroscope init failed, continuing without it: %v", err)
		return
	}
	log.Info("profiling enabled → pushing to " + pyroscopeAddr)
}