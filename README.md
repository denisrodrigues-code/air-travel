# airtravel

Scraper por requisição da API interna da **TAP Air Portugal**
(`booking.flytap.com`), escrito em Go. Consome os endpoints JSON do site
diretamente — sem navegador, sem renderização — reproduzindo a impressão digital
TLS e HTTP/2 do Chrome.

Dados tratados vão para **PostgreSQL**; as respostas brutas, para **Redis**.

Exercício da Semana 5 do programa trainee (trilha Crawler/RPA + IA): engenharia
reversa de API interna, gestão de sessão e persistência em dois destinos.

---

## Começando

**Pré-requisitos:** Docker e **Go 1.25+** (exigido pelo `pgx/v5`). Nada mais —
não é preciso obter cookies nem configurar nada.

```bash
cd airtravel
./run.sh demo
```

O `demo` sobe os bancos, roda os testes, exercita os três modos de coleta
contra a API real e mostra o resultado em SQL. Leva cerca de um minuto.

<details>
<summary>Saída esperada</summary>

```
Container airtravel-postgres Started
Container airtravel-redis Started
aguardando serviços..... prontos

ok  	airtravel/internal/api	0.008s
ok  	airtravel/internal/collect	0.004s
ok  	airtravel/internal/models	0.043s
ok  	airtravel/internal/tap	0.011s

ROTA     DATA        DIA  CABINE  MENOR PREÇO  MÍN. MÊS
----     ----        ---  ------  -----------  --------
LIS-GIG  01/09/2026  ter  E       487.21 EUR   sim
LIS-RIO  02/09/2026  qua  E       487.21 EUR   sim

Buscas: 1 total | 1 concluídas | 0 ignoradas | 0 falhas | 355 ofertas coletadas

LIS-RIO · ida 01/09/2026
VOLTA       DIA  NOITES  TOTAL   MÍN. MÊS
-----       ---  ------  -----   --------
23/09/2026  qua  22      445.92  sim
04/10/2026  dom  33      445.92  sim
```

</details>

Se travar em `aguardando serviços`, o Docker não está rodando.

---

## API HTTP

```bash
./run.sh up && ./run.sh api
```

Documentação interativa em **<http://localhost:8080/docs>** (Swagger UI; `/`
redireciona para lá). A especificação OpenAPI 3.1 vai **embutida no binário** e é
servida em `/openapi.yaml` — funciona em qualquer cliente OpenAPI mesmo sem
acesso ao CDN do Swagger UI.

| Método | Rota | O que faz |
|---|---|---|
| `POST` | `/api/v1/searches` | Coleta voos e tarifas na TAP, persiste nos dois destinos, devolve as ofertas |
| `GET` | `/api/v1/flights` | O mesmo, com os critérios na query string |
| `GET` | `/api/v1/calendar` | Melhor preço por data de partida |
| `GET` | `/api/v1/returns` | Matriz ida × volta |
| `GET` | `/api/v1/searches` | Histórico, filtrável por `origin`/`destination`/`market` |
| `GET` | `/api/v1/searches/{key}` | Uma busca do histórico, com as ofertas |
| `GET` | `/api/v1/searches/{key}/raw` | Payload bruto do Redis. `?body=true` devolve o JSON original |
| `GET` | `/health` · `/health/ready` | Liveness · readiness (verifica PostgreSQL e Redis) |
| `GET` | `/docs` · `/openapi.yaml` | Swagger UI · especificação |

**`POST` coleta; `GET` lê do banco.** Os endpoints de calendário e matriz aceitam
`refresh=true` para coletar antes de responder — não é o padrão porque cada
coleta consulta o GDS da TAP e leva de 3 a 9 segundos.

Datas de entrada aceitam `2026-09-01`, `01/09/2026`, `01-09-2026` ou `01092026`.
O `DDMMYYYY` que o BFM exige é detalhe do adapter.

### `POST /api/v1/searches` · voos e tarifas

Corpo JSON. `origin`, `destination` e `departureDate` são obrigatórios.

| Campo | Tipo | Padrão | Observação |
|---|---|---|---|
| `origin` · `destination` | string | — | IATA de aeroporto (`GRU`) ou cidade (`RIO`) |
| `departureDate` | string | — | |
| `returnDate` | string | — | ausente ⇒ somente ida |
| `cabinClass` | `E`\|`W`\|`C` | `E` | economy, premium, executiva |
| `passengers.adults` | int | `1` | também `youths`, `children`, `infants` |
| `limit` | int | `50` | corta a lista de ofertas |

```bash
curl -s -X POST localhost:8080/api/v1/searches \
  -H 'content-type: application/json' \
  -d '{"origin":"LIS","destination":"RIO","departureDate":"2026-09-01",
       "passengers":{"adults":1},"limit":3}' | jq
```

```json
{
  "searchKey": "search:LIS:RIO:01092026:OW:E:PT:1",
  "search": { "origin": "LIS", "destination": "RIO", "currency": "EUR",
              "officeId": "LISTP08AB", "totalOffers": 104,
              "rawKey": "tap:raw:search:LIS:RIO:01092026:OW:E:PT:1:1785776319" },
  "offers": [
    { "route": "LIS-GIG", "flightNumbers": ["TP71"],
      "departureTime": "2026-09-01T18:55:00Z", "arrivalTime": "2026-09-02T00:50:00Z",
      "durationMinutes": 595, "numberOfStops": 0,
      "fareFamily": "DISCINT", "totalPrice": 615.21, "tax": 291.21, "currency": "EUR" }
  ],
  "capture": { "tlsProfile": "firefox_148", "engine": "gecko",
               "latencyMs": 8800, "flights": 34, "offers": 104 }
}
```

O bloco `capture` responde "qual combinação capturou este preço" — e `rawKey`
liga a resposta ao payload original no Redis.

### `GET /api/v1/flights` · o mesmo por query string

`origin`, `destination` e `departureDate` obrigatórios; aceita também
`returnDate`, `cabinClass`, `adults` e `limit`.

```bash
curl -s -G localhost:8080/api/v1/flights \
  -d origin=LIS -d destination=RIO -d departureDate=2026-09-01 -d limit=3 | jq
```

### `GET /api/v1/calendar` · melhor preço por data de partida

Uma requisição à TAP cobre ~365 datas.

| Parâmetro | Padrão | Observação |
|---|---|---|
| `origin` · `destination` | — | obrigatórios |
| `tripType` | `R` | `O` só ida · `R` **ida e volta** (mais barato) |
| `cabinClass` | `E` | |
| `adults` | `1` | **identifica a série**, não filtra: ver abaixo |
| `from` · `to` | — | recorta o intervalo de datas |
| `refresh` | `false` | coleta na TAP antes de responder |
| `limit` | `50` | teto de 1000 |

> **`adults` faz parte da identidade do preço.** O total para 1 e para 2
> passageiros são valores diferentes para a mesma data, coletados e guardados em
> separado. Consultar com um `adults` que não foi coletado devolve lista vazia —
> não o preço de outra contagem. Use `refresh=true` para coletar a série que
> falta.

```bash
curl -s -G localhost:8080/api/v1/calendar \
  -d origin=LIS -d destination=RIO -d tripType=R \
  -d from=2026-09-01 -d to=2026-10-31 -d limit=3 | jq
```

```json
{
  "origin": "LIS", "destination": "RIO", "tripType": "R", "adults": 1,
  "currency": "EUR",
  "cheapest": { "departureDate": "2026-09-01T00:00:00Z", "bestTotalPrice": 487.21,
                "departureAirport": "LIS", "arrivalAirport": "GIG",
                "monthlyMinimum": true },
  "dates": [ "..." ]
}
```

### `GET /api/v1/returns` · matriz ida × volta

Fixada a data de ida, devolve o preço **total** de cada data de retorno (~337 por
requisição). `nights` é o eixo de análise: "a viagem de 18 noites mais barata".

| Parâmetro | Padrão | Observação |
|---|---|---|
| `origin` · `destination` | — | obrigatórios |
| `departureDate` | — | **obrigatório** quando `refresh=true` |
| `cabinClass` | `E` | |
| `adults` | `1` | identifica a série, como no calendário |
| `minNights` · `maxNights` | — | filtra a duração da viagem |
| `refresh` | `false` | |
| `limit` | `50` | |

```bash
curl -s -G localhost:8080/api/v1/returns \
  -d origin=LIS -d destination=RIO -d departureDate=2026-09-01 \
  -d minNights=7 -d maxNights=21 -d limit=3 | jq
```

```json
{
  "origin": "LIS", "destination": "RIO", "departureDate": "2026-09-01",
  "currency": "EUR",
  "cheapest": { "returnDate": "2026-09-21T00:00:00Z", "nights": 20,
                "totalPrice": 520.92, "resolvedDestination": "GIG" },
  "combinations": [ "..." ]
}
```

### Histórico

| Rota | Parâmetros |
|---|---|
| `GET /api/v1/searches` | `origin`, `destination`, `market`, `limit`, `offset` |
| `GET /api/v1/searches/{key}` | `limit` |
| `GET /api/v1/searches/{key}/raw` | `body=true` devolve o JSON original da TAP |

`{key}` é a chave canônica, ex.: `search:LIS:RIO:01092026:OW:E:PT:1`. Sem
`body=true`, o endpoint de payload bruto devolve só os metadados — a resposta
original tem centenas de kilobytes.

```bash
curl -s -G localhost:8080/api/v1/searches -d origin=LIS -d limit=5 \
  | jq '.items[].searchKey'

curl -s localhost:8080/api/v1/searches/search:LIS:RIO:01092026:OW:E:PT:1/raw | jq
```

> Os exemplos usam `curl -G` com `-d` em vez de colar a query na URL: uma URL
> longa entre aspas **não** pode ser quebrada com `\`, porque dentro das aspas a
> barra é literal e corrompe a requisição.

### Erros

Corpo no espírito do RFC 9457, com um `code` estável para lógica no cliente:

```json
{ "status": 502, "code": "upstream_blocked",
  "detail": "acesso negado pelo WAF da TAP: HTTP 403" }
```

| Situação | Status | `code` |
|---|---|---|
| Entrada inválida | 400 | `bad_request` |
| Busca inexistente, ou payload bruto expirado | 404 | `not_found` |
| Método não suportado na rota | 405 | `method_not_allowed` |
| WAF ou desafio da Cloudflare | **502** | `upstream_blocked` |
| Limite de requisições da TAP | 429 | `upstream_rate_limited` |
| TAP não respondeu no prazo | 504 | `upstream_timeout` |

O bruto tem TTL de 7 dias no Redis e o registro tratado não expira, então um `404`
em `/searches/{key}/raw` costuma significar "a busca existe, a captura já não" — e
não que a chave seja desconhecida.

Um bloqueio é **502**, não 403: o cliente não errou — o provedor upstream
recusou. O handler não sabe o que é um JA3: a tradução compara apenas com os erros
de `internal/collect`, e é o adapter da TAP que traduz as suas falhas para esse
vocabulário.

---

## Comandos

`./run.sh <comando>` — use `./run.sh help` para a lista completa.
Há um `Makefile` equivalente para quem tem `make` (o WSL costuma não trazer).

| Comando | O que faz |
|---|---|
| `demo` | `up` + `test` + `calendar` + `returns` + `search` + `queries` |
| `up` / `down` / `reset` | sobe / para / apaga os volumes |
| `test` | 185 testes offline: fixtures reais + dublês |
| `test-int` | 11 testes contra PostgreSQL e Redis reais |
| `check` | `gofmt` + `go vet` + testes |
| `calendar` | melhor preço por data de partida |
| `returns` | matriz ida × volta |
| `search` | voos, horários e tarifas detalhados |
| `api` | sobe a API HTTP com Swagger em `/docs` |
| `queries` | mostra o que foi coletado ([`queries.sql`](queries.sql)) |
| `redis` | lista as chaves das respostas brutas |
| `psql` | abre um shell no PostgreSQL |

Parametrize por variável de ambiente:

```bash
ORIGINS=OPO DESTINATIONS=GRU FROM=01-12-2026 TO=31-01-2027 ./run.sh calendar
```

`ORIGINS` · `DESTINATIONS` · `CABINS` · `ADULTS` · `START` · `DAYS` · `FROM` ·
`TO` · `TOP` · `MARKET` · `LANGUAGE` · `PROXY` · `PROFILE` · `API_PORT`

`PROXY` é lido tanto pelo `cmd/scraper` quanto pelo `cmd/api`, e é vazio por
padrão nos dois. Aponte-o para o powhttp (`http://localhost:8888`) para inspecionar
o tráfego.

### Mercado e moeda

`MARKET` define a moeda **e as regras tarifárias**. O padrão é `PT` (euros); use
`BR` para reproduzir o site brasileiro:

```bash
MARKET=BR ./run.sh calendar    # preços em BRL
```

**Não é conversão de câmbio.** LIS→RIO ida e volta, economy, medido no mesmo dia:

| `MARKET` | Moeda | Menor | Datas | Data mais barata |
|---|---|---|---|---|
| `PT` | EUR | 487,21 | 355 | 18/08/2026 |
| `BR` | BRL | 2.477,49 | 345 | 23/02/2027 |

A ~6,26 BRL/EUR, os 487,21 € dariam ~3.050 BRL — mas o mercado BR cobra
2.477,49, cerca de **19% mais barato**. O inventário também difere (345 contra
355 datas com voo) e o mínimo cai em outro mês. São conjuntos tarifários
distintos, não a mesma tarifa em outra moeda.

---

## O que é coletado

Três modos, selecionados por `-mode`:

### `calendar` — melhor preço por data de partida

Uma requisição devolve **um ano de preços diários** para a rota. É o modo mais
econômico: uma chamada por rota, em vez de uma por data.

```bash
go run ./cmd/scraper -mode calendar \
  -origins LIS -destinations RIO -trip-type R \
  -from 01-09-2026 -to 31-10-2026
```

`-trip-type` altera o preço, não só o formato: com `R` a API devolve a **tarifa
de ida e volta**, sensivelmente mais barata que a de só ida.

| `-trip-type` | LIS→RIO, set–out/2026 |
|---|---|
| `O` (só ida) | menor 615,21 € · média 643,84 € |
| `R` (ida e volta) | menor **487,21 €** · média 552,15 € |

> Nunca some duas pernas de só ida para estimar um round-trip: a diferença
> observada foi de quase 3×.

### `returns` — matriz ida × volta

Fixada a data de ida, devolve o **preço total** para cada data de retorno
possível (~337 datas por requisição). É a dimensão que permite responder "qual a
viagem de 18 noites mais barata?".

```bash
go run ./cmd/scraper -mode returns \
  -origins LIS -destinations RIO \
  -start 01-09-2026 -days 7
```

A coluna `nights` é derivada na gravação, para servir de eixo de análise.

### `search` — voos e tarifas detalhados

Itinerários completos: segmentos, horários, número de voo, duração, paradas,
famílias tarifárias e impostos.

```bash
go run ./cmd/scraper -mode search \
  -origins LIS -destinations RIO -start 01-09-2026
```

```
ROTA     VOOS  PARTIDA      CHEGADA      DURAÇÃO  PARADAS  TARIFA   VALOR
LIS-GIG  TP71  01/09 18:55  02/09 00:50  9h55m    0        DISCINT  615.21 EUR
LIS-GIG  TP75  01/09 23:25  02/09 05:20  9h55m    0        DISCINT  615.21 EUR
```

**Exige perfil TLS da família Gecko ou WebKit** — o padrão já é `firefox_148`.
Perfis Chromium recebem 403 do WAF nesta rota; ver
[Impressão digital](#impressão-digital).

---

## Arquitetura

```
cmd/api/              servidor HTTP com OpenAPI embutido
cmd/scraper/          CLI: flags, wiring, tratamento de sinais
cmd/tlsprobe/         diagnóstico: qual JA3/JA4 cada perfil TLS produz
cmd/wafprobe/         diagnóstico: quais combinações atravessam o WAF
internal/config/      configuração e leitura de cookies
internal/client/      cliente TLS (tls-client), cookie jar, descompressão
  engine.go             identidade por motor: UA, client hints, ordem de headers
  profile_chrome151.go  perfil próprio que replica o JA4 do Chrome 151
internal/platform/    bootstrap: monta as dependências e as fecha
internal/collect/     caso de uso: coletar + persistir + orquestrar
internal/tap/         adapter da TAP: sessão, busca, calendário, transporte
internal/models/      structs da API, conversões, achatamento para análise
internal/storage/     postgres.go (tratado) · redis.go (bruto) · schema.sql
internal/api/         rotas, DTOs, tradução de erro -> status, OpenAPI
internal/report/      tabelas para leitura humana
```

**Fluxo de uma coleta** (`collect.Service`): verifica retomada no PostgreSQL →
requisição → grava o **bruto no Redis primeiro** (se o tratamento falhar, a
resposta não se perde nem exige nova requisição) → grava o tratado em **uma
transação**.

A política vive num único lugar, `collect.Service.persist`. O CLI e a API a
consomem; nenhum dos dois a repete.

**Concorrência:** `sourcegraph/conc/pool` com limite configurável e
`golang.org/x/time/rate` para o *rate limit*. Uma falha isolada não interrompe as
demais — cada resultado carrega seu próprio erro e o resumo consolida tudo.

**Retomada:** cada coleta tem uma chave canônica (rota + datas + cabine + mercado
+ passageiros). Com `-resume` (padrão), o que já está no banco **e ainda é
recente** é ignorado. Recente é `-resume-max-age`, por padrão **24 h**.

O prazo existe porque a chave do calendário não inclui data: sem ele, uma rota
coletada uma vez ficava marcada como pronta para sempre, e a segunda execução de
`./run.sh calendar` era um no-op permanente — com preços de meses atrás no banco e
nenhum sinal disso. Use `-resume-max-age 0` para o comportamento antigo, ou
`-resume=false` para recoletar agora.

### Dependências

| Pacote | Para quê |
|---|---|
| `bogdanfinn/tls-client` + `fhttp` | impressão digital TLS/HTTP2 de navegador |
| `jackc/pgx/v5` | PostgreSQL |
| `redis/go-redis/v9` | Redis |
| `sourcegraph/conc` | pools de goroutines |
| `golang.org/x/time/rate` | *rate limiting* |

O resto é biblioteca padrão — `encoding/json`, `log/slog`, `context`.

---

## Persistência

### PostgreSQL — dados tratados

O schema ([`internal/storage/schema.sql`](internal/storage/schema.sql)) é
aplicado automaticamente na inicialização.

| Tabela | Conteúdo |
|---|---|
| `calendar_prices` | melhor preço por rota / data / cabine / tipo de viagem |
| `calendar_return_prices` | preço total por combinação ida × volta, com `nights` |
| `searches` → `flights` → `segments` / `offers` | itinerários detalhados (modo `search`) |
| `airports`, `airlines` | dicionários vindos do bloco `translate` das respostas |

Reexecutar a mesma coleta **atualiza** os preços (upsert por chave), sem
duplicar — e atualiza também o que muda com eles: `arrival_airport` (que troca
quando o destino é código de cidade e `RIO` resolve para `GIG` ou `SDU`),
`resolved_dest` e `direct_flight`.

> **`adults` é coluna, não só parte da chave.** O total para 1 e para 2
> passageiros são séries distintas da mesma rota. Sem a coluna as duas coexistiam
> sem forma de distinguí-las, e uma agregação somava as duas — o mesmo erro que
> misturar EUR com BRL por ignorar `market`. As consultas de `queries.sql` quebram
> pelos dois.

> As tabelas guardam também as datas sem voo (`no_flights`, `sold_out`), cujo
> preço é `0` — filtre-as nas consultas. São informação útil: indicam
> indisponibilidade, não ausência de dado.

### Redis — respostas brutas

Chave `tap:raw:<chave-da-coleta>:<unix>`, TTL de 7 dias, mais um índice ordenado
`tap:raw:index:<chave>` para listar o histórico. A coluna `raw_key` das tabelas
aponta para a resposta que originou cada linha, fechando a rastreabilidade.

```bash
./run.sh redis    # lista as chaves
```

---

## Testes

```bash
./run.sh test                      # 185 offline: sem rede, sem banco
./run.sh up && ./run.sh test-int   # 11 contra PostgreSQL e Redis reais
./run.sh check                     # gofmt + vet + os dois conjuntos
```

**185 testes offline** mais **11 de integração** (a suíte offline roda em menos de 1 s), validados contra respostas reais:

| Fixture | Tamanho |
|---|---|
| `calendar_response.json` | 121 KB · 365 datas |
| `availability_search_response.json` | 279 KB · 34 voos, 105 ofertas |
| `calendar_returns_response.json` | 48 KB · 337 datas de retorno |
| `access_denied.html` | 92 KB · página 403 do WAF |

Cobrem desserialização, formatos de data, filtros de disponibilidade,
cruzamento oferta↔voo, detecção de bloqueio, perfis de cabeçalho por endpoint,
expiração de JWT e geração de combinações.

Cobertura por pacote, medida em 2026-08-04 (**56,4%** no agregado offline,
**66,4%** com integração):

| Pacote | Cobertura |
|---|---|
| `config` | 94,9% |
| `collect` | 94,1% |
| `client` | 95,1% |
| `models` | 83,7% |
| `report` | 80,4% |
| `api` | 78,6% |
| `tap` | 71,4% |
| `platform` | 70,3% |
| `storage` | 68,8% (integração) |

Os testes usam **dublês nas portas** — nada de rede, PostgreSQL ou Redis. Os de
integração ficam atrás da tag `integration` para preservar essa propriedade; eles
escrevem no mesmo banco da aplicação, marcam tudo com a rota sentinela `TST→XXX`
(mercado `ZZ`) e apagam ao final, para não poluir `./run.sh queries`.
Cobrem a ordem de gravação (Redis antes do PostgreSQL, porque o registro tratado
guarda a chave do bruto), a tradução de erro em status, a validação de entrada
que não deve chegar à TAP, e que um `GET` **não** coleta sem `refresh=true`.

Três deles usam `DisallowUnknownFields`: **falham se a API passar a devolver
campos que o modelo não representa** — servem de alarme para mudanças na API.

Alguns testes existem para fixar armadilhas que custaram falha real e que um
refactor bem-intencionado desfaria sem perceber:

- `calendarReturns` recebe `origin`/`destination` **invertidos** — descrevem a
  perna de volta. Na ordem natural devolve preços de outra rota sem erro algum.
- `calendar` recebe a data em DDMMYYYY; `calendarReturns`, em ISO.
- As duas rotas respondem **HTTP 200 com corpo vazio** quando rejeitam o payload.
- O perfil TLS `chrome_151` é o `Chrome_144` mais três algoritmos de assinatura
  pós-quânticos, **e nada mais**: o teste compara os dois specs byte a byte.
- A rotação de fingerprint tira a combinação **uma vez por requisição**, para que
  uma troca concorrente não misture o User-Agent de um perfil com o ClientHello de
  outro.
- O payload bruto ausente é **404**, não 500: com TTL de 7 dias e o registro
  tratado sem expiração, "existe a busca, não a captura" é o caso normal depois de
  uma semana.
- A retomada **tem prazo**. Sem ele, a segunda execução do modo `calendar` nunca
  coletaria nada.
- O pool distingue **bloqueio por volume** de recusa de identidade: no primeiro,
  rotacionar só queima as combinações por algo que passa sozinho.
- `adults` separa as séries de preço. Misturá-las devolvia duas linhas por data,
  com valores diferentes e nada que as distinguisse.

---

## Impressão digital

O WAF da Cloudflare protege `availability/search` e **discrimina por família de
motor**, não por qualidade do fingerprint TLS:

```
firefox_148      gecko      OK  34 voos · 105 ofertas · 615.21 EUR
safari_ios_18_5  webkit     OK  34 voos · 105 ofertas · 615.21 EUR
chrome_151       chromium   BLOQUEADO  WAF HTTP 403
```

> **Cookies não mudam isso.** `firefox_148` passa **sem cookie algum**, e
> `chrome_151` é recusado mesmo com `cf_clearance` válido no jar — medido nos dois
> sentidos. Quem decide é o motor.
>
> Há também um **bloqueio temporário por volume** que atinge todos os perfis
> depois de uma sequência de coletas — observado durando de 16 a 24 minutos. Nesse
> caso rotacionar não resolve, e o código sabe disso: o pool suspende a rotação ao
> ver três perfis distintos recusados em um minuto, e o `wafprobe` mede um perfil de
> controle antes de tudo e **se recusa a medir** se ele for bloqueado, em vez de
> imprimir uma tabela que não quer dizer nada. Ver [`CLAUDE.md`](CLAUDE.md) §4.

Chromium é recusado **mesmo com o JA4 idêntico ao do Chrome real**. A razão: com
`tls-client` o ClientHello é fiel, mas o User-Agent e os client hints são
montados à mão — e o Chromium anuncia quatro cabeçalhos de identidade
(`sec-ch-ua`, `sec-ch-ua-mobile`, `sec-ch-ua-platform`, `priority`) onde o Gecko
não anuncia nenhum. Menos superfície, menos divergência.

Por isso o padrão é `firefox_148`, e a coerência perfil↔motor é imposta por
construção: `Config` não tem campos de User-Agent — eles vêm de
`client.EngineFor(TLSProfile)`.

### Rotação automática

Um 403 não encerra mais a coleta: o adapter troca de combinação e continua. Ligado
por padrão, `-rotate=false` desliga.

```bash
go run ./cmd/scraper -mode search -origins LIS -destinations RIO \
  -tls-profile chrome_151 -resume=false -proxy ""

WARN  combinação bloqueada, rotacionando  de=chrome_151 para=firefox_148 cooldown=10m0s
INFO  busca persistido  ofertas=108 voos=34
```

O `-tls-profile` passa a significar "por onde começar". A combinação recusada fica
de fora por 10 minutos, dobrando a cada bloqueio repetido — e o pool só inclui os
perfis medidos como aprovados, porque pôr um Chromium na rotação gastaria uma
requisição para descobrir o que já se sabe.

Para medir combinações:

```bash
go run ./cmd/wafprobe                 # quais atravessam o WAF
go run ./cmd/wafprobe -control ""     # sem a checagem de janela
go run ./cmd/tlsprobe                 # qual JA3/JA4 cada perfil produz
```

Os perfis medidos, o que passa em cada rota e por quê estão em
[`CLAUDE.md`](CLAUDE.md) §4 — inclusive a distinção entre o bloqueio por motor,
que é permanente, e o bloqueio por volume, que passa sozinho.

### Horários são hora local, não UTC

A API devolve `2026-09-01T23:30:00.000Z`, mas o valor é a **hora de parede do
aeroporto** — o `Z` mente. As colunas são `TIMESTAMP` sem fuso e a duração vem
sempre do campo `duration`: subtrair os timestamps daria 380 minutos onde o voo
leva 620. Ver [`CLAUDE.md`](CLAUDE.md) §3.

---

## Desenvolvimento

### Organização

A estrutura passou por 6 etapas de refatoração, cada uma deixando o projeto
compilando. **As 6 estão aplicadas:**

- a política de persistência vive só em `internal/collect`;
- o adapter foi fatiado em oito arquivos e renomeado para `internal/tap`;
- `internal/api` não importa mais o adapter;
- `cmd/wafprobe` mede com o adapter real, sem cópia das definições de motor;
- `internal/platform.Bootstrap` monta e fecha as dependências dos dois comandos;
- cobertura de 23,4% para **56,2%** offline (**66,0%** com integração).

[`CLAUDE.md`](CLAUDE.md) documenta a engenharia reversa: endpoints, formatos de
payload, armadilhas de tipagem já pagas (valores monetários que chegam como
`0.0`, datas que não são RFC3339, campos invertidos) e as medições de impressão
digital. **Leia antes de mexer nos modelos** — várias decisões que parecem
arbitrárias vieram de erro real.

Para comparar perfis TLS:

```bash
go run ./cmd/tlsprobe -profiles chrome_144,chrome_146,chrome_151
```

Sonda um recurso estático (não passa pelo WAF) marcando cada requisição com
`?probe=<perfil>`, para casar entrada e perfil na captura.

### Convenções

- Erros sempre embrulhados com `fmt.Errorf("failed to ...: %w", err)`; sentinelas
  comparadas com `errors.Is`.
- `context.Context` como primeiro parâmetro de tudo que faz I/O.
- Concorrência via `conc/pool` — nunca `sync.WaitGroup` manual.
- Comentários explicam **por quê**, não o quê.
- `cookies.txt` está no `.gitignore`: contém `cf_clearance` atrelado ao IP.
