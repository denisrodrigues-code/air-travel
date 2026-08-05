package report

import (
	"strings"
	"testing"
	"time"

	"airtravel/internal/models"
)

func render(t *testing.T, fn func(*strings.Builder) error) string {
	t.Helper()

	var b strings.Builder
	if err := fn(&b); err != nil {
		t.Fatalf("erro ao renderizar: %v", err)
	}
	return b.String()
}

// TestPrintOffersSortsByPrice fixa a ordenação: preço crescente, empate
// resolvido pela duração — a tarifa mais barata primeiro, e entre iguais a
// viagem mais curta.
func TestPrintOffersSortsByPrice(t *testing.T) {
	records := []models.OfferRecord{
		{OfferID: 3, TotalPrice: 900, DurationMin: 600, Currency: "EUR", Route: []string{"LIS", "GIG"}},
		{OfferID: 1, TotalPrice: 615.21, DurationMin: 855, Currency: "EUR", Route: []string{"LIS", "GIG"}},
		{OfferID: 2, TotalPrice: 615.21, DurationMin: 595, Currency: "EUR", Route: []string{"LIS", "GIG"}},
	}

	out := render(t, func(b *strings.Builder) error { return PrintOffers(b, records, 0) })

	i615a := strings.Index(out, "9h55m")  // 595 min
	i615b := strings.Index(out, "14h15m") // 855 min
	i900 := strings.Index(out, "900.00")

	if !(i615a < i615b && i615b < i900) {
		t.Errorf("ordem incorreta (595=%d, 855=%d, 900=%d):\n%s", i615a, i615b, i900, out)
	}
}

// TestPrintOffersLimit confirma que limit corta e que 0 imprime tudo.
func TestPrintOffersLimit(t *testing.T) {
	records := make([]models.OfferRecord, 5)
	for i := range records {
		records[i] = models.OfferRecord{
			OfferID: i, TotalPrice: float64(100 * (i + 1)),
			Currency: "EUR", Route: []string{"LIS", "GIG"},
		}
	}

	limited := render(t, func(b *strings.Builder) error { return PrintOffers(b, records, 2) })
	if n := strings.Count(limited, "EUR"); n != 2 {
		t.Errorf("limit=2 imprimiu %d linhas", n)
	}

	all := render(t, func(b *strings.Builder) error { return PrintOffers(b, records, 0) })
	if n := strings.Count(all, "EUR"); n != 5 {
		t.Errorf("limit=0 imprimiu %d linhas, esperado 5", n)
	}
}

// TestPrintOffersDoesNotMutateInput: a ordenação usa cópia, senão o chamador
// veria a sua fatia reordenada como efeito colateral de imprimir.
func TestPrintOffersDoesNotMutateInput(t *testing.T) {
	records := []models.OfferRecord{
		{OfferID: 1, TotalPrice: 900, Currency: "EUR", Route: []string{"LIS", "GIG"}},
		{OfferID: 2, TotalPrice: 100, Currency: "EUR", Route: []string{"LIS", "GIG"}},
	}

	_ = render(t, func(b *strings.Builder) error { return PrintOffers(b, records, 0) })

	if records[0].OfferID != 1 {
		t.Error("PrintOffers reordenou a fatia do chamador")
	}
}

// TestPrintEmptyIsExplicit: lista vazia não pode gerar tabela sem linhas.
func TestPrintEmptyIsExplicit(t *testing.T) {
	if out := render(t, func(b *strings.Builder) error { return PrintOffers(b, nil, 0) }); !strings.Contains(out, "Nenhuma oferta") {
		t.Errorf("PrintOffers vazio = %q", out)
	}
	if out := render(t, func(b *strings.Builder) error { return PrintCalendar(b, nil, 0) }); !strings.Contains(out, "Nenhuma data") {
		t.Errorf("PrintCalendar vazio = %q", out)
	}
	if out := render(t, func(b *strings.Builder) error {
		return PrintReturns(b, "LIS-RIO", time.Now(), nil, 0)
	}); !strings.Contains(out, "Nenhuma data de retorno") {
		t.Errorf("PrintReturns vazio = %q", out)
	}
}

// TestPrintCalendarFormatsDate confirma o dia da semana em português e a data
// em DD/MM/AAAA — o formato da API (2026-09-01T00:00:00) não é para leitura.
func TestPrintCalendarFormatsDate(t *testing.T) {
	prices := []models.BestPriceForDate{{
		DepartureAirport: "LIS", ArrivalAirport: "GIG",
		DepartureDate:  "2026-09-01T00:00:00", // uma terça-feira
		BestTotalPrice: 487.21, Currency: "EUR", CabinClass: "E",
		MonthlyMinimum: true,
	}}

	out := render(t, func(b *strings.Builder) error { return PrintCalendar(b, prices, 0) })

	for _, want := range []string{"LIS-GIG", "01/09/2026", "ter", "487.21 EUR", "sim"} {
		if !strings.Contains(out, want) {
			t.Errorf("saída não contém %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "T00:00:00") {
		t.Error("a data crua da API vazou para a tabela")
	}
}

// TestPrintReturnsComputesNights verifica o cálculo de noites, que é o eixo de
// análise da matriz.
func TestPrintReturnsComputesNights(t *testing.T) {
	departure := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	prices := []models.ReturnPrice{
		{ReturnDate: "2026-09-19T00:00:00", Price: 605.92},
		{ReturnDate: "2026-09-21T00:00:00", Price: 520.92},
	}

	out := render(t, func(b *strings.Builder) error {
		return PrintReturns(b, "LIS-RIO", departure, prices, 0)
	})

	if !strings.Contains(out, "ida 01/09/2026") {
		t.Errorf("cabeçalho sem a data de ida:\n%s", out)
	}
	// A mais barata (21/09, 20 noites) vem primeiro.
	i20 := strings.Index(out, "520.92")
	i18 := strings.Index(out, "605.92")
	if i20 > i18 {
		t.Errorf("ordenação por preço falhou:\n%s", out)
	}
	for _, want := range []string{"20", "18"} {
		if !strings.Contains(out, want) {
			t.Errorf("saída não contém a contagem de noites %q:\n%s", want, out)
		}
	}
}

// TestFormatDuration cobre a formatação vinda do campo duration da API.
func TestFormatDuration(t *testing.T) {
	tests := map[int]string{595: "9h55m", 855: "14h15m", 620: "10h20m", 60: "1h00m", 0: "-", -5: "-"}
	for minutes, want := range tests {
		if got := formatDuration(minutes); got != want {
			t.Errorf("formatDuration(%d) = %q, esperado %q", minutes, got, want)
		}
	}
}

// TestWeekdayPT cobre os sete dias, incluindo o acento de sábado.
func TestWeekdayPT(t *testing.T) {
	base := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC) // domingo
	want := []string{"dom", "seg", "ter", "qua", "qui", "sex", "sáb"}

	for i, w := range want {
		if got := weekdayPT(base.AddDate(0, 0, i)); got != w {
			t.Errorf("dia +%d = %q, esperado %q", i, got, w)
		}
	}
}

// TestPrintSummary confirma o balanço.
func TestPrintSummary(t *testing.T) {
	out := render(t, func(b *strings.Builder) error { return PrintSummary(b, 10, 7, 2, 1, 350) })

	for _, want := range []string{"10 total", "7 concluídas", "2 ignoradas", "1 falhas", "350 ofertas"} {
		if !strings.Contains(out, want) {
			t.Errorf("resumo não contém %q: %s", want, out)
		}
	}
}
