// Package report formata os voos capturados para leitura humana.
package report

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"airtravel/internal/models"
)

// PrintOffers escreve uma tabela com as principais informações dos voos:
// origem, destino, número do voo, horários e valor.
//
// As ofertas são ordenadas por preço crescente; limit <= 0 imprime todas.
//
// As colunas IDA e TOTAL são separadas de propósito. A TAP exibe ao usuário o
// preço da PERNA DE IDA, e uma tabela com uma coluna só de preço parecia
// contradizê-la: 1.305,10 aqui contra 460,21 na tela, para a mesma oferta.
// Nenhum dos dois estava errado — faltava dizer qual era qual.
func PrintOffers(w io.Writer, records []models.OfferRecord, limit int) error {
	if len(records) == 0 {
		if _, err := fmt.Fprintln(w, "Nenhuma oferta encontrada."); err != nil {
			return fmt.Errorf("failed to write report: %w", err)
		}
		return nil
	}

	sorted := slices.Clone(records)
	slices.SortFunc(sorted, func(a, b models.OfferRecord) int {
		if a.TotalPrice != b.TotalPrice {
			if a.TotalPrice < b.TotalPrice {
				return -1
			}
			return 1
		}
		return a.DurationMin - b.DurationMin
	})

	if limit > 0 && len(sorted) > limit {
		sorted = sorted[:limit]
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ROTA\tVOOS\tPARTIDA\tCHEGADA\tDURAÇÃO\tPARADAS\tCABINE\tTARIFA\tIDA\tTOTAL")
	fmt.Fprintln(tw, "----\t----\t-------\t-------\t-------\t-------\t------\t------\t---\t-----")

	for _, rec := range sorted {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%.2f %s\n",
			rec.RouteString(),
			strings.Join(rec.FlightNumbers, "+"),
			formatTime(rec.DepartureTime),
			formatTime(rec.ArrivalTime),
			formatDuration(rec.DurationMin),
			formatStops(rec.NumberOfStops, rec.TechnicalStops),
			rec.Cabin,
			rec.FareFamily,
			formatOutbound(rec.OutboundPrice),
			rec.TotalPrice,
			rec.Currency,
		)
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("failed to flush report: %w", err)
	}
	return nil
}

// PrintCalendar escreve as datas mais baratas de uma rota: origem, destino,
// data, valor e se é o mínimo do mês.
//
// limit <= 0 imprime todas as datas com voo.
func PrintCalendar(w io.Writer, prices []models.BestPriceForDate, limit int) error {
	if len(prices) == 0 {
		if _, err := fmt.Fprintln(w, "Nenhuma data com voo disponível."); err != nil {
			return fmt.Errorf("failed to write report: %w", err)
		}
		return nil
	}

	sorted := slices.Clone(prices)
	slices.SortFunc(sorted, func(a, b models.BestPriceForDate) int {
		if a.BestTotalPrice != b.BestTotalPrice {
			if a.BestTotalPrice < b.BestTotalPrice {
				return -1
			}
			return 1
		}
		return strings.Compare(a.DepartureDate, b.DepartureDate)
	})

	if limit > 0 && len(sorted) > limit {
		sorted = sorted[:limit]
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ROTA\tDATA\tDIA\tCABINE\tMENOR PREÇO\tMÍN. MÊS")
	fmt.Fprintln(tw, "----\t----\t---\t------\t-----------\t--------")

	for _, p := range sorted {
		day, date := "-", p.DepartureDate
		if t, err := p.Departure(); err == nil {
			day = weekdayPT(t)
			date = t.Format("02/01/2006")
		}
		monthly := ""
		if p.MonthlyMinimum {
			monthly = "sim"
		}
		fmt.Fprintf(tw, "%s-%s\t%s\t%s\t%s\t%.2f %s\t%s\n",
			p.DepartureAirport, p.ArrivalAirport,
			date, day, p.CabinClass,
			p.BestTotalPrice, p.Currency, monthly)
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("failed to flush report: %w", err)
	}
	return nil
}

// weekdayPT devolve o dia da semana abreviado em português.
func weekdayPT(t time.Time) string {
	names := [...]string{"dom", "seg", "ter", "qua", "qui", "sex", "sáb"}
	return names[int(t.Weekday())]
}

// PrintReturns escreve a matriz ida x volta: para cada data de retorno, o preço
// TOTAL da viagem e quantas noites ela representa.
//
// departure é a data de ida a que estes retornos pertencem.
func PrintReturns(w io.Writer, route string, departure time.Time, prices []models.ReturnPrice, limit int) error {
	if len(prices) == 0 {
		if _, err := fmt.Fprintln(w, "Nenhuma data de retorno disponível."); err != nil {
			return fmt.Errorf("failed to write report: %w", err)
		}
		return nil
	}

	sorted := slices.Clone(prices)
	slices.SortFunc(sorted, func(a, b models.ReturnPrice) int {
		if a.Price != b.Price {
			if a.Price < b.Price {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ReturnDate, b.ReturnDate)
	})

	if limit > 0 && len(sorted) > limit {
		sorted = sorted[:limit]
	}

	fmt.Fprintf(w, "\n%s · ida %s\n", route, departure.Format("02/01/2006"))

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "VOLTA\tDIA\tNOITES\tTOTAL\tMÍN. MÊS")
	fmt.Fprintln(tw, "-----\t---\t------\t-----\t--------")

	for _, p := range sorted {
		date, day, nights := p.ReturnDate, "-", "-"
		if t, err := p.Return(); err == nil {
			date = t.Format("02/01/2006")
			day = weekdayPT(t)
			nights = fmt.Sprintf("%d", int(t.Sub(departure).Hours()/24))
		}
		monthly := ""
		if p.MonthlyMinimum {
			monthly = "sim"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%.2f\t%s\n", date, day, nights, p.Price, monthly)
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("failed to flush report: %w", err)
	}
	return nil
}

// PrintSummary escreve o balanço de uma execução.
func PrintSummary(w io.Writer, total, done, skipped, failed, offers int) error {
	_, err := fmt.Fprintf(w,
		"\nBuscas: %d total | %d concluídas | %d ignoradas | %d falhas | %d ofertas coletadas\n",
		total, done, skipped, failed, offers)
	if err != nil {
		return fmt.Errorf("failed to write summary: %w", err)
	}
	return nil
}

// formatTime reduz um instante RFC3339 a "DD/MM HH:MM".
func formatTime(value string) string {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return t.Format("02/01 15:04")
}

// formatDuration converte minutos em "XhYYm".
func formatDuration(minutes int) string {
	if minutes <= 0 {
		return "-"
	}
	return fmt.Sprintf("%dh%02dm", minutes/60, minutes%60)
}

// formatStops junta conexões e paradas técnicas sem somá-las.
//
// A TAP conta as duas como "escala" na interface, mas são coisas diferentes:
// conexão troca de voo, parada técnica não. Somar faria a tabela concordar com o
// site ao custo de perder a distinção; separar mantém as duas legíveis. O TP67
// LIS→GIG, que o site anuncia como "1 escala", sai daqui como "0 (+1 téc)".
func formatStops(connections, technical int) string {
	if technical <= 0 {
		return fmt.Sprintf("%d", connections)
	}
	return fmt.Sprintf("%d (+%d téc)", connections, technical)
}

// formatOutbound exibe o preço da perna de ida, ou um travessão quando a
// resposta não o trouxe — que não é o mesmo que ele ser zero.
func formatOutbound(price *float64) string {
	if price == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f", *price)
}
