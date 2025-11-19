package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type CEPRequest struct {
	CEP string `json:"cep"`
}

type CEPResponse struct {
	City   string  `json:"city"`
	TempC  float64 `json:"temp_C"`
	TempF  float64 `json:"temp_F"`
	TempK  float64 `json:"temp_K"`
}

func initProvider(serviceName, collectorURL string) (func(context.Context) error, error) {
	ctx := context.Background()

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Retry logic para conectar ao collector
	var conn *grpc.ClientConn
	maxRetries := 20
	retryDelay := 2 * time.Second
	
	for i := 0; i < maxRetries; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var err error
		conn, err = grpc.DialContext(ctx, collectorURL,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithBlock(),
		)
		cancel()
		
		if err == nil {
			log.Printf("Successfully connected to OTEL collector after %d attempts", i+1)
			break
		}
		
		if i < maxRetries-1 {
			log.Printf("Failed to connect to collector (attempt %d/%d): %v. Retrying in %v...", i+1, maxRetries, err, retryDelay)
			time.Sleep(retryDelay)
		} else {
			return nil, fmt.Errorf("failed to create gRPC connection to collector after %d attempts: %w", maxRetries, err)
		}
	}

	traceExporter, err := otlptracegrpc.New(context.Background(), otlptracegrpc.WithGRPCConn(conn))
	if err != nil {
		return nil, fmt.Errorf("failed to create trace exporter: %w", err)
	}

	bsp := sdktrace.NewBatchSpanProcessor(traceExporter)
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(bsp),
	)
	otel.SetTracerProvider(tracerProvider)

	otel.SetTextMapPropagator(propagation.TraceContext{})

	return tracerProvider.Shutdown, nil
}

func validateCEP(cep string) bool {
	if len(cep) != 8 {
		return false
	}
	for _, char := range cep {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func handleCEP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tracer := otel.Tracer("servico-a")
	
	ctx, span := tracer.Start(ctx, "servico-a.handleCEP")
	defer span.End()

	var req CEPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		span.RecordError(err)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	ctx, validateSpan := tracer.Start(ctx, "servico-a.validateCEP")
	isValid := validateCEP(req.CEP)
	validateSpan.End()

	if !isValid {
		span.RecordError(fmt.Errorf("invalid zipcode"))
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid zipcode"})
		return
	}

	servicoBURL := os.Getenv("SERVICO_B_URL")
	if servicoBURL == "" {
		servicoBURL = "http://servico-b:8081"
	}

	ctx, callSpan := tracer.Start(ctx, "servico-a.callServicoB")
	defer callSpan.End()

	httpReq, err := http.NewRequestWithContext(ctx, "POST", servicoBURL+"/temperature", nil)
	if err != nil {
		callSpan.RecordError(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-CEP", req.CEP)
	
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(httpReq.Header))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		callSpan.RecordError(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		callSpan.RecordError(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Se não for status 200, retornar o erro do servico-b
	if resp.StatusCode != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(bodyBytes)
		return
	}

	var cepResp CEPResponse
	if err := json.Unmarshal(bodyBytes, &cepResp); err != nil {
		callSpan.RecordError(err)
		http.Error(w, fmt.Sprintf("failed to decode response: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(cepResp)
}

func main() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	collectorURL := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if collectorURL == "" {
		collectorURL = "otel-collector:4317"
	}

	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "servico-a"
	}

	shutdown, err := initProvider(serviceName, collectorURL)
	if err != nil {
		log.Printf("Warning: Failed to initialize OTEL provider: %v. Continuing without tracing.", err)
		shutdown = func(context.Context) error { return nil }
	}
	defer func() {
		if err := shutdown(ctx); err != nil {
			log.Printf("Warning: Failed to shutdown TracerProvider: %v", err)
		}
	}()

	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Use(middleware.RealIP)
	router.Use(middleware.Logger)
	router.Use(middleware.Recoverer)
	router.Post("/", handleCEP)

	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = ":8080"
	}

	go func() {
		log.Printf("Serviço A iniciado na porta %s", port)
		if err := http.ListenAndServe(port, router); err != nil {
			log.Fatal(err)
		}
	}()

	select {
	case <-sigCh:
		log.Println("Shutting down gracefully, CTRL+C pressed...")
	case <-ctx.Done():
		log.Println("Shutting down due to other reason...")
	}
}

