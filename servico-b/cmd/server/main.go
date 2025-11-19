package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
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

type ViaCEPResponse struct {
	Cep         string `json:"cep"`
	Logradouro  string `json:"logradouro"`
	Complemento string `json:"complemento"`
	Bairro      string `json:"bairro"`
	Localidade  string `json:"localidade"`
	UF          string `json:"uf"`
	Erro        bool   `json:"erro"`
}

type WeatherAPIResponse struct {
	Location struct {
		Name string `json:"name"`
	} `json:"location"`
	Current struct {
		TempC float64 `json:"temp_c"`
	} `json:"current"`
}

type TemperatureResponse struct {
	City  string  `json:"city"`
	TempC float64 `json:"temp_C"`
	TempF float64 `json:"temp_F"`
	TempK float64 `json:"temp_K"`
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

func celsiusToFahrenheit(c float64) float64 {
	return c*1.8 + 32
}

func celsiusToKelvin(c float64) float64 {
	return c + 273
}

func searchCEP(ctx context.Context, cep string) (*ViaCEPResponse, error) {
	tracer := otel.Tracer("servico-b")
	ctx, span := tracer.Start(ctx, "servico-b.searchCEP")
	defer span.End()

	url := fmt.Sprintf("https://viacep.com.br/ws/%s/json/", cep)
	
	span.SetAttributes(
		semconv.HTTPMethod("GET"),
		semconv.HTTPURL(url),
	)
	
	startTime := time.Now()
	resp, err := http.Get(url)
	duration := time.Since(startTime)
	
	if err != nil {
		span.RecordError(err)
		return nil, err
	}
	defer resp.Body.Close()
	
	span.SetAttributes(
		semconv.HTTPStatusCode(resp.StatusCode),
	)

	if resp.StatusCode == http.StatusBadRequest {
		return nil, fmt.Errorf("invalid zipcode")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var viaCEPResp ViaCEPResponse
	if err := json.Unmarshal(body, &viaCEPResp); err != nil {
		return nil, err
	}

	if viaCEPResp.Erro {
		return nil, fmt.Errorf("can not find zipcode")
	}

	log.Printf("CEP search took %v", duration)

	return &viaCEPResp, nil
}

func getTemperature(ctx context.Context, city string) (float64, error) {
	tracer := otel.Tracer("servico-b")
	ctx, span := tracer.Start(ctx, "servico-b.getTemperature")
	defer span.End()

	weatherAPIKey := os.Getenv("WEATHER_API_KEY")
	if weatherAPIKey == "" {
		return 0, fmt.Errorf("WEATHER_API_KEY not set")
	}

	// URL encode a cidade para evitar problemas com espaços e caracteres especiais
	encodedCity := url.QueryEscape(city)
	url := fmt.Sprintf("http://api.weatherapi.com/v1/current.json?key=%s&q=%s&aqi=no", weatherAPIKey, encodedCity)
	
	span.SetAttributes(
		semconv.HTTPMethod("GET"),
		semconv.HTTPURL(url),
	)
	
	startTime := time.Now()
	resp, err := http.Get(url)
	duration := time.Since(startTime)
	
	if err != nil {
		span.RecordError(err)
		return 0, err
	}
	defer resp.Body.Close()
	
	span.SetAttributes(
		semconv.HTTPStatusCode(resp.StatusCode),
	)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("Weather API error response: %s", string(body))
		return 0, fmt.Errorf("weather API returned status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var weatherResp WeatherAPIResponse
	if err := json.Unmarshal(body, &weatherResp); err != nil {
		return 0, err
	}

	log.Printf("Temperature search took %v", duration)

	return weatherResp.Current.TempC, nil
}

func handleTemperature(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tracer := otel.Tracer("servico-b")
	
	ctx, span := tracer.Start(ctx, "servico-b.handleTemperature")
	defer span.End()

	cep := r.Header.Get("X-CEP")
	if cep == "" {
		span.RecordError(fmt.Errorf("CEP not provided"))
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "CEP header is required"})
		return
	}

	ctx, validateSpan := tracer.Start(ctx, "servico-b.validateCEP")
	isValid := validateCEP(cep)
	validateSpan.End()

	if !isValid {
		span.RecordError(fmt.Errorf("invalid zipcode"))
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid zipcode"})
		return
	}

	viaCEPResp, err := searchCEP(ctx, cep)
	if err != nil {
		if err.Error() == "invalid zipcode" {
			span.RecordError(err)
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid zipcode"})
			return
		}
		if err.Error() == "can not find zipcode" {
			span.RecordError(err)
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]string{"error": "can not find zipcode"})
			return
		}
		span.RecordError(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tempC, err := getTemperature(ctx, viaCEPResp.Localidade)
	if err != nil {
		span.RecordError(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tempF := celsiusToFahrenheit(tempC)
	tempK := celsiusToKelvin(tempC)

	response := TemperatureResponse{
		City:  viaCEPResp.Localidade,
		TempC: tempC,
		TempF: tempF,
		TempK: tempK,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
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
		serviceName = "servico-b"
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
	router.Post("/temperature", handleTemperature)

	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = ":8081"
	}

	go func() {
		log.Printf("Serviço B iniciado na porta %s", port)
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

