#!/usr/bin/env bash
# Atalhos para rodar o scraper. Equivalente ao Makefile, para quem não tem make.
# Uso: ./run.sh <comando>   —   ./run.sh help lista tudo.
set -euo pipefail

cd "$(dirname "$0")"

# Parâmetros da demonstração. Sobreponha por variável de ambiente, ex.:
#   ORIGINS=OPO DESTINATIONS=GRU ./run.sh calendar
ORIGINS="${ORIGINS:-LIS}"
DESTINATIONS="${DESTINATIONS:-RIO}"
CABINS="${CABINS:-E}"
ADULTS="${ADULTS:-1}"
START="${START:-01-09-2026}"
DAYS="${DAYS:-2}"
FROM="${FROM:-01-09-2026}"
TO="${TO:-31-10-2026}"
TOP="${TOP:-12}"
# O mercado define a MOEDA e as regras tarifárias — não é conversão de câmbio.
# PT -> EUR (site português) · BR -> BRL (site brasileiro).
MARKET="${MARKET:-PT}"
LANGUAGE="${LANGUAGE:-pt}"
# Vazio desativa o proxy. Aponte para o powhttp para inspecionar o tráfego.
PROXY="${PROXY:-}"
# Vazio usa config.DefaultTLSProfile, a única fonte de verdade. Não fixe um perfil
# aqui: o padrão acompanha a medição, e um valor fixado num script envelhece em
# silêncio — foi o que aconteceu quando este arquivo trazia -tls-profile chrome_151.
# No modo search, sem cf_clearance no jar, só Gecko/WebKit atravessam o WAF.
PROFILE="${PROFILE:-}"
API_PORT="${API_PORT:-8080}"

scraper() {
  local args=(-proxy "$PROXY" -market "$MARKET" -language "$LANGUAGE")
  [ -n "$PROFILE" ] && args+=(-tls-profile "$PROFILE")
  go run ./cmd/scraper "${args[@]}" "$@"
}

# health espera os contêineres ficarem saudáveis.
health() {
  printf 'aguardando serviços'
  for _ in $(seq 1 60); do
    local pg rd
    pg=$(docker inspect -f '{{.State.Health.Status}}' airtravel-postgres 2>/dev/null || echo x)
    rd=$(docker inspect -f '{{.State.Health.Status}}' airtravel-redis 2>/dev/null || echo x)
    if [ "$pg" = healthy ] && [ "$rd" = healthy ]; then echo ' prontos'; return 0; fi
    printf '.'; sleep 1
  done
  echo ' TIMEOUT'; return 1
}

case "${1:-help}" in

help)
  cat <<'TXT'
Comandos:
  up         sobe PostgreSQL + Redis e espera ficarem saudáveis
  down       para os serviços (mantém os dados)
  reset      para os serviços e APAGA os volumes
  test       roda os testes offline (fixtures reais e dublês)
  check      gofmt + vet + testes (offline e integração)
  test-int   testes contra PostgreSQL e Redis reais (exige ./run.sh up)
  calendar   coleta o melhor preço por data de partida (ida e volta)
  returns    coleta a matriz ida x volta
  search     voos, horários e tarifas detalhados
  queries    mostra o que foi coletado, consultando o PostgreSQL
  redis      lista as chaves de respostas brutas
  psql       abre um shell psql
  api        sobe a API HTTP; docs em http://localhost:8080/docs
  wafprobe   mede quais perfis TLS atravessam o WAF (rota search)
  routeprobe mede cada perfil nas rotas de calendário, que não discriminam
  tlsprobe   mede o JA3/JA4 de cada perfil (exige o proxy powhttp)
  demo       up + test + calendar + returns + search + queries

Variáveis: ORIGINS DESTINATIONS CABINS ADULTS START DAYS FROM TO TOP
           MARKET LANGUAGE PROXY PROFILE API_PORT COOKIES_FILE
Exemplos:  ORIGINS=OPO DESTINATIONS=GRU ./run.sh calendar
           MARKET=BR ./run.sh calendar        # preços em BRL
           ./run.sh calendar -resume=false     # flags extras são repassadas
TXT
  ;;

up)     docker compose up -d && health ;;
down)   docker compose down ;;
reset)  docker compose down -v ;;

test)   go test ./... -count=1 ;;

test-int)
  echo '--- integração: exige ./run.sh up ---'
  go test -tags=integration ./internal/storage/ -count=1
  ;;
check)
  echo '--- gofmt (silêncio = ok) ---'; gofmt -l ./cmd ./internal
  echo '--- vet ---';                   go vet ./... && go vet -tags=integration ./...
  echo '--- testes offline ---';        go test ./... -count=1
  echo '--- testes de integração ---';  go test -tags=integration ./internal/storage/ -count=1
  ;;

calendar)
  scraper -mode calendar \
    -origins "$ORIGINS" -destinations "$DESTINATIONS" -cabins "$CABINS" \
    -adults "$ADULTS" -trip-type R -from "$FROM" -to "$TO" -top "$TOP" "${@:2}"
  ;;

returns)
  # -trip-type R não é decoração: com O a TAP devolve menos datas de retorno e
  # não resolve o código de cidade para o aeroporto (destino fica RIO em vez de
  # GIG), então resolved_dest é gravado com a informação pior. Medido em
  # 05/08/2026: 338 datas e "RIO" com O, 341 e "GIG" com R, preços iguais.
  scraper -mode returns \
    -origins "$ORIGINS" -destinations "$DESTINATIONS" -cabins "$CABINS" \
    -adults "$ADULTS" -start "$START" -days "$DAYS" -trip-type R \
    -from "$FROM" -to "$TO" -top "$TOP" "${@:2}"
  ;;

search)
  scraper -mode search \
    -origins "$ORIGINS" -destinations "$DESTINATIONS" -cabins "$CABINS" \
    -adults "$ADULTS" -start "$START" -days 1 -top "$TOP" "${@:2}"
  ;;

queries)
  docker exec -i airtravel-postgres psql -U airtravel -d airtravel < queries.sql
  ;;

redis)
  docker exec airtravel-redis redis-cli --raw KEYS 'tap:raw:*' | grep -v index | sort
  ;;

psql)
  docker exec -it airtravel-postgres psql -U airtravel -d airtravel
  ;;

wafprobe)
  go run ./cmd/wafprobe -proxy "$PROXY" -market "$MARKET" -language "$LANGUAGE" "${@:2}"
  ;;

routeprobe)
  go run ./cmd/routeprobe -proxy "$PROXY" -market "$MARKET" -language "$LANGUAGE" "${@:2}"
  ;;

tlsprobe)
  go run ./cmd/tlsprobe "${@:2}"
  ;;

api)
  args=(-addr ":$API_PORT" -market "$MARKET" -language "$LANGUAGE" -proxy "$PROXY")
  [ -n "$PROFILE" ] && args+=(-tls-profile "$PROFILE")
  echo "docs em http://localhost:$API_PORT/docs"
  go run ./cmd/api "${args[@]}" "${@:2}"
  ;;

demo)
  "$0" up && "$0" test && "$0" calendar && "$0" returns && "$0" search && "$0" queries
  ;;

*)
  echo "comando desconhecido: $1" >&2
  "$0" help >&2
  exit 2
  ;;
esac
