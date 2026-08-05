# airtravel — scraper da API interna da TAP

Scraper por requisição da TAP Air Portugal (`booking.flytap.com`), em Go 1.25+
(exigido pelo `pgx/v5`) com fingerprint TLS de navegador. Exercício da **Semana 5** do programa trainee
(trilha Crawler/RPA + IA): engenharia reversa de API interna, gestão de sessão
(cookies, headers, tokens) e persistência em dois destinos.

Persistência exigida pelo desafio: **PostgreSQL** para dados tratados, **Redis**
para a resposta bruta.

---

## 1. Como rodar

**Não é preciso preparar nada além de subir os serviços.** Verificou-se que
**as três rotas respondem 200 sem cookie algum**, o modo `search` incluído —
quem decide ali é o motor do perfil TLS, não o cookie (§4). O `cookies.txt` é
opcional; a flag existe porque a hipótese do cookie foi testada e descartada.

```bash
./run.sh demo      # up + test + calendar + returns + search + queries
```

Ou passo a passo (`./run.sh help` lista tudo; há um `Makefile` equivalente para
quem tem `make` — o WSL costuma não trazer):

```bash
./run.sh up          # PostgreSQL 17 + Redis 7, espera ficarem saudáveis
./run.sh test        # 185 testes contra as fixtures, offline
./run.sh calendar    # melhor preço por data de partida
./run.sh returns     # matriz ida x volta
./run.sh queries     # o que foi coletado, via SQL (queries.sql)
./run.sh redis       # chaves das respostas brutas
```

Parametrize por variável de ambiente:

```bash
ORIGINS=OPO DESTINATIONS=GRU FROM=01-12-2026 TO=31-01-2027 ./run.sh calendar
```

Para inspecionar o tráfego no powhttp, aponte o proxy:

```bash
PROXY=http://localhost:8888 ./run.sh calendar
```

Chamada direta, se preferir:

```bash
go run ./cmd/scraper -mode calendar -origins LIS -destinations RIO \
  -trip-type R -from 01-09-2026 -to 31-10-2026 -tls-profile chrome_151
```

Flags principais: **`-mode`** (`calendar` padrão, `returns` ou `search` — §4),
`-origins`, `-destinations`, `-start` (DD-MM-AAAA), `-days`, `-cabins` (E/W/C),
`-adults`, **`-trip-type`** (O/R), `-trip-duration`, **`-from`/`-to`** (recorte
da saída, DD-MM-AAAA), `-concurrency`, `-rps`, `-resume`,
**`-resume-max-age`** (§5), `-tls-profile` (preferido), **`-rotate`** (padrão
ligado, §4), `-top`, `-debug`.

**`-proxy` é vazio por padrão** e lê a variável `PROXY`. O padrão era
`http://localhost:8888`, herdado do powhttp, o que fazia qualquer invocação
direta falhar em máquina sem ele de pé — inclusive os exemplos desta
documentação. Para inspecionar o tráfego, ligue-o de volta.

A API tem flags próprias: `-addr`, `-timeout`, `-rotate`, `-rps`, `-cookies` e
`-debug`, além de `-market`, `-language`, `-tls-profile` e `-proxy`. As que
importam para o wiring leem variável de ambiente equivalente (`API_ADDR`,
`MARKET`, `LANGUAGE`, `TLS_PROFILE`, `PROXY`, `COOKIES_FILE`).

No modo `calendar` as datas do plano são ignoradas (`dedupeRoutes`): a API
devolve o ano inteiro, então basta uma requisição por rota/cabine.

Variáveis de ambiente: `POSTGRES_DSN`, `REDIS_ADDR`, `REDIS_PASSWORD`.

---

## 2. A API da TAP (engenharia reversa concluída)

Base: `https://booking.flytap.com`, prefixo `/bfm/rest/`. Tudo em HTTP/2.

> Esta seção é a API **da TAP**, que consumimos. A nossa API HTTP, que expõe
> isto, está em §6.

### Autenticação — o ponto que mais custou

| Endpoint | Papel |
|---|---|
| `POST /bfm/rest/session/create` | **emite o primeiro JWT** ← usar este |
| `POST /bfm/rest/session/resetValues` | apenas *reinicia* sessão existente; **HTTP 400 sem Bearer** |
| `POST /bfm/rest/session/renew` | renovação |
| `POST /bfm/rest/session/extendSession` | extensão |

O JWT chega no campo **`id`** (não em `token`) e vale ~5 h
(`ROLE_ANONYMOUS_USER`). Usar como `Authorization: Bearer <id>`.

Credenciais **públicas e estáticas**, extraídas do bundle `main.js` da SPA
(objeto `y`) e conferidas byte a byte contra o tráfego do navegador — estão em
`internal/config/config.go`:

```
clientId     = "-bqBinBiHz4Yg+87BN+PU3TaXUWyRrn1T/iV/LjxgeSA="
clientSecret = "DxKLkFeWzANc4JSIIarjoPSr6M+cXv1r..."
referralId   = "h7g+cmbKWJ3XmZajrMhyUpp9.cms35"
```

> Como esta descoberta foi feita: `curl https://booking.flytap.com/main.js`
> (6,5 MB; assets estáticos **não** são bloqueados) e grep pelas constantes
> `x+"session/..."`. É o caminho para achar qualquer endpoint novo.

### Calendário de preços — o caminho de coleta que funciona

`POST /bfm/rest/booking/availability/calendar/`

A rota aceita **duas formas de corpo**, ambas verificadas. Usamos a primeira.

**1. Ancorada em data — um ano por requisição (é a que o código usa):**

```json
{"origin":"LIS","destination":"RIO","departureDate":"01092026",
 "tripType":"R","market":"PT","language":"pt","adt":1,"cabinClass":"E"}
```

→ 356–365 datas, ~121 KB. Ver `models.CalendarRequest`.

**2. Canônica do navegador — um mês por requisição:**

```json
{"cabinClass":"E","destination":"RIO","market":"PT","origin":"LIS",
 "tripType":"R","paxType":"ADT","month":9,"year":2026}
```

→ 30–31 datas, ~10 KB. Note `paxType:"ADT"` em vez de `adt:1`, `month`/`year`
inteiros em vez de `departureDate`, e ausência de `language`.

A forma 1 cobre um ano em **1** requisição contra **12** da forma 2, por isso é a
escolhida. Como a forma 2 é a que o frontend realmente envia, ela é a que tem
menor risco de desaparecer — está registrada aqui para servir de alternativa.

**`tripType` muda o preço, não só o formato:**

| tripType | significado | LIS→RIO, set–out/2026 |
|---|---|---|
| `O` | só ida | menor 615,21 € · média 643,84 € |
| `R` | **ida e volta** | menor **487,21 €** · média 552,15 € |

Com `R` a API devolve a **tarifa de ida e volta**, mais barata que a soma de duas
de só ida — nunca somar duas pernas `O` para estimar um round-trip. A data de
retorno não é enviada: o calendário dá o melhor preço de ida e volta por data de
*partida*. Controlado por `-trip-type` (ou derivado de `-trip-duration`).

**Atenção:** `origin`/`destination`/`departureDate` são **escalares** aqui, ao
contrário da busca, onde são arrays. Enviar arrays devolve **HTTP 200 com corpo
vazio** — daí a checagem explícita de `len(raw) == 0` em `Scraper.Calendar`.

Resposta: sem o envelope `status`/`errors` — apenas `data.bestPriceForDates[]` e
`translate`. Cada item traz `bestTotalPrice`, `departureAirport`/
`arrivalAirport`, `currency`, `monthlyMinimum`, `soldOut`, `noFlights`.

### Matriz ida × volta

`POST /bfm/rest/booking/availability/calendarReturns/`

```json
{"cabinClass":"E","destination":"LIS","market":"PT","origin":"RIO",
 "tripType":"R","departureDate":"2026-09-01","paxType":"ADT"}
```

Exatos 127 bytes; ver `models.CalendarReturnsRequest`. **Duas armadilhas**, ambas
fixadas em `TestReturnsRequestShape`:

1. **`origin`/`destination` descrevem a perna de VOLTA** — portanto invertidos
   em relação ao sentido da viagem. Uma viagem LIS→RIO envia `origin:"RIO"`,
   `destination:"LIS"`. `Scraper.CalendarReturns` faz a inversão, então o
   chamador pensa no sentido da viagem.
2. **`departureDate` é a data da IDA e usa ISO** (`2026-09-01`), não o DDMMYYYY
   do resto da API. Constante `models.ISODateLayout`.

Resposta: `data.returns[]` com `returnDate` e **`price` = total da viagem**, mais
`data.origin`/`data.destination` no sentido da viagem e com o destino **resolvido
para o aeroporto concreto** (`RIO` → `GIG`). ~337 datas de retorno por data de
ida, ~48 KB.

Uma requisição por data de ida; cada uma cobre todas as voltas possíveis.
A coluna `nights` é derivada na gravação para servir de eixo de análise
("a viagem de 7 noites mais barata").

O `Referer` desta rota é **`/booking/dates`**, não `/booking/flights`: é o painel
de datas que a dispara. Sem entrada própria em `headerProfiles` ela caía no padrão
e anunciava uma página que o usuário não teria visitado ainda.

> Descoberto na captura, não por sondagem: as tentativas de adivinhar pelo
> tamanho de 127 bytes falharam (todas HTTP 400) porque a inversão de
> origem/destino e o formato ISO não eram dedutíveis. O gatilho no navegador é o
> painel de datas de ida e volta (`/booking/dates`).

### Busca de disponibilidade (atravessa o WAF com Gecko — ver §4)

`POST /bfm/rest/booking/availability/search?payWithMiles=false&starAlliance=false`

Corpo: 36 campos, ver `models.SearchRequest`. A **ordem de declaração dos
campos do struct reproduz a ordem das chaves do JSON do navegador** — não
reordenar. Particularidades fiéis à captura:

- `cmsId` e `session` são os literais `"string"`;
- datas em **`DDMMYYYY`** (ex.: `01092026`);
- só-ida envia `returnDate == departureDate`, `oneWay:true`, `tripType:"O"`;
- `origin`/`destination` são arrays; aceitam código de cidade (`RIO`) além de
  aeroporto (`GRU`, `SDU`).

### Mercado define moeda E tarifas

`market` não é escolha de moeda: é o conjunto tarifário. LIS→RIO ida e volta,
economy, medido no mesmo dia:

| market | moeda | menor | datas com voo | data mais barata |
|---|---|---|---|---|
| `PT` | EUR | 487,21 | 355 | 18/08/2026 |
| `BR` | BRL | 2.477,49 | 345 | 23/02/2027 |

A ~6,26 BRL/EUR, 487,21 € equivaleriam a ~3.050 BRL; o mercado BR cobra 2.477,49
— **~19% mais barato em termos reais**. O inventário difere (345 contra 355
datas) e o mínimo cai em outro mês. O `officeId` também muda.

Consequência para o armazenamento: `market` faz parte da chave de deduplicação,
então os dois mercados coexistem sem se sobrescrever. Consultas que comparam
preço **precisam filtrar por `market`** — misturar EUR e BRL na mesma agregação
produz número sem sentido.

### Resposta

Envelope: `{"status":"200","errors":[],"data":{…},"translate":{…}}`.
**`status` é string, não número.**

| Caminho | Conteúdo |
|---|---|
| `data.listOutbound[]` | itinerários de ida (34 na amostra) |
| `data.listOutbound[].listSegment[]` | trechos, com `carrier`+`flightNumber`, datas RFC3339 |
| `data.offers.listOffers[]` | ofertas/tarifas (105 na amostra) |
| `data.offers.listOffers[].groupFlights[].idOutBound` | **liga oferta → `listOutbound[].idFlight`** |
| `data.outPanel.listTab[]` | calendário de datas vizinhas com preço mínimo |
| `data.offerMatrix.listTab[]` | matriz ida×volta (`offerBean` tem a forma de `Offer`) |
| `translate.locations` / `.airlines` | dicionários por código IATA |

### Outros endpoints mapeados (do `main.js`)

`booking/availability/searchMoreFlights` (**paginação real da busca**),
`booking/availability/calendar/`, `calendarReturns/`,
`booking/availability/calendar/prices/lowest/monthly`,
`booking/availability/select`, `booking/flights/info/retrieve`,
`journey/origin/search`, `journey/destination/search`,
`geography/airports`, `geography/continents`, `search/pax/types`.

---

## 3. Tipagem — armadilhas já pagas

Estas decisões vieram de erro real, não de palpite. **Não "simplificar".**

1. **`status` é `string`** (`"200"`).
2. **Valores monetários são `float64`, mesmo zerados.** `ccFee`, `obFee` e
   `sliderDiscount` chegam como **`0.0`** no fio. Declará-los `int` produz
   `json: cannot unmarshal number 0.0 into ... of type int`. Medido na sintaxe
   JSON: 462 ocorrências de cada campo, todas decimais.
   Já `miles` e `minFareInPoints` são inteiros puros (são contagens).
3. **`powhttp_infer_schema` reporta "integer" para `0.0`** — ele olha o valor,
   não a sintaxe. Não confiar nele para int vs float; conferir com o teste.
4. **Os horários de voo são hora LOCAL do aeroporto — o sufixo `Z` mente.**
   A API devolve `2026-09-01T23:30:00.000Z` e o valor é 23:30 em Lisboa, não em
   UTC. Use `models.ParseWallClock`, nunca `time.RFC3339`, e colunas `TIMESTAMP`
   sem fuso. **Nunca subtraia dois timestamps para obter duração** — use o campo
   `duration`.

   Duas provas independentes, do TP87 LIS→GRU: a própria SPA lê esse campo e
   envia `departureTime "23:30:00"` ao `flights/info/retrieve`; e partida 23:30 →
   chegada 05:50 dariam 380 min se ambos fossem UTC, mas `duration` informa 620
   — que é o valor correto tratando como local (LIS UTC+1 = 22:30Z, GRU UTC−3 =
   08:50Z). Fixado em `TestDurationComesFromAPI`.
5. **As datas do calendário NÃO são RFC3339.** Vêm como
   `2026-08-03T00:00:00`, sem fuso — usar `models.CalendarDateLayout`
   (`2006-01-02T15:04:05`). Um `time.Parse(time.RFC3339, ...)` falha; há teste
   que garante isso.
6. `departureTerminal`/`arrivalTerminal` são `*string` (união `null|string`).
7. `errors` vem `[]` num endpoint e `null` noutro → `json.RawMessage`.
8. Campos observados só como `null` usam `json.RawMessage`: preserva o valor e
   não arrisca erro de unmarshal quando a API passar a preenchê-los.

`TestNoUnknownFields` usa `DisallowUnknownFields` e **passa** — o modelo cobre
100% dos campos da resposta real. Se falhar, a API mudou ou há informação nova
para aproveitar.

---

## 4. Stealth — o que foi medido

Baseline: Chrome 151 real, JA4 `t13d1516h2_8daaf6152771_806a8c22fdea`.

**O perfil `chrome_151` (nosso, em `internal/client/profile_chrome151.go`)
reproduz esse JA4 exatamente — verificado.** Use-o: `-tls-profile chrome_151`.

Medições por perfil (via `cmd/tlsprobe`, sondando um SVG estático):

| perfil | JA4 |
|---|---|
| `chrome_133`, `_psk` | `t13d1516h3_8daaf6152771_d8a2da3f94cd` (ALPN h3) |
| `chrome_144`, `_psk` | `t13d1516h2_8daaf6152771_d8a2da3f94cd` |
| `chrome_146`, `_psk` | `t13d1517h2_8daaf6152771_dcad5a053991` |
| **`chrome_151` (nosso)** | **`t13d1516h2_8daaf6152771_806a8c22fdea`** ✅ |
| Chrome 151 real | `t13d1516h2_8daaf6152771_806a8c22fdea` |

- `chrome_151` = `Chrome_144` + os três algoritmos de assinatura **ML-DSA**
  (`0x0904/0905/0906`) que o Chrome 151 anuncia e nenhum perfil do tls-client
  tem. Era a única divergência restante no ClientHello.
- **`chrome_146` erra por uma extensão a mais:** `0xca34`
  (`TLSEXT_TYPE_trust_anchors`), que o tls-client inclui e o Chrome 151 ainda
  não envia. Ela vem **do próprio perfil**, não de
  `WithRandomTLSExtensionOrder()` — desligar aquela opção não a remove.
- `WithRandomTLSExtensionOrder()` fica desligado porque o Chrome não embaralha
  as extensões; ligável via `client.Options.RandomExtensionOrder`.
- **`WithDisableHttp3` existe** e está aplicado. Um inventário anterior concluiu
  que não existia, por um regex que descartava nomes terminados em dígito — o
  erro estava na ferramenta de inventário, não na biblioteca.
- **Fingerprint HTTP/2 confere byte a byte** com o Chrome 151:
  `1:65536; 2:0; 4:6291456; 6:262144` + `WINDOW_UPDATE 15663105`.

> Antes de mexer em perfis ou cabeçalhos, refaça a medição: `cmd/tlsprobe` mede o
> ClientHello contra um asset estático e `cmd/wafprobe` mede quais combinações
> trazem voos. Toda tabela desta seção saiu de uma dessas duas ferramentas, e
> nenhuma conclusão aqui vale mais que a última medição.

### `cmd/tlsprobe`

```bash
go run ./cmd/tlsprobe -profiles chrome_144,chrome_151
# depois: powhttp_search_entries(filters={url_contains: "probe="}, include_details=true)
```

Sonda um asset estático (não passa pelo WAF), marcando cada requisição com
`?probe=<perfil>` para casar entrada e perfil na captura.

`cmd/wafprobe` é a outra ferramenta: monta o adapter real com cada perfil e reporta
quais trazem voos. Use `./run.sh wafprobe`.
- **Descompressão é manual:** `Accept-Encoding` é definido à mão, então o fhttp
  não descomprime. `client.DecompressBody` replica o que o wrapper CFFI do
  tls-client faz, com guarda `if !resp.Uncompressed`.
- **O conjunto de headers varia por endpoint.** `x-dtreferer` e `x-dtpc`
  (Dynatrace) aparecem nas chamadas da página de resultados, **mas não** em
  `session/create`. `Referer` é `/booking` ali e `/booking/flights` na busca.
  Ver `headerProfiles` em `internal/tap/endpoints.go`.
- Ordem de pseudo-headers: `:method, :authority, :scheme, :path`.
- `HeaderOrderKey` inclui `content-length` e `cookie` embora não sejam definidos
  à mão — é a **posição relativa** que compõe a impressão digital.

### O WAF de `availability/search` — resolvido com Gecko

`POST /bfm/rest/booking/availability/search` responde **200 com perfis Firefox e
Safari** e **403 com qualquer perfil Chromium**. Medido por `cmd/wafprobe`:

```
firefox_148      gecko      OK  34 voos · 105 ofertas · 615.21 EUR · 3207 ms
firefox_147      gecko      OK  34 voos · 105 ofertas · 615.21 EUR
firefox_135      gecko      OK  34 voos · 105 ofertas · 615.21 EUR
safari_ios_18_5  webkit     OK  34 voos · 105 ofertas · 615.21 EUR
chrome_151       chromium   BLOQUEADO  WAF HTTP 403
chrome_146       chromium   BLOQUEADO  WAF HTTP 403
```

Sem cookie nenhum. Por isso `DefaultTLSProfile = "firefox_148"`.

**Por que o Chromium falha mesmo com JA4 idêntico ao do Chrome real:** o
tls-client reproduz o ClientHello, mas o User-Agent e os client hints são
montados à mão. O Chromium tem muito mais superfície para divergir — anuncia
`sec-ch-ua`, `sec-ch-ua-mobile`, `sec-ch-ua-platform` e `priority`, cada um com
formato próprio. O Gecko **não anuncia client hint nenhum**: não há o que errar.

> **Erro de análise a não repetir.** A conclusão anterior aqui era "não é
> fingerprint", derivada de testar `chrome_133/144/146/151` e obter 403 em todos,
> inclusive com JA4 idêntico ao do navegador. A conclusão correta era "paridade
> de fingerprint *Chrome* não basta". Generalizar de uma família de motores para
> todas custou várias horas e quase eliminou o modo `search` do projeto.
> A hipótese Gecko veio de outra implementação do mesmo desafio.

A coerência perfil↔motor está em `internal/client/engine.go` e é imposta por
construção: `config.Config` **não** tem campos de User-Agent ou client hints —
eles são derivados de `TLSProfile` por `client.EngineFor`, para que não seja
possível configurar uma combinação incoerente.

### Rotação de fingerprint — a diferença entre degradar e parar

`internal/client/pool.go`. Com uma combinação só, um 403 em `firefox_148` encerra
a coleta: o perfil é escolhido no boot e não há para onde ir. O pool mantém várias
e troca quando o WAF recusa.

Verificado ao vivo, começando de propósito por um perfil bloqueado:

```
$ go run ./cmd/scraper -mode search -origins LIS -destinations RIO \
    -tls-profile chrome_151 -rotate -resume=false -proxy ""

WARN  combinação bloqueada, rotacionando  de=chrome_151 para=firefox_148
      cooldown=10m0s motivo="...HTTP 403 em /bfm/rest/booking/availability/search"
INFO  busca persistido  ofertas=108 voos=34
Buscas: 1 total | 1 concluídas | 0 falhas | 108 ofertas coletadas
```

Antes desta mudança, esse comando falhava.

**A combinação é tirada UMA vez por requisição** (`fp := s.fps.Current()` em
`Scraper.do`) e motor e cliente viajam juntos. Ler o motor de um campo e o cliente
de outro permitiria que uma rotação concorrente casasse o User-Agent de um perfil
com o ClientHello de outro — exatamente a incoerência que fez o WAF recusar os
perfis Chromium. `client.Fingerprint` existe para tornar isso estrutural.

**Perfis Chromium ficam FORA do pool** (`client.PassingProfiles`). Foram medidos
como bloqueados nessa rota: incluí-los gastaria uma requisição e um 403 para
descobrir o que já se sabe. Ainda são usáveis via `-tls-profile`, que só define por
onde começar.

**Dois orçamentos separados** em `doWithRetry`: repetir resolve falha de rede,
rotacionar resolve recusa de identidade. Se o bloqueio consumisse o orçamento de
retry, um pool de quatro combinações nunca seria percorrido com `MaxRetries=3`. E a
rotação é **imediata** — não é falha transitória, esperar não muda nada.

**Relatos repetidos custam uma rotação.** Numa coleta concorrente várias goroutines
tomam 403 com a mesma combinação quase ao mesmo tempo; `Pool.Blocked` confere o
nome do perfil contra o corrente, senão oito relatos queimariam o pool inteiro por
um perfil ruim.

Duas decisões que **não** são medições:

- **O cooldown de 10 min é escolha.** Não se sabe por quanto tempo o WAF da TAP
  lembra de um fingerprint recusado. Dobra a cada bloqueio repetido do mesmo
  perfil. Ajustável por `RotationCooldown`.
- **Quando a preferida sai do cooldown, o pool não volta para ela.** Trocar de
  identidade no meio de uma coleta que funciona é estranho de se observar do outro
  lado, e `-tls-profile` serve para escolher por onde começar, não para ser
  restaurado a cada dez minutos. Fixado em `TestPoolStaysOnAWorkingProfile`.

Desligue com `-rotate=false`. **O `cmd/wafprobe` desliga obrigatoriamente**: com a
rotação ligada, um 403 em `chrome_151` faria o adapter trocar para `firefox_148` e
a tentativa sairia como aprovada — a ferramenta mediria o pool, não o perfil.

Mapa de cobertura do WAF (mesmo corpo em todas as rotas), reconfirmado em
2026-08-04:

| rota | Chromium | Gecko |
|---|---|---|
| `booking/availability/search` | 🔴 403 | ✅ 200 |
| `booking/availability/calendar/` | ✅ 200 | ✅ 200 |
| `booking/availability/calendarReturns/` | ✅ 200 | ✅ 200 |
| `session/create`, `search/pax/types`, `journey/stopover/search` | ✅ 200 | ✅ 200 |

`retrieveMatrix` é inviável isoladamente: o navegador o chama com **corpo vazio**,
dependendo do estado de sessão criado pela busca.

### Bloqueio transitório por volume — a armadilha de medição de 04/08

Depois de uma sequência de coletas (~8 requisições em poucos minutos, entre
`calendar`, `returns` e `search`), **todos os seis perfis passaram a tomar 403 na
rota `search`** — Gecko e WebKit incluídos. Minutos depois, sem mudar nada,
`firefox_148` voltou a trazer 34 voos.

Medido nas duas direções no mesmo IP e na mesma sessão:

| momento | combinação | cookies | resultado |
|---|---|---|---|
| 15:47 | `firefox_148` → 147 → 135 → safari (rotação) | sim | 🔴 os quatro, 403 |
| 15:55 | os seis, um a um (`wafprobe`) | **não** | 🔴 os seis, 403 · ~1585 ms cada |
| 16:11 | `chrome_151` | sim | 🔴 403 |
| 16:11 | `firefox_148` | sim | ✅ 34 voos · 106 ofertas |
| 16:13 | `firefox_148` (rotação) | sim | ✅ 34 voos · 106 ofertas |
| 16:14 | `firefox_148` (`wafprobe`) | **não** | ✅ 34 voos · 106 ofertas |

Três conclusões, e a terceira é sobre método:

1. **O cookie não é o fator.** `firefox_148` passa **sem cookie algum**
   (`wafprobe` nunca os carrega — usa `config.Default()` e não chama
   `LoadCookiesFile`), e `chrome_151` é recusado **com** `cf_clearance` válido no
   jar. Quem decide é o motor, como o resto desta seção descreve.
2. **O bloqueio por volume é temporário e atinge todos os perfis.** Não confundir
   com o bloqueio por motor: este é permanente para Chromium e independe de
   quantas requisições foram feitas. A latência uniforme de ~1585 ms nos seis (vs.
   ~4700 ms de uma busca que passa) é o sinal — recusa antes do GDS.
3. **`cmd/wafprobe` dava falso negativo se rodado dentro dessa janela.** Ele mediu
   "os seis bloqueados" e a leitura imediata — "o WAF endureceu, nenhum perfil
   passa" — estava errada; a certa era "estou dentro de um bloqueio temporário".
   É o mesmo erro de método registrado logo acima, na outra direção: generalizar de
   uma medição pontual para uma regra.

### As duas defesas que esse erro gerou

**O pool distingue os dois bloqueios** (`client.Pool`). Três perfis DISTINTOS
recusados dentro de um minuto deixam de ser lidos como recusa de identidade:
`Blocked` passa a devolver `false`, a rotação é suspensa e o log diz para esperar.
Antes disso a coleta queimava as quatro combinações em segundos e mandava todas
para o cooldown por causa de algo que passa sozinho.

Duas escolhas dentro dessa detecção:

- **A contagem é por perfil distinto, não por relato.** Numa coleta concorrente o
  mesmo perfil é reportado várias vezes; contar relatos desfaria a proteção de
  `Pool.Blocked` contra relatos repetidos.
- **O cooldown aplicado é o base, sem a progressão.** O perfil não é culpado pelo
  bloqueio por volume, e inflá-lo o puniria por estar em uso na hora errada.

`Pool.GlobalBlockSuspected()` expõe o diagnóstico, e `Scraper.blockError` o usa para
escolher a mensagem: "esgotadas as combinações" manda arranjar outro fingerprint,
"parece bloqueio por volume" manda esperar. São ações opostas, e a mensagem errada
manda procurar no lugar errado. Fixado em `TestPoolDetectsGlobalBlock` e
`TestGlobalBlockSaysToWaitNotToRotate`.

**O `wafprobe` mede um controle antes e depois** (`-control`, padrão
`firefox_148`). Se o perfil de referência é recusado, o comando **não mede**: sai
com erro explicando que a janela está fechada, em vez de imprimir uma tabela de
"todos bloqueados" que não quer dizer nada. Se o controle passa no início e falha no
fim, a tabela sai com aviso de que o bloqueio começou no meio. `-force` mede
mesmo assim; `-control ""` desliga.

Verificado no cenário real, com o IP bloqueado:

```
controle (firefox_148): BLOQUEADO

o perfil de referência "firefox_148" foi recusado, então o WAF está recusando tudo
agora — provavelmente bloqueio por VOLUME, que é temporário e independe de
fingerprint.
```

Custou zero requisição de perfil: o comando recusou-se a gastar as três que a
tabela pediria.

`ErrCloudflareChallenge` é outra coisa (página "Just a moment"): aí sim
recoletar `cf_clearance`. A detecção de "ACCESS DENIED" vem **antes** porque o
beacon da CF em toda página do site contém `challenge-platform` e causava falso
positivo.

---

## 5. Arquitetura

```
cmd/api/main.go            servidor HTTP: flags, wiring, sinais
cmd/scraper/main.go        CLI: flags, wiring, sinais
internal/config            config + leitura de cookies.txt
internal/client            tls-client, cookie jar, descompressão
  pool.go       rotação de fingerprint com cooldown
internal/platform          bootstrap: monta as dependências e as fecha
                           (BootstrapAdapter monta só o caminho até a TAP)
internal/collect           CASO DE USO: coletar + persistir + orquestrar
internal/tap               adapter da TAP, um arquivo por responsabilidade:
  tap.go        o tipo e o construtor
  endpoints.go  paths e cabeçalhos por endpoint
  errors.go     sentinelas e classificação das respostas
  session.go    JWT: obtenção e renovação
  search.go     busca de disponibilidade e aquecimento
  calendar.go   calendário e matriz ida x volta
  transport.go  execução, retry, montagem de cabeçalhos
  dynatrace.go  identificadores de telemetria
internal/models            structs da API + achatamento (FlattenOffers)
internal/storage           postgres.go (tratado), redis.go (bruto), schema.sql
internal/api               rotas, DTOs, query.go (binder), erro->status, openapi.yaml
internal/report            tabela de voos para leitura humana
```

**Fluxo por rota, modo calendar** (`collect.Service.Calendar`): checa retomada em
`calendar_prices` → `Scraper.Calendar` → grava **bruto no Redis primeiro** →
grava as 365 datas no PostgreSQL em **uma transação** (upsert por
`calendar_key, departure_date`, então recoletar atualiza os preços).

**Fluxo por busca, modo search** (`collect.Service.Search`): checa retomada no PostgreSQL → `Search`
→ grava **bruto no Redis primeiro** (se o tratamento falhar, não se perde a
resposta nem se repete a requisição) → grava tratado no PostgreSQL em **uma
transação**.

**Paginação:** a API não é paginada por página/cursor. A iteração é o produto
cartesiano rota × data × cabine (`scraper.Plan.Expand`), concorrente via
`sourcegraph/conc/pool`, com `rate.Limiter` e retomada por `search_key`. Uma
falha isolada não derruba as demais — cada `JobResult` carrega seu erro.
`scraper.CalendarDates` extrai do `outPanel` as datas com voo, para descobrir
barato o que vale uma busca completa.
Para aprofundar, há `booking/availability/searchMoreFlights` (não implementado).

### Persistência

PostgreSQL (`internal/storage/schema.sql`, aplicado na inicialização):

- **`calendar_prices`** — o preço mínimo por rota/data/cabine (modo `calendar`).
  Chave `(calendar_key, departure_date)`. Guarda também as datas sem voo
  (`no_flights`/`sold_out`), então filtre-as nas consultas — o preço delas é 0.
  As consultas de `queries.go` já as descartam.
- **`calendar_return_prices`** — o preço total por combinação ida × volta, com
  `nights` derivado na gravação (modo `returns`). Chave
  `(returns_key, return_date)`.
- `searches` → `flights` → `segments` / `offers` (CASCADE), do modo `search`.
- `airports` e `airlines`, vindos de `translate` em ambos os modos. Rerodar a mesma busca substitui os dados
(upsert por `search_key`); `searches.raw_key` referencia a chave no Redis.

**`adults` é coluna das duas tabelas de calendário, não só da chave.** O total
para 1 e para 2 passageiros são valores diferentes para a mesma data, coletados e
guardados em separado. Sem a coluna as duas séries coexistiam sem forma de
distinguí-las: `GET /api/v1/calendar` devolvia duas linhas por data, e o
`cheapest` saía sempre da série de menos passageiros. É a mesma armadilha do
`market` (§2) numa dimensão que passou batido — e as agregações de `queries.sql`
agora quebram pelos dois.

**O `ON CONFLICT` atualiza tudo que a TAP pode mudar entre coletas**, não só o
preço. `arrival_airport` muda de verdade: quando o destino é código de cidade,
`RIO` resolve para `GIG` ou `SDU` conforme a data. Atualizar só o preço deixava o
aeroporto congelado no da primeira coleta. Idem `resolved_dest` e `direct_flight`
na matriz.

### Retomada tem prazo

`-resume` (padrão ligado) ignora o que já foi coletado — mas só enquanto for
**recente**. `-resume-max-age` define recente; o padrão é **24 h**.

É escolha, não medição: preço de passagem muda todo dia, então 24 h é o ponto em
que "já coletei isso" deixa de ser boa razão para não coletar de novo. `0` desliga
o corte e restaura a checagem por existência pura.

O corte importa mais no calendário: a `calendar_key` **não inclui data**, então
sem ele uma rota coletada uma vez ficava marcada como pronta para sempre — a
segunda execução de `./run.sh calendar` era um no-op permanente, com preços de
meses atrás no banco e nenhum sinal disso. Fixado em `TestResumeHasADeadline`
(unitário) e `TestResumeRespectsAge` (integração).

Redis: `tap:raw:<search_key>:<unix>` com TTL de 7 dias, mais um índice ordenado
`tap:raw:index:<search_key>` para listar o histórico de coletas.

---

## 6. A nossa API HTTP

`cmd/api` expõe a coleta e o histórico. O catálogo de rotas, parâmetros e
exemplos está no [README](README.md#api-http) — aqui ficam só as decisões e as
armadilhas.

### A especificação não pode dessincronizar

`internal/api/openapi.yaml` é escrita à mão e embutida com `go:embed`. Para que
não envelheça em silêncio, as rotas vivem numa **tabela** (`Server.apiRoutes`) e
`TestSpecCoversAllRoutes` percorre essa tabela exigindo entrada correspondente na
especificação.

Acrescentar uma rota sem documentá-la **quebra o teste** — verificado
introduzindo uma rota falsa de propósito:

```
--- FAIL: TestSpecCoversAllRoutes
    rota GET /api/v1/naodocumentada não consta de openapi.yaml
```

Rotas intencionalmente não descritas (o redirecionamento da raiz, o próprio
`/docs`) têm `SpecPath` vazio.

**O teste confere o método, não só o caminho.** A versão anterior buscava o path
como substring, então acrescentar `DELETE /api/v1/calendar` à tabela passava — o
path já estava documentado. `specBlockFor` recorta o bloco YAML daquele path e
procura o verbo dentro dele; `TestSpecBlockIsScopedToOnePath` prova que o recorte
não vaza para o path seguinte, que é a propriedade de que a checagem depende.

### Portas, não implementações

`internal/collect` declara `Provider`, `TreatedStore` e `RawStore`; `internal/api`
declara `Collector` (implementada pelo `collect.Service`), `Reader` e `RawReader`.
O `cmd/api` injeta as implementações concretas.

Não é arquitetura por enfeite: é o que permite os testes rodarem com dublês, **sem
rede e sem banco** — e é o que fez a política de persistência caber num só lugar.

O `market` **não** está no `api.Server`: vem de `Collector.Market()`. Antes da
Etapa 1 ele aparecia em 18 lugares, porque cada chamador montava a chave.

### A tradução de erro é única

`statusFor` em `internal/api/errors.go` é o único lugar que decide status HTTP.
Nenhum handler escolhe código.

| Erro do domínio | HTTP | `code` |
|---|---|---|
| `scraper.ErrAccessDenied` · `ErrCloudflareChallenge` | **502** | `upstream_blocked` |
| `scraper.ErrRateLimited` | 429 | `upstream_rate_limited` |
| `scraper.ErrUnauthorized` | 502 | `upstream_unauthorized` |
| `scraper.ErrAPIStatus` | 502 | `upstream_invalid_response` |
| `storage.ErrNotFound` | 404 | `not_found` |
| validação de entrada | 400 | `bad_request` |
| `context.DeadlineExceeded` | 504 | `upstream_timeout` |
| `context.Canceled` | 499 | `client_closed_request` |

**Bloqueio é 502, não 403.** O cliente não errou — o provedor upstream recusou.
Devolver 403 sugeriria que o chamador não tem permissão, o que é falso. Fixado em
`TestStatusMapping`.

**Acrescentar um erro no adapter exige embrulhar um de `collect`.** Esquecer disso
faria a API mapear para 500 em silêncio; `TestSentinelsTranslateToUseCase` falha
em vez disso.

### `POST` coleta, `GET` lê

Cada coleta consulta o GDS da TAP e leva de 3 a 9 segundos: não é o
comportamento aceitável de um `GET`. Os endpoints de calendário e matriz aceitam
`refresh=true` para coletar antes de responder, e `TestCalendarDoesNotCollectByDefault`
garante que sem esse parâmetro **nada é gravado**.

### Falha de persistência não descarta a captura

Se o Redis ou o PostgreSQL falharem, a resposta sai **200 com `warnings[]`**, não
erro. O dado custou uma consulta ao GDS; jogá-lo fora por causa de um Redis fora
do ar seria o pior dos dois mundos.

A ordem de gravação é **Redis antes do PostgreSQL**, porque o registro tratado
guarda a chave do bruto (`searches.raw_key`) — assim ela nunca aponta para o
vazio.

As duas regras vivem em `collect.Service.persist` e são fixadas por
`TestRawIsPersistedBeforeTreated` e `TestPersistenceFailureBecomesWarning`. A API
apenas repassa os avisos, o que `TestWarningsArePropagated` garante.

### Sem framework

Roteamento pelo padrão da stdlib (Go 1.22+): `"POST /api/v1/searches"` e
`r.PathValue("key")`. Middleware de panic, log de acesso e timeout escritos à
mão, ~40 linhas em `server.go`. A ordem importa: `recoverPanic` é o mais externo,
para capturar panic dos demais.

`ReadHeaderTimeout` é curto (10 s) contra Slowloris, mas o teto por requisição
fica no middleware — senão a coleta lenta na TAP seria cortada.

### Entrada tolerante, saída canônica

A fronteira aceita `2026-09-01`, `01/09/2026`, `01-09-2026` e `01092026`; o
`DDMMYYYY` que o BFM exige é convertido em `dto.go`. `DisallowUnknownFields` no
corpo: um campo com erro de digitação é 400, não silêncio.

Os booleanos de query (`refresh`, `body`) passam por `strconv.ParseBool`, então
`1` e `TRUE` valem — é o que o `type: boolean` do OpenAPI promete. Antes só a
string exata `"true"` contava, e `refresh=1` respondia do banco sem coletar **sem
dizer por quê**. O que não é booleano agora é 400, não um `false` implícito: o
motivo original de ser restritivo — `refresh` custa de 3 a 9 s no GDS, então um
erro de digitação não deve virar requisição — continua garantido, e melhor.

`GET /api/v1/flights` monta o `SearchRequest` **pelo binder**, não por
`strconv.Atoi` com o erro descartado. `adults=abc` era silenciosamente 1 e
`limit=xyz` era 0, enquanto o mesmo erro em `/calendar` já dava 400 — duas
políticas para a mesma classe de entrada.

`adults` é parâmetro dos dois endpoints de leitura, com padrão 1, porque compõe a
identidade da série (§5). Zero e negativos são 400: produziriam uma chave que
nenhuma coleta pode ter gerado, logo uma lista sempre vazia — indistinguível de
"não há voos".

### O 404 do payload bruto

`storage.LoadRaw` e `LatestRaw` devolvem `ErrNotFound` quando a chave não existe,
e não o `redis.Nil` cru. Com TTL de 7 dias no Redis e o registro tratado sem
expiração, "a busca existe, a captura já não" é o caso **normal** depois de uma
semana — e `statusFor` só reconhece `storage.ErrNotFound`, então antes disso
`GET /api/v1/searches/{key}/raw` respondia **500** numa situação que o
`openapi.yaml` documentava como 404. Passava nos testes porque o dublê nunca
falhava. Fixado em `TestRawExpiredIsNotFound` e `TestRawAbsentIsNotFound`.

### O `Capture` não pode mentir sobre o fingerprint

`Capture` responde "qual combinação capturou este preço". Com rotação, o perfil
muda em tempo de execução, então reportar o do boot mentiria justamente no campo que
existe para isso. `api.Options.Fingerprint` é uma função consultada a cada resposta;
`cmd/api` a liga a `Scraper.Profile()`/`Engine()`.

Verificado ao vivo com `-tls-profile chrome_151`: a resposta traz
`"tlsProfile":"firefox_148","engine":"gecko"` — a combinação que de fato coletou.

### Um erro acumulado, uma checagem

A leitura da query string está em `internal/api/query.go`, no padrão *errors are
values*: cada parâmetro é uma linha, o erro fica num acumulador e o handler o
confere **uma vez**. Antes eram nove repetições de

```go
limit, err := intParam(q, "limit")
if err != nil {
	writeError(w, s.log, err)
	return
}
```

— quatro seguidas em `getReturns`, dezesseis linhas para ler quatro parâmetros.

Duas decisões que o código encoda:

- **O primeiro erro é o que fica.** Reportar o último faria a mensagem depender da
  ordem em que o handler lê os parâmetros, que é detalhe interno.
- **A leitura continua depois de um erro**, devolvendo zero ou o padrão. É seguro
  porque o handler desvia em `Err()` antes de usar os valores, e é o que permite
  uma linha por parâmetro.

`intPtr` e `int` coexistem de propósito: em `minNights` o zero é legítimo (ida e
volta no mesmo dia) e a ausência significa "sem filtro"; em `limit` os dois querem
dizer a mesma coisa.

`q.enum` fechou uma lacuna do caminho de **leitura**: um `cabinClass` inexistente
consultava o banco e devolvia 200 com zero datas — indistinguível de "não há voos
nessa rota". Agora é 400, com os mesmos conjuntos declarados no `openapi.yaml`.

## 7. Convenções

- Erros sempre com `fmt.Errorf("failed to ...: %w", err)`; sentinelas
  comparadas com `errors.Is`.
- `context.Context` como primeiro parâmetro de tudo que faz I/O.
- Concorrência por `sourcegraph/conc/pool` — nunca `sync.WaitGroup` manual.
- Comentários explicam **por quê** (sobretudo escolhas de fingerprint e
  tipagem), não o quê.
- `cookies.txt` está no `.gitignore`: contém `cf_clearance` atrelado ao IP.

## 8. Estado dos testes

`go test ./...` — 185 testes, todos passando e **offline**, em menos de 1 s. Mais 11
de integração atrás de `-tags=integration`, que exigem PostgreSQL e Redis de pé.

Cobertura medida em 2026-08-04: **56,4%** offline, **66,4%** com integração.

> A suíte levava 2,4 s porque o backoff de retry estava fixo em código (2 s) e dois
> testes o esperavam de verdade. Virou `config.RetryBackoff`; os testes usam 1 ms e
> o `tap` caiu de 4,1 s para 0,09 s.

| Pacote | Cobertura | O que garante |
|---|---|---|
| `config` | 94,9% | ordem dos cookies, valores com `=`, arquivo ausente não é fatal |
| `collect` | 94,1% | política de persistência, prazo da retomada, orquestração resistente a falha isolada |
| `client` | 95,1% | perfis do registro, cookies, descompressão idempotente, o spec do `chrome_151`, o pool, a detecção de bloqueio por volume |
| `models` | 83,7% | desserialização, float vs int, formatos de data, chaves canônicas |
| `report` | 80,4% | formatação das três tabelas |
| `tap` | 71,4% | classificação de resposta, retry, rotação, diagnóstico de bloqueio, cabeçalhos por endpoint, calendários |
| `api` | 78,6% | tradução erro→status, binder de query, cobertura da especificação |
| `platform` | 70,3% | ordem de encerramento, falha na montagem, `BootstrapAdapter`, os dois caminhos de rotação |
| `storage` | 68,8% (integração) | esquema, idempotência, hora de parede, séries por `adults`, prazo da retomada |

Os de `internal/api` usam dublês nas portas; os demais, **fixtures reais**:

- `internal/models/testdata/calendar_response.json` — calendário LIS→RIO,
  121.785 bytes, 365 datas.
- `internal/models/testdata/availability_search_response.json` — resposta HTTP
  200 de 279.287 bytes (34 voos, 105 ofertas).
- `internal/models/testdata/calendar_returns_response.json` — matriz ida × volta,
  ~48 KB, 337 datas de retorno.
- `internal/tap/testdata/access_denied.html` — página 403 do WAF, 92.229
  bytes.

Os testes de integração escrevem no **mesmo banco** que a aplicação usa — não há
base de teste separada. Eles marcam tudo com a rota sentinela `TST→XXX` no mercado
`ZZ` e apagam no `t.Cleanup`; sem isso as linhas sobreviviam à suíte e apareciam em
`./run.sh queries` e em `GET /api/v1/searches`, onde pareciam coleta real.

A limpeza por mercado **não alcançava os dicionários**, que não têm coluna
`market`: o aeroporto `TST · Teste · Cidade · País` sobrevivia e aparecia na
consulta 7 de `queries.sql` ao lado dos aeroportos reais. `purgeSentinel` agora
apaga os códigos sentinela de `airports` — só eles, porque os demais vêm de coleta
real do usuário.

Os testes de `internal/collect` cobrem a política de persistência: a ordem
(bruto antes do tratado) nos três modos, a tabela de falhas Redis/Postgres/ambos,
a distinção entre falha do provedor (erro) e de gravação (aviso), a retomada que
não toca a rede, e a validação que não gasta requisição.

Os testes da API cobrem: ordem de gravação, tradução erro→status, validação
que não deve chegar à TAP, `GET` que não coleta sem `refresh`, `?body=true` do
payload bruto, readiness refletindo as dependências, 405 com `Allow`, e a
cobertura da especificação pelas rotas.

Os demais cobrem: desserialização completa dos dois endpoints, float vs int dos preços,
formato de data do calendário (com teste que garante que RFC3339 **falha**),
filtro de datas comercializáveis e mínimo, datas RFC3339 dos segmentos,
cruzamento oferta↔voo, dicionários, ausência de campos não modelados nos dois
endpoints, detecção do bloqueio e extração dos seus detalhes, perfis de header
por endpoint, `exp` do JWT, `Plan.Expand` e o payload de só-ida.

### O que os testes mais recentes fixam

Três armadilhas do adapter, verificadas com o dublê `doer` (sem rede):

- **`calendarReturns` inverte a rota.** `origin`/`destination` descrevem a perna
  de *volta*. Enviar na ordem natural devolve preços de outra rota **sem erro
  nenhum** — a falha seria invisível.
- **Os dois calendários usam formatos de data diferentes**: `calendar` recebe
  DDMMYYYY, `calendarReturns` recebe ISO.
- **Corpo vazio com HTTP 200.** As rotas respondem 200 sem corpo quando o payload
  é rejeitado. A guarda existe para o erro não sair como `unexpected end of JSON
  input`.

E um contrato de coleta: quando o calendário volta sem datas, **a resposta e o
bruto acompanham o erro**. A requisição já foi gasta; jogar a captura fora
impediria investigar o motivo.

Em `internal/client`, o teste do `chrome_151` deriva o spec do `Chrome_144` e
afirma, byte a byte, que os dois diferem **apenas** em `signature_algorithms` —
os três ML-DSA na frente. É a descoberta do §4 encodada: se
alguém "atualizar" o perfil e reintroduzir `trust_anchors` (`0xca34`), o teste
aponta o índice exato. O payload GREASE do ECH é excluído da comparação porque é
aleatório por construção. O *hash* JA4 continua só verificável na rede.

### Uma lacuna que os testes expuseram

`SearchParams.Validate` checava `CabinClass` apenas por vazio, embora a própria
mensagem de erro prometesse "E, W ou C" e o OpenAPI declare `enum: [E, W, C]` em
dois lugares. Uma cabine inexistente atravessava até a TAP, que responde 200 com
corpo vazio — requisição gasta para não dizer nada. A validação do enum passou a
viver em `internal/models/params.go`, que é o caminho comum do CLI e da API.

### Coleta verificada ao vivo (2026-08-03)

```
-mode calendar -origins LIS -destinations RIO,GRU
→ 2 rotas, 725 datas, 714 com voo, 0 falhas
→ LIS-GRU mínimo 518.21 EUR (17/08/2026) | LIS-RIO mínimo 615.21 EUR
→ PostgreSQL: calendar_prices + airports/airlines populados
→ Redis: tap:raw:calendar:... com 121.785 bytes por rota
```

O mínimo LIS→RIO de 615.21 EUR coincide com o preço da fixture do
`availability/search`, o que valida os dois caminhos entre si.

## 9. Refatoração

Seis etapas, todas aplicadas. O que segue é o resultado medido de cada uma, para
que a razão de a estrutura ser esta não se perca.

**A Etapa 1 está aplicada.** A política de persistência, que existia duplicada em
`internal/tap/runner.go` e `internal/api/handlers.go` — com as duas cópias
divergindo no tratamento de falha — passou a viver **só** em
`internal/collect/service.go`, no método `persist`. O runner virou orquestração;
os handlers, tradução HTTP.

A divergência foi resolvida a favor da política da API: **falha de persistência
vira aviso, não erro**, porque cada coleta custa uma consulta ao GDS.

**A Etapa 2 está aplicada.** O adapter foi fatiado em oito arquivos por
responsabilidade e o pacote foi renomeado de `scraper` para **`tap`** — depois da
Etapa 1 ele é só o adapter da TAP, e o nome agora diz isso. Verificado como
movimentação pura: 26 funções, nenhum corpo alterado, nenhum teste tocado.

**A Etapa 3 está aplicada.** `internal/api` não importa mais o adapter: os
sentinelas de `tap` embrulham os erros de `collect`, e a tradução erro→status
compara só com estes. A direção é `tap → collect`, porque é o adapter que traduz
as falhas do provedor para o vocabulário do caso de uso.

**As 6 etapas estão aplicadas.** Em resumo:

| Etapa | O que mudou |
|---|---|
| 1 | política de persistência num só lugar (`internal/collect`) |
| 2 | adapter fatiado em 8 arquivos, renomeado para `internal/tap` |
| 3 | `api` deixou de importar o adapter; erros traduzidos no adapter |
| 4 | `wafprobe` usa o adapter real, sem cópia dos motores |
| 5 | `internal/platform.Bootstrap` monta e fecha as dependências |
| 6 | cobertura de 23,4% para 39,9% offline (50,3% com integração) |

Depois das 6 etapas houve mais um trabalho, que levou o agregado a **56,2%**
offline (**66,0%** com integração) e 85 → 174 testes. Quatro extrações:

- `collect.Collector` — interface de 3 métodos que o `Runner` consome no lugar de
  `*Service`, para que a orquestração seja testável sem provedor nem bancos
  (45,5% → **93,4%**).
- `client` de 4,3% → **93,6%**, incluindo a comparação byte a byte entre o spec do
  `chrome_151` e o do `Chrome_144`.
- `platform.BootstrapAdapter` — monta só o caminho até a TAP, sem banco. O
  `wafprobe` deixou de repetir os quatro passos de montagem; duas cópias fariam a
  ferramenta medir uma combinação e a aplicação enviar outra (58,7% → **70,3%**).

E duas simplificações:

- `internal/api/query.go` substituiu nove repetições do par `intParam` +
  verificação de erro por um binder com erro acumulado (67,9% → **76,3%**). Ver §6.
- `tap` de 48,7% → **70,9%**, cobrindo os payloads dos dois calendários pelo
  dublê `doer` e a rotação de fingerprint.

Esse trabalho expôs **três** problemas reais, nenhum deles procurado:

1. O enum de `CabinClass` não era validado, apesar de o OpenAPI declará-lo (§8).
2. O caminho de **leitura** da API também não o validava — devolvia 200 com zero
   datas, indistinguível de "não há voos" (§6).
3. Os testes de integração poluíam o banco de desenvolvimento: escrevem no mesmo
   DSN da aplicação e não apagavam as linhas sentinela, que apareciam em
   `./run.sh queries` como coleta real (§8).

E uma quinta extração, que é a **única mudança de comportamento** de todo o
trabalho: `client.Pool` faz um 403 do WAF deixar de ser fatal (§4). Ela criou duas
armadilhas próprias, porque as duas coisas passaram a reportar o perfil errado: o
`wafprobe` mediria o pool em vez do perfil pedido (resolvido desligando a rotação
por obrigação, §4) e o `Capture` da API reportaria o perfil do boot em vez do que
coletou (resolvido tornando `Fingerprint` uma função, §6).

Dois pontos que valem para quem for mexer:

- **`platform.Bootstrap` fecha na ordem inversa e, numa falha, não devolve nem
  `Deps` nem a função de encerramento** — o chamador não tem como esquecer de
  fechar o que não recebeu.
- **`internal/tap` tem uma interface `doer` de uma linha** para o cliente HTTP. É
  o que torna retry, classificação de erro e ordem de cabeçalhos testáveis sem
  rede.

## 10. Pendências

- [ ] `calendar/prices/lowest/monthly`: rota livre (200), payload não descoberto.
- [ ] `retrieveMatrix` é inviável isoladamente: o navegador o chama com **corpo
      vazio**, dependendo do estado de sessão que a busca bloqueada cria.
- [ ] `searchMoreFlights` para paginar além do primeiro lote.
- [ ] Ida e volta: `-trip-duration` gera os parâmetros, mas `listInbound` e
      `offerMatrix` ainda não são persistidos.
- [ ] **`warnings` fica em dois lugares**: na raiz de `POST /api/v1/searches` e
      dentro de `capture` em `/calendar` e `/returns`. As duas formas estão
      descritas no `openapi.yaml`; uniformizar quebraria clientes, então ficou
      documentado em vez de corrigido.
- [ ] `ensureToken` não tem singleflight: com `-concurrency 3` e o JWT vencendo,
      três `session/create` simultâneos. Inofensivo hoje (a rota é livre), mas é
      requisição gasta.
- [ ] Em `doWithRetry`, uma rotação depois de uma falha de rede repaga o backoff
      da tentativa corrente, porque `attempts` não zera. Espera desnecessária: a
      recusa de identidade não é transitória.
- [ ] **Quanto dura o bloqueio por volume da rota `search`** (§4) e a partir de
      quantas requisições ele começa. Medido em 2026-08-04: uma janela entre
      ~15:47 e ~16:11 (~24 min) e outra que **passou de 16 min de espera dedicada**
      sem abrir, depois de algumas dezenas de buscas acumuladas. A duração parece
      crescer com o volume, mas isso não foi isolado. O pool e o `wafprobe` já
      **detectam** o bloqueio (§4); o que falta é saber quanto esperar.
- [ ] **A hipótese do ALPS não foi testada.** `Chrome_131` é o único perfil
      Chromium que atravessa o WAF (com identidade Gecko) e o único com a extensão
      ALPS no codepoint antigo `0x4469`; de 133 em diante todos usam `0x44cd` e
      todos são recusados — correlação 1:1 em cinco perfis. Entre 131 e 133 mudam
      DUAS coisas ao mesmo tempo (o ALPS e a ordem das extensões), então a
      correlação não separa causa de coincidência. O experimento que separaria é um
      perfil `Chrome_144` com o ALPS trocado para `0x4469`, mantendo a ordem: foi
      montado e verificado byte a byte (única divergência no codepoint), mas as três
      tentativas de medir caíram em janela de bloqueio por volume e o controle
      falhou nas três. **Inconclusivo, não negativo.**

      <details><summary>Como refazer o experimento (o perfil não foi versionado)</summary>

      O diff completo entre `Chrome_131` e `Chrome_133`, medido offline:

      | | Chrome_131 | Chrome_133 |
      |---|---|---|
      | cifradores | 16, idênticos | 16, idênticos |
      | extensões | 18 | 18 |
      | ordem das extensões | `0000 000d fe0d ff01 0012 001b 0010 0005 000a 4469 000b 0033 0023 002b 002d 0000 0017 0000` | `0000 0023 000d 44cd 0033 0012 000b 002b 0005 0010 0000 fe0d 001b 000a 002d 0017 ff01 0000` |
      | ALPS | `0x4469` `ApplicationSettingsExtension` (h2) | `0x44cd` `ApplicationSettingsExtensionNew` (h3, h2) |
      | ALPN | `h2, http/1.1` | `h3, h2, http/1.1` |
      | HTTP/2 | SETTINGS, ordem, `connectionFlow` 15663105, pseudo-headers | idênticos |

      O ALPN **não** explica o bloqueio: 144, 146 e `chrome_151` anunciam o mesmo
      ALPN do 131 (`h2, http/1.1`) e são recusados. Restam ALPS e ordem.

      Para isolar: derive `tls_client.Chrome_144.GetClientHelloSpec()` e troque a
      única `*tls.ApplicationSettingsExtensionNew` por
      `&tls.ApplicationSettingsExtension{SupportedProtocols: <os mesmos>}`,
      preservando a posição (índice 3) e todo o resto. Monte o `ClientProfile` com
      os parâmetros HTTP/2 do `chrome_151` (são os do Chrome_144) e registre em
      `profileRegistry` **e** em `engineByProfile` com motor **`Gecko`** — o 131 só
      passa com identidade Gecko, então o experimento tem de usá-la. Um arquivo com
      `func init()` acrescentando aos dois mapas evita editar `client.go` e
      `engine.go`.

      Registre também um controle com o `Chrome_144` intacto + Gecko, para medir os
      dois na mesma janela. Afirme antes, num teste, que a única divergência é o
      codepoint — foi assim que se soube que a variável estava isolada.

      **Meça a janela primeiro:** `./run.sh wafprobe -profiles firefox_148`. Se o
      controle não passar, não gaste o experimento.

      </details>
- [ ] **Paridade de JA4 pode não ser o critério desta rota.** O JA4 ordena as
      extensões antes de hashear, então não distingue `Chrome_131` de `Chrome_133`
      — mas o WAF distingue. Se o experimento acima apontar a ordem, a premissa do
      perfil `chrome_151` (perseguir o JA4 do Chrome real) deixa de ser suficiente
      aqui, ainda que continue correta para reproduzir o ClientHello.
- [ ] **`go test -race` nunca rodou** — o ambiente não tem compilador C
      (`CGO_ENABLED=0`), e a mensagem é `-race requires cgo`. Ficou mais urgente
      com o `client.Pool`, que é estado mutável compartilhado entre as goroutines
      do `conc/pool`. `TestPoolUnderContention` exercita 16 goroutines × 200
      rodadas e pega erro de lógica sob contenção, mas **não detecta corrida de
      dados**. Rode com `-race` antes de confiar.
- [ ] Testes de `internal/storage` (exigem PostgreSQL/Redis de teste). As
      consultas de `queries.go` só foram exercitadas manualmente.
- [ ] O Swagger UI vem de CDN (`unpkg`), então `/docs` exige internet. A
      especificação em `/openapi.yaml` é local e a página tem *fallback* que
      aponta para ela. Embutir o UI resolveria, ao custo de ~2 MB no binário.
