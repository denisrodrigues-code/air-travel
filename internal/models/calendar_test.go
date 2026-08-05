package models

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// calendarFixture é uma resposta real de /bfm/rest/booking/availability/calendar/
// (LIS->RIO, economy, só ida): 121.785 bytes, 365 datas.
const calendarFixture = "testdata/calendar_response.json"

func loadCalendar(t *testing.T) []byte {
	t.Helper()

	raw, err := os.ReadFile(calendarFixture)
	if err != nil {
		t.Fatalf("failed to read fixture: %v", err)
	}
	return raw
}

// TestCalendarUnmarshal valida os tipos contra a resposta real.
func TestCalendarUnmarshal(t *testing.T) {
	var resp CalendarResponse
	if err := json.Unmarshal(loadCalendar(t), &resp); err != nil {
		t.Fatalf("failed to unmarshal calendar: %v", err)
	}

	if got, want := len(resp.Data.BestPriceForDates), 365; got != want {
		t.Errorf("len(BestPriceForDates) = %d, esperado %d (um ano)", got, want)
	}

	first := resp.Data.BestPriceForDates[0]
	if first.DepartureAirport != "LIS" {
		t.Errorf("DepartureAirport = %q, esperado LIS", first.DepartureAirport)
	}
	if first.Currency != "EUR" {
		t.Errorf("Currency = %q, esperado EUR", first.Currency)
	}
	if first.CabinClass != "E" || first.TripType != "O" {
		t.Errorf("CabinClass/TripType = %q/%q, esperado E/O", first.CabinClass, first.TripType)
	}
}

// TestCalendarNoUnknownFields revela campos que a API devolve e o modelo ignora.
func TestCalendarNoUnknownFields(t *testing.T) {
	dec := json.NewDecoder(bytes.NewReader(loadCalendar(t)))
	dec.DisallowUnknownFields()

	var resp CalendarResponse
	if err := dec.Decode(&resp); err != nil {
		t.Errorf("campo não modelado na resposta do calendário: %v", err)
	}
}

// TestCalendarDates confirma o formato de data, que NÃO é RFC3339 (não traz
// fuso), e por isso exige CalendarDateLayout.
func TestCalendarDates(t *testing.T) {
	var resp CalendarResponse
	if err := json.Unmarshal(loadCalendar(t), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	for i := range resp.Data.BestPriceForDates {
		item := &resp.Data.BestPriceForDates[i]
		if _, err := item.Departure(); err != nil {
			t.Fatalf("item %d: %v", i, err)
		}
		if _, err := item.Inserted(); err != nil {
			t.Fatalf("item %d: %v", i, err)
		}
	}

	// Um parser RFC3339 rejeitaria estas datas: é a armadilha que o layout
	// próprio evita.
	sample := resp.Data.BestPriceForDates[0].DepartureDate
	if _, err := time.Parse(time.RFC3339, sample); err == nil {
		t.Errorf("%q foi aceito como RFC3339; o layout próprio deixou de ser necessário", sample)
	}
}

// TestCalendarBookable cobre o filtro de datas comercializáveis e o mínimo.
func TestCalendarBookable(t *testing.T) {
	var resp CalendarResponse
	if err := json.Unmarshal(loadCalendar(t), &resp); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	bookable := resp.Data.Bookable()
	total := len(resp.Data.BestPriceForDates)

	if len(bookable) == 0 {
		t.Fatal("nenhuma data comercializável")
	}
	if len(bookable) > total {
		t.Errorf("bookable (%d) excede o total (%d)", len(bookable), total)
	}
	for _, item := range bookable {
		if item.NoFlights || item.SoldOut {
			t.Errorf("data %s marcada como disponível apesar de noFlights=%v soldOut=%v",
				item.DepartureDate, item.NoFlights, item.SoldOut)
		}
		if item.BestTotalPrice <= 0 {
			t.Errorf("data %s sem preço", item.DepartureDate)
		}
	}

	cheapest := resp.Data.Cheapest()
	if cheapest == nil {
		t.Fatal("Cheapest() devolveu nil havendo datas disponíveis")
	}
	for _, item := range bookable {
		if item.BestTotalPrice < cheapest.BestTotalPrice {
			t.Errorf("Cheapest() = %.2f, mas %s custa %.2f",
				cheapest.BestTotalPrice, item.DepartureDate, item.BestTotalPrice)
		}
	}
}

// TestCalendarKey fixa o formato da chave de deduplicação.
func TestCalendarKey(t *testing.T) {
	key := CalendarKey{
		Origin: "LIS", Destination: "RIO", CabinClass: "E",
		TripType: "O", Market: "PT", Adults: 1,
	}
	if got, want := key.String(), "calendar:LIS:RIO:E:O:PT:1"; got != want {
		t.Errorf("String() = %q, esperado %q", got, want)
	}
}
