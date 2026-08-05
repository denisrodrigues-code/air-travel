// Command tlsprobe mede a impressão digital TLS de cada perfil do tls-client.
//
// Faz uma requisição a um recurso estático de booking.flytap.com por perfil,
// atravessando o proxy do powhttp, e marca cada uma com ?probe=<perfil>. Depois
// basta consultar o powhttp para ler o JA3/JA4 resultante:
//
//	powhttp_search_entries(filters={url_contains: "probe="})
//
// Serve para escolher o perfil que reproduz o JA4 do navegador real — no caso
// da TAP, t13d1516h2_8daaf6152771_806a8c22fdea (Chrome 151).
//
// Usa um SVG estático de propósito: assets não passam pelo WAF, então a sondagem
// não consome tentativas nem provoca bloqueio.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"

	"airtravel/internal/client"
)

const probePath = "/chevron-select-input.svg"

func main() {
	var (
		proxyURL = flag.String("proxy", "http://localhost:8888", "proxy HTTP para inspeção")
		baseURL  = flag.String("base", "https://booking.flytap.com", "origem a sondar")
		profiles = flag.String("profiles", "chrome_133,chrome_133_psk,chrome_144,chrome_144_psk,chrome_146,chrome_146_psk",
			"perfis a sondar, separados por vírgula")
		delay = flag.Duration("delay", 2*time.Second, "intervalo entre sondagens")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx := context.Background()

	names := strings.Split(*profiles, ",")
	failures := 0

	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}

		status, err := probe(ctx, *proxyURL, *baseURL, name)
		if err != nil {
			failures++
			logger.ErrorContext(ctx, "sondagem falhou", "perfil", name, "err", err)
		} else {
			logger.InfoContext(ctx, "sondagem enviada", "perfil", name, "http", status)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(*delay):
		}
	}

	fmt.Fprintf(os.Stdout,
		"\n%d de %d perfis sondados.\nLeia os fingerprints com:\n"+
			"  powhttp_search_entries(filters={url_contains: \"probe=\"}, include_details=true)\n",
		len(names)-failures, len(names))

	if failures > 0 {
		os.Exit(1)
	}
}

// probe emite uma requisição marcada com o nome do perfil.
func probe(ctx context.Context, proxyURL, baseURL, profile string) (int, error) {
	httpClient, _, err := client.New(client.Options{
		ProxyURL:       proxyURL,
		ProfileName:    profile,
		TimeoutSeconds: 30,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to build client for %q: %w", profile, err)
	}

	target := fmt.Sprintf("%s%s?probe=%s", baseURL, probePath, profile)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) "+
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36")
	req.Header.Set("accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	req.Header.Set("accept-encoding", "gzip, deflate, br, zstd")
	req.Header[http.HeaderOrderKey] = []string{"user-agent", "accept", "accept-encoding"}
	req.Header[http.PHeaderOrderKey] = []string{":method", ":authority", ":scheme", ":path"}

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed to execute probe: %w", err)
	}
	defer resp.Body.Close()

	// Drena o corpo para que a conexão seja concluída e registrada pelo proxy.
	client.DecompressBody(resp)
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return resp.StatusCode, fmt.Errorf("failed to drain body: %w", err)
	}
	return resp.StatusCode, nil
}
