package api

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// query lê parâmetros da query string acumulando o primeiro erro.
//
// Existe para eliminar um encanamento que se repetia nove vezes nos handlers:
//
//	limit, err := intParam(q, "limit")
//	if err != nil {
//		writeError(w, s.log, err)
//		return
//	}
//
// Em getReturns eram quatro desses seguidos — dezesseis linhas para ler quatro
// parâmetros. É o padrão "errors are values": o erro fica no acumulador e os
// handlers o conferem **uma vez**, depois de ler tudo.
//
// A leitura continua depois de um erro, e isso é deliberado: os valores
// devolvidos são zero ou o padrão, que nunca chegam a ser usados porque o handler
// desvia em Err(). Em troca, cada parâmetro é uma linha só.
type query struct {
	values url.Values
	err    error
}

func newQuery(r *http.Request) *query {
	return &query{values: r.URL.Query()}
}

// fail guarda o PRIMEIRO erro. Os seguintes são descartados: a resposta carrega
// um erro só, e o primeiro é o que o cliente precisa corrigir.
func (q *query) fail(err error) {
	if q.err == nil {
		q.err = err
	}
}

// Err devolve o erro acumulado, se houver.
func (q *query) Err() error { return q.err }

// require exige a presença dos parâmetros informados, reportando todos os
// ausentes de uma vez — dizer "falta origin" e só depois "falta destination"
// custaria duas viagens ao cliente.
func (q *query) require(names ...string) {
	missing := make([]string, 0, len(names))
	for _, name := range names {
		if strings.TrimSpace(q.values.Get(name)) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		q.fail(badRequest("parâmetros obrigatórios ausentes: %s", strings.Join(missing, ", ")))
	}
}

// raw lê um parâmetro como veio. A normalização de códigos IATA e datas é feita
// depois, por SearchRequest.toParams, que é o caminho comum com o corpo do POST.
func (q *query) raw(name string) string {
	return q.values.Get(name)
}

// upper lê um parâmetro normalizado para maiúsculas, como exigem os códigos IATA.
func (q *query) upper(name string) string {
	return strings.ToUpper(strings.TrimSpace(q.values.Get(name)))
}

// enum lê um parâmetro restrito a um conjunto de valores, aplicando o padrão
// quando ausente.
//
// Validar aqui fecha uma lacuna do caminho de leitura: sem isso um cabinClass
// inexistente consultava o banco e devolvia 200 com zero datas, o que se parece
// com "não há voos" e não com "você pediu uma cabine que não existe". Os
// conjuntos são os mesmos declarados no openapi.yaml.
func (q *query) enum(name, fallback string, allowed ...string) string {
	value := q.upper(name)
	if value == "" {
		return fallback
	}
	if !slices.Contains(allowed, value) {
		// A mensagem é neutra em gênero de propósito: "cabinClass inválida" e
		// "tripType inválido" concordam de formas diferentes, e um enum genérico
		// não tem como saber qual.
		q.fail(badRequest("%s: %q não é um valor aceito (use %s)",
			name, value, strings.Join(allowed, ", ")))
		return fallback
	}
	return value
}

// date lê uma data opcional, nil quando ausente.
func (q *query) date(name string) *time.Time {
	raw := q.values.Get(name)
	if raw == "" {
		return nil
	}
	t, err := parseDate(name, raw)
	if err != nil {
		q.fail(err)
		return nil
	}
	return &t
}

// intPtr lê um inteiro opcional, nil quando ausente. Usado onde a ausência
// significa "sem filtro" e o zero é um valor legítimo — o caso de minNights, em
// que 0 quer dizer ida e volta no mesmo dia.
func (q *query) intPtr(name string) *int {
	raw := q.values.Get(name)
	if raw == "" {
		return nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		q.fail(badRequest("%s: %q não é um inteiro", name, raw))
		return nil
	}
	return &v
}

// int lê um inteiro opcional, zero quando ausente. Usado onde zero já significa
// "sem limite".
func (q *query) int(name string) int {
	if v := q.intPtr(name); v != nil {
		return *v
	}
	return 0
}

// positive lê um inteiro >= 1, aplicando o padrão quando ausente.
//
// Usado na contagem de passageiros, onde zero e negativos não têm significado —
// e onde deixá-los passar produziria uma chave canônica que nenhuma coleta pode
// ter gerado, logo uma listagem sempre vazia.
func (q *query) positive(name string, fallback int) int {
	v := q.intPtr(name)
	if v == nil {
		return fallback
	}
	if *v < 1 {
		q.fail(badRequest("%s: %d é inválido (mínimo 1)", name, *v))
		return fallback
	}
	return *v
}

// flag lê um booleano tolerando as formas que os clientes de fato enviam.
//
// O openapi.yaml declara `type: boolean`, para o qual `1` e `TRUE` são valores
// válidos. Aceitar só a string exata "true" fazia `refresh=1` ser ignorado em
// silêncio — e um GET que devia coletar respondia do banco sem dizer por quê.
// Um valor que não é booleano agora é 400, não um false implícito.
func (q *query) flag(name string) bool {
	raw := strings.TrimSpace(q.values.Get(name))
	if raw == "" {
		return false
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		q.fail(badRequest("%s: %q não é um booleano (use true ou false)", name, raw))
		return false
	}
	return v
}
