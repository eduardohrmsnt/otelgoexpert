# Sistema de Temperatura por CEP com OpenTelemetry e Zipkin

Este projeto implementa um sistema distribuído em Go que recebe um CEP, identifica a cidade e retorna o clima atual (temperatura em graus Celsius, Fahrenheit e Kelvin) juntamente com a cidade. O sistema implementa OpenTelemetry (OTEL) para tracing distribuído e Zipkin para visualização dos traces.

## Arquitetura

O sistema é composto por dois serviços:

### Serviço A (Porta 8080)
- Recebe requisições POST com CEP
- Valida o formato do CEP (8 dígitos, string)
- Encaminha requisições válidas para o Serviço B
- Retorna erro 422 para CEPs inválidos

### Serviço B (Porta 8081)
- Recebe CEP válido do Serviço A
- Busca localização via API ViaCEP
- Busca temperatura via WeatherAPI
- Converte temperaturas (Celsius, Fahrenheit, Kelvin)
- Retorna resposta formatada

## Pré-requisitos

- Docker e Docker Compose instalados
- Chave de API do WeatherAPI (obtenha em https://www.weatherapi.com/)

## Configuração

1. Clone o repositório:
```bash
git clone <repository-url>
cd otel
```

2. Configure a variável de ambiente com sua chave da WeatherAPI:
```bash
export WEATHER_API_KEY=sua_chave_aqui
```

Ou crie um arquivo `.env` na raiz do projeto:
```
WEATHER_API_KEY=sua_chave_aqui
```

## Executando o Projeto

### Usando Docker Compose

1. Execute o docker-compose para subir todos os serviços:
```bash
docker-compose up --build
```

Isso irá iniciar:
- **Serviço A**: http://localhost:8080
- **Serviço B**: http://localhost:8081
- **Zipkin UI**: http://localhost:9411
- **Jaeger UI**: http://localhost:16686
- **OTEL Collector**: porta 4317 (gRPC)

2. Aguarde alguns segundos para todos os serviços iniciarem completamente.

### Testando o Sistema

#### Exemplo de requisição válida:
```bash
curl -X POST http://localhost:8080 \
  -H "Content-Type: application/json" \
  -d '{"cep": "01310100"}'
```

**Resposta esperada (200):**
```json
{
  "city": "São Paulo",
  "temp_C": 28.5,
  "temp_F": 83.3,
  "temp_K": 301.5
}
```

#### Exemplo de CEP inválido:
```bash
curl -X POST http://localhost:8080 \
  -H "Content-Type: application/json" \
  -d '{"cep": "123"}'
```

**Resposta esperada (422):**
```json
{
  "error": "invalid zipcode"
}
```

#### Exemplo de CEP não encontrado:
```bash
curl -X POST http://localhost:8080 \
  -H "Content-Type: application/json" \
  -d '{"cep": "99999999"}'
```

**Resposta esperada (404):**
```json
{
  "error": "can not find zipcode"
}
```

## Visualizando Traces

### Zipkin
Acesse http://localhost:9411 para visualizar os traces distribuídos no Zipkin.

### Jaeger
Acesse http://localhost:16686 para visualizar os traces no Jaeger (opcional).

## Estrutura do Projeto

```
otel/
├── servico-a/              # Serviço A - Validação e encaminhamento
│   ├── cmd/
│   │   └── server/
│   │       └── main.go
│   ├── Dockerfile
│   └── go.mod
├── servico-b/              # Serviço B - Busca CEP e temperatura
│   ├── cmd/
│   │   └── server/
│   │       └── main.go
│   ├── Dockerfile
│   └── go.mod
├── .docker/
│   └── otel-collector-config.yaml  # Configuração do OTEL Collector
├── docker-compose.yaml     # Orquestração dos serviços
└── README.md
```

## OpenTelemetry

O projeto implementa tracing distribuído usando OpenTelemetry:

- **Spans criados:**
  - `servico-a.handleCEP`: Processamento da requisição no Serviço A
  - `servico-a.validateCEP`: Validação do CEP
  - `servico-a.callServicoB`: Chamada HTTP para o Serviço B
  - `servico-b.handleTemperature`: Processamento da requisição no Serviço B
  - `servico-b.validateCEP`: Validação do CEP no Serviço B
  - `servico-b.searchCEP`: Busca do CEP na API ViaCEP (com tempo de resposta)
  - `servico-b.getTemperature`: Busca da temperatura na WeatherAPI (com tempo de resposta)

- **Propagação de contexto:** Os traces são propagados entre os serviços usando headers HTTP.

## APIs Externas Utilizadas

- **ViaCEP**: https://viacep.com.br/ - Para buscar informações de localização pelo CEP
- **WeatherAPI**: https://www.weatherapi.com/ - Para buscar temperatura atual

## Conversões de Temperatura

- **Fahrenheit**: `F = C * 1.8 + 32`
- **Kelvin**: `K = C + 273`

## Desenvolvimento Local

Para desenvolver localmente sem Docker:

1. Instale as dependências:
```bash
cd servico-a && go mod download
cd ../servico-b && go mod download
```

2. Configure as variáveis de ambiente:
```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317
export OTEL_SERVICE_NAME=servico-a  # ou servico-b
export HTTP_PORT=:8080  # ou :8081
export WEATHER_API_KEY=sua_chave
export SERVICO_B_URL=http://localhost:8081  # apenas para servico-a
```

3. Execute os serviços:
```bash
# Terminal 1 - Serviço B
cd servico-b && go run cmd/server/main.go

# Terminal 2 - Serviço A
cd servico-a && go run cmd/server/main.go
```

## Troubleshooting

### Erro: "WEATHER_API_KEY not set"
Certifique-se de que a variável de ambiente `WEATHER_API_KEY` está configurada antes de executar o docker-compose.

### Erro: "can not find zipcode"
O CEP informado não foi encontrado na base de dados do ViaCEP. Verifique se o CEP está correto.

### Traces não aparecem no Zipkin
1. Verifique se o OTEL Collector está rodando: `docker ps | grep otel-collector`
2. Verifique os logs do collector: `docker logs otel-collector`
3. Certifique-se de que o Zipkin está acessível: `curl http://localhost:9411`

## Licença

Este projeto foi desenvolvido como parte de um laboratório de OpenTelemetry.

# otelgoexpert
