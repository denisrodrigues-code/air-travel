package models

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// returnsFixture é uma resposta real de calendarReturns/ para a viagem
// LIS→RIO com ida em 01/09/2026: 48.136 bytes, 337 datas de retorno.
const returnsFixture = "testdata/calendar_returns_response.json"

func loadReturns(t *testing.T) []byte {
	t.Helper()

	raw, err := os.ReadFile(returnsFixture)
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}
	return raw
}

// TestReturnsUnmarshal valida os tipos contra a resposta real.
func TestReturnsUnmarshal(t *testing.T) {
	var resp CalendarReturnsResponse
	if err := json.Unmarshal(loadReturns(t), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if got, want := len(resp.Data.Returns), 337; got != want {
		t.Errorf("len(Returns) = %d, esperado %d", got, want)
	}
	if got, want := resp.Data.Currency, "EUR"; got != want {
		t.Errorf("Currency = %q, esperado %q", got, want)
	}
	if got, want := resp.Data.TripType, "R"; got != want {
		t.Errorf("TripType = %q, esperado %q", got, want)
	}

	// data.origin/destination seguem o sentido da VIAGEM e o destino vem
	// resolvido para o aeroporto concreto.
	if got, want := resp.Data.Origin, "LIS"; got != want {
		t.Errorf("Data.Origin = %q, esperado %q", got, want)
	}
	if resp.Data.Destination == "RIO" {
		t.Error("Data.Destination = RIO; esperava-se um aeroporto resolvido (ex.: GIG)")
	}
}

// TestReturnsNoUnknownFields revela campos que a API devolve e o modelo ignora.
func TestReturnsNoUnknownFields(t *testing.T) {
	dec := json.NewDecoder(bytes.NewReader(loadReturns(t)))
	dec.DisallowUnknownFields()

	var resp CalendarReturnsResponse
	if err := dec.Decode(&resp); err != nil {
		t.Errorf("campo não modelado em calendarReturns: %v", err)
	}
}

// TestReturnsRequestShape fixa as duas armadilhas do payload: a inversão de
// origem/destino e a data em ISO. O corpo real tem exatamente 127 bytes.
func TestReturnsRequestShape(t *testing.T) {
	req := CalendarReturnsRequest{
		CabinClass:    "E",
		Destination:   "LIS", // origem da viagem
		Market:        "PT",
		Origin:        "RIO", // destino da viagem
		TripType:      "R",
		DepartureDate: "2026-09-01",
		PaxType:       "ADT",
	}

	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	const want = `{"cabinClass":"E","destination":"LIS","market":"PT","origin":"RIO",` +
		`"tripType":"R","departureDate":"2026-09-01","paxType":"ADT"}`
	if got := string(encoded); got != want {
		t.Errorf("corpo serializado divergente\n obtido:  %s\n esperado: %s", got, want)
	}
	if got, want := len(encoded), 127; got != want {
		t.Errorf("len(corpo) = %d, esperado %d bytes (como no tráfego real)", got, want)
	}
}

// TestReturnsDates confirma o formato de data e o cálculo de noites.
func TestReturnsDates(t *testing.T) {
	var resp CalendarReturnsResponse
	if err := json.Unmarshal(loadReturns(t), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	departure := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	for i := range resp.Data.Returns {
		item := &resp.Data.Returns[i]
		returnDate, err := item.Return()
		if err != nil {
			t.Fatalf("item %d: %v", i, err)
		}
		if returnDate.Before(departure) {
			t.Errorf("item %d: retorno %s é anterior à ida %s",
				i, returnDate.Format(time.DateOnly), departure.Format(time.DateOnly))
		}
	}
}

// TestReturnsBookable cobre o filtro e o mínimo.
func TestReturnsBookable(t *testing.T) {
	var resp CalendarReturnsResponse
	if err := json.Unmarshal(loadReturns(t), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	bookable := resp.Data.Bookable()
	if len(bookable) == 0 {
		t.Fatal("nenhuma data de retorno comercializável")
	}
	for _, item := range bookable {
		if item.NoFlights || item.SoldOut || item.Price <= 0 {
			t.Errorf("retorno %s marcado como disponível indevidamente", item.ReturnDate)
		}
	}

	cheapest := resp.Data.Cheapest()
	if cheapest == nil {
		t.Fatal("Cheapest() devolveu nil havendo datas disponíveis")
	}
	for _, item := range bookable {
		if item.Price < cheapest.Price {
			t.Errorf("Cheapest() = %.2f, mas %s custa %.2f",
				cheapest.Price, item.ReturnDate, item.Price)
		}
	}
}

// TestReturnsInWindow cobre o recorte por intervalo.
func TestReturnsInWindow(t *testing.T) {
	var resp CalendarReturnsResponse
	if err := json.Unmarshal(loadReturns(t), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

	window := ReturnsInWindow(resp.Data.Returns, from, to)
	if len(window) == 0 {
		t.Fatal("recorte de setembro ficou vazio")
	}
	if len(window) > 30 {
		t.Errorf("len(window) = %d, esperado no máximo 30 dias", len(window))
	}
	for _, item := range window {
		day, err := item.Return()
		if err != nil {
			t.Fatalf("data inválida no recorte: %v", err)
		}
		if day.Before(from) || day.After(to) {
			t.Errorf("data %s fora do intervalo pedido", day.Format(time.DateOnly))
		}
	}
}
