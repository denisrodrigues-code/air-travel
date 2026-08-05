.PHONY: help up down reset test test-int calendar returns search api wafprobe tlsprobe demo psql redis queries fmt check

# Rota e período usados pelos alvos de demonstração. Sobreponha na linha de
# comando, ex.: make calendar ORIGINS=OPO DESTINATIONS=GRU
ORIGINS      ?= LIS
DESTINATIONS ?= RIO
CABINS       ?= E
ADULTS       ?= 1
START        ?= 01-09-2026
DAYS         ?= 2
FROM         ?= 01-09-2026
TO           ?= 31-10-2026
TOP          ?= 12
# O mercado define a MOEDA e as regras tarifárias. PT -> EUR · BR -> BRL.
MARKET       ?= PT
LANGUAGE     ?= pt
# Vazio desativa o proxy. Aponte para o powhttp quando quiser inspecionar.
PROXY        ?=
# Vazio usa config.DefaultTLSProfile (firefox_148). Não fixe um perfil Chromium:
# tomam 403 do WAF no modo search.
PROFILE      ?=
API_PORT     ?= 8080

SCRAPER = go run ./cmd/scraper -proxy "$(PROXY)" -market $(MARKET) -language $(LANGUAGE) $(if $(PROFILE),-tls-profile $(PROFILE),)

help: ## Lista os alvos disponíveis
	@grep -E '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) \
		| sed 's/:.*## /\t/' | awk -F'\t' '{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

up: ## Sobe PostgreSQL e Redis e espera ficarem saudáveis
	docker compose up -d
	@printf 'aguardando serviços'
	@until [ "$$(docker inspect -f '{{.State.Health.Status}}' airtravel-postgres 2>/dev/null)" = healthy ] \
	   && [ "$$(docker inspect -f '{{.State.Health.Status}}' airtravel-redis 2>/dev/null)" = healthy ]; do \
		printf '.'; sleep 1; done
	@echo ' prontos'

down: ## Para os serviços (mantém os dados)
	docker compose down

reset: ## Para os serviços e APAGA os volumes
	docker compose down -v

test: ## Roda os testes contra as fixtures reais (não usa rede)
	go test ./... -count=1

test-int: ## Testes contra PostgreSQL e Redis reais (exige make up)
	go test -tags=integration ./internal/storage/ -count=1

check: ## Formatação, vet e testes
	gofmt -l ./cmd ./internal
	go vet ./...
	go test ./... -count=1
	go vet -tags=integration ./...
	go test -tags=integration ./internal/storage/ -count=1

calendar: ## Coleta o melhor preço por data de partida (ida e volta)
	$(SCRAPER) -mode calendar \
		-origins $(ORIGINS) -destinations $(DESTINATIONS) -cabins $(CABINS) \
		-adults $(ADULTS) -trip-type R -from $(FROM) -to $(TO) -top $(TOP)

returns: ## Coleta a matriz ida x volta (preço total por data de retorno)
	$(SCRAPER) -mode returns \
		-origins $(ORIGINS) -destinations $(DESTINATIONS) -cabins $(CABINS) \
		-adults $(ADULTS) -start $(START) -days $(DAYS) \
		-from $(FROM) -to $(TO) -top $(TOP)

search: ## Coleta voos, horários e tarifas detalhados
	$(SCRAPER) -mode search \
		-origins $(ORIGINS) -destinations $(DESTINATIONS) -cabins $(CABINS) \
		-adults $(ADULTS) -start $(START) -days 1 -top $(TOP)

demo: up test calendar returns search queries ## Roteiro completo de ponta a ponta

wafprobe: ## Mede quais perfis TLS atravessam o WAF
	go run ./cmd/wafprobe -proxy "$(PROXY)" -market $(MARKET) -language $(LANGUAGE)

tlsprobe: ## Mede o JA3/JA4 de cada perfil (exige o proxy powhttp)
	go run ./cmd/tlsprobe

api: ## Sobe a API HTTP; docs em http://localhost:8080/docs
	go run ./cmd/api -addr ":$(API_PORT)" -market $(MARKET) -language $(LANGUAGE) \
		-proxy "$(PROXY)" $(if $(PROFILE),-tls-profile $(PROFILE),)

psql: ## Abre um shell psql no banco
	docker exec -it airtravel-postgres psql -U airtravel -d airtravel

redis: ## Lista as chaves de respostas brutas no Redis
	@docker exec airtravel-redis redis-cli --raw KEYS 'tap:raw:*' | grep -v index | sort

queries: ## Mostra o que foi coletado, consultando o PostgreSQL
	@docker exec airtravel-postgres psql -U airtravel -d airtravel -f /dev/stdin < queries.sql
