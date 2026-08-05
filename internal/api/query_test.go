package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newQueryFor monta o binder a partir de uma query string literal.
func newQueryFor(target string) *query {
	return newQuery(httptest.NewRequest(http.MethodGet, target, nil))
}

// TestQueryFirstErrorWins é a razão de o binder existir com um acumulador em vez
// de retornar erro em cada leitura: os handlers leem tudo e conferem UMA vez.
//
// O primeiro erro é o que fica. Reportar o último faria a mensagem depender da
// ordem em que o handler lê os parâmetros, que é um detalhe interno.
func TestQueryFirstErrorWins(t *testing.T) {
	q := newQueryFor("/?limit=abc&offset=xyz")

	q.int("limit")
	q.int("offset")

	err := q.Err()
	if err == nil {
		t.Fatal("dois parâmetros inválidos e nenhum erro")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("erro = %v, esperado citar limit (o primeiro)", err)
	}
	if strings.Contains(err.Error(), "offset") {
		t.Errorf("erro = %v, não deveria citar o segundo problema", err)
	}
}

// TestQueryKeepsReadingAfterError: a leitura continua depois de um erro e devolve
// zero/padrão. É seguro porque o handler desvia em Err() antes de usar os valores
// — e é o que permite ler cada parâmetro numa linha só.
func TestQueryKeepsReadingAfterError(t *testing.T) {
	q := newQueryFor("/?limit=abc&origin=lis&cabinClass=W")

	q.int("limit") // falha

	if got := q.upper("origin"); got != "LIS" {
		t.Errorf("upper() após erro = %q, esperado LIS", got)
	}
	if got := q.enum("cabinClass", "E", "E", "W", "C"); got != "W" {
		t.Errorf("enum() após erro = %q, esperado W", got)
	}
	if q.Err() == nil {
		t.Error("o erro original foi perdido")
	}
}

// TestQueryRequireReportsAllMissing: dizer "falta origin" e só depois "falta
// destination" custaria duas viagens ao cliente.
func TestQueryRequireReportsAllMissing(t *testing.T) {
	q := newQueryFor("/?destination=RIO")
	q.require("origin", "destination", "departureDate")

	err := q.Err()
	if err == nil {
		t.Fatal("parâmetros ausentes foram aceitos")
	}
	for _, want := range []string{"origin", "departureDate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("erro = %v, esperado citar %q", err, want)
		}
	}

	// Espaço em branco não conta como valor.
	blank := newQueryFor("/?origin=%20%20")
	blank.require("origin")
	if blank.Err() == nil {
		t.Error("origin só com espaços foi aceito")
	}
}

func TestQueryEnum(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		want     string
		wantFail bool
	}{
		{"ausente cai no padrão", "/", "E", false},
		{"valor permitido", "/?cabinClass=C", "C", false},
		{"minúscula é normalizada", "/?cabinClass=w", "W", false},
		{"valor fora do conjunto", "/?cabinClass=Z", "E", true},
	}

	for _, tc := range tests {
		q := newQueryFor(tc.target)
		got := q.enum("cabinClass", "E", "E", "W", "C")

		if got != tc.want {
			t.Errorf("%s: enum() = %q, esperado %q", tc.name, got, tc.want)
		}
		if failed := q.Err() != nil; failed != tc.wantFail {
			t.Errorf("%s: erro = %v, esperado falha = %v", tc.name, q.Err(), tc.wantFail)
		}
	}
}

// TestQueryIntPtrDistinguishesZeroFromAbsent é a razão de existirem intPtr e int:
// em minNights, 0 significa "ida e volta no mesmo dia" e ausente significa "sem
// filtro". Colapsar os dois esconderia uma consulta legítima.
func TestQueryIntPtrDistinguishesZeroFromAbsent(t *testing.T) {
	if got := newQueryFor("/").intPtr("minNights"); got != nil {
		t.Errorf("ausente = %v, esperado nil", *got)
	}
	got := newQueryFor("/?minNights=0").intPtr("minNights")
	if got == nil {
		t.Fatal("minNights=0 virou nil, colapsando com a ausência")
	}
	if *got != 0 {
		t.Errorf("minNights = %d, esperado 0", *got)
	}

	// Já em limit, zero e ausente significam a mesma coisa: sem limite.
	if got := newQueryFor("/").int("limit"); got != 0 {
		t.Errorf("int() ausente = %d, esperado 0", got)
	}
	if got := newQueryFor("/?limit=25").int("limit"); got != 25 {
		t.Errorf("int() = %d, esperado 25", got)
	}
}

func TestQueryDate(t *testing.T) {
	if got := newQueryFor("/").date("from"); got != nil {
		t.Errorf("ausente = %v, esperado nil", got)
	}

	// A fronteira é tolerante quanto ao formato de entrada.
	for _, target := range []string{
		"/?from=2026-09-01", "/?from=01/09/2026", "/?from=01-09-2026", "/?from=01092026",
	} {
		q := newQueryFor(target)
		got := q.date("from")

		if q.Err() != nil {
			t.Errorf("%s: %v", target, q.Err())
			continue
		}
		if got == nil || got.Format("2006-01-02") != "2026-09-01" {
			t.Errorf("%s: data = %v, esperado 2026-09-01", target, got)
		}
	}

	q := newQueryFor("/?from=setembro")
	if q.date("from"); q.Err() == nil {
		t.Error("data inválida foi aceita")
	}
}

// TestQueryFlagAcceptsBooleans: o openapi.yaml declara refresh como boolean, e
// `1`/`TRUE` são booleanos válidos. Tratá-los como false em silêncio fazia um GET
// que devia coletar responder do banco sem dizer por quê.
//
// O motivo original de exigir a string exata — refresh custa de 3 a 9 s no GDS,
// então um erro de digitação não deve virar uma requisição — continua garantido, e
// melhor: um valor que não é booleano agora é 400, não uma coleta acidental nem um
// false implícito.
func TestQueryFlagAcceptsBooleans(t *testing.T) {
	for _, target := range []string{"/?refresh=true", "/?refresh=TRUE", "/?refresh=1", "/?refresh=t"} {
		q := newQueryFor(target)
		if !q.flag("refresh") {
			t.Errorf("%s não foi reconhecido como verdadeiro", target)
		}
		if q.Err() != nil {
			t.Errorf("%s: %v", target, q.Err())
		}
	}

	for _, target := range []string{"/", "/?refresh=", "/?refresh=false", "/?refresh=0"} {
		q := newQueryFor(target)
		if q.flag("refresh") {
			t.Errorf("%s foi tratado como verdadeiro", target)
		}
		if q.Err() != nil {
			t.Errorf("%s: %v", target, q.Err())
		}
	}

	// O que não é booleano é erro do cliente, não um false silencioso.
	for _, target := range []string{"/?refresh=yes", "/?refresh=sim", "/?refresh=on"} {
		q := newQueryFor(target)
		if q.flag("refresh"); q.Err() == nil {
			t.Errorf("%s foi aceito", target)
		}
	}
}

// TestQueryPositiveRejectsZeroAndBelow: adults compõe a chave da coleta, então um
// zero produziria uma chave que nenhuma coleta pode ter gerado — logo uma listagem
// sempre vazia, indistinguível de "não há voos".
func TestQueryPositiveRejectsZeroAndBelow(t *testing.T) {
	if got := newQueryFor("/").positive("adults", 1); got != 1 {
		t.Errorf("ausente: adults = %d, esperado o padrão 1", got)
	}
	if got := newQueryFor("/?adults=3").positive("adults", 1); got != 3 {
		t.Errorf("adults = %d, esperado 3", got)
	}
	for _, target := range []string{"/?adults=0", "/?adults=-2"} {
		q := newQueryFor(target)
		if q.positive("adults", 1); q.Err() == nil {
			t.Errorf("%s foi aceito", target)
		}
	}
}

// ---------------------------------------------------------------------------
// A lacuna que o binder fechou, medida pela borda HTTP
// ---------------------------------------------------------------------------

// TestReadPathRejectsInvalidCabin: antes do binder, o caminho de LEITURA não
// validava a cabine. Um cabinClass inexistente consultava o banco e devolvia 200
// com zero datas — indistinguível de "não há voos nessa rota".
func TestReadPathRejectsInvalidCabin(t *testing.T) {
	c := &fakeCollector{}
	h := newTestServer(t, c, &fakeReader{}, &fakeRaw{})

	for _, target := range []string{
		"/api/v1/calendar?origin=LIS&destination=RIO&cabinClass=Z",
		"/api/v1/returns?origin=LIS&destination=RIO&cabinClass=Z",
	} {
		rec := do(t, h, http.MethodGet, target, "")

		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, esperado 400 (corpo: %s)", target, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "cabinClass") {
			t.Errorf("%s: corpo não cita o parâmetro: %s", target, rec.Body)
		}
	}

	// A validação é anterior à coleta: nada deve ter chegado à TAP.
	if c.calendarCalls != 0 || c.returnsCalls != 0 {
		t.Errorf("calendarCalls=%d returnsCalls=%d, esperado 0 — parâmetro inválido não deve gastar requisição",
			c.calendarCalls, c.returnsCalls)
	}
}

// TestReadPathRejectsBadTripType mantém a validação que já existia, agora vinda
// do binder.
func TestReadPathRejectsBadTripType(t *testing.T) {
	h := newTestServer(t, &fakeCollector{}, &fakeReader{}, &fakeRaw{})

	rec := do(t, h, http.MethodGet, "/api/v1/calendar?origin=LIS&destination=RIO&tripType=X", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, esperado 400 (corpo: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "tripType") {
		t.Errorf("corpo não cita tripType: %s", rec.Body)
	}
}

// TestCalendarDefaultsToRoundTrip: o padrão é R porque com ida e volta a TAP
// devolve a tarifa de round-trip, que é a mais barata — é o preço que se quer ver
// num calendário.
func TestCalendarDefaultsToRoundTrip(t *testing.T) {
	h := newTestServer(t, &fakeCollector{}, &fakeReader{}, &fakeRaw{})

	rec := do(t, h, http.MethodGet, "/api/v1/calendar?origin=LIS&destination=RIO", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, esperado 200 (corpo: %s)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"tripType":"R"`) {
		t.Errorf("tripType padrão não é R: %s", rec.Body)
	}
}

// TestBadIntParamIsRejectedBeforeCollecting fecha o conjunto: um inteiro
// malformado é 400, não 500 nem uma consulta silenciosa.
func TestBadIntParamIsRejectedBeforeCollecting(t *testing.T) {
	c := &fakeCollector{}
	h := newTestServer(t, c, &fakeReader{}, &fakeRaw{})

	for _, target := range []string{
		"/api/v1/calendar?origin=LIS&destination=RIO&limit=muitos",
		"/api/v1/returns?origin=LIS&destination=RIO&minNights=sete",
		"/api/v1/searches?limit=abc",
	} {
		rec := do(t, h, http.MethodGet, target, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, esperado 400 (corpo: %s)", target, rec.Code, rec.Body)
		}
	}
	if c.calendarCalls != 0 || c.returnsCalls != 0 {
		t.Error("parâmetro malformado chegou a disparar coleta")
	}
}
