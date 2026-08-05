package client

import "testing"

// TestEngineCoherence garante que cada perfil TLS tem motor associado e que os
// motores são internamente coerentes.
//
// A incoerência entre perfil TLS e client hints é o que faz o WAF da TAP recusar
// os perfis Chromium — ver CLAUDE.md §4.
func TestEngineCoherence(t *testing.T) {
	for profile := range profileRegistry {
		e, err := EngineFor(profile)
		if err != nil {
			t.Errorf("perfil %q registrado sem motor: %v", profile, err)
			continue
		}
		if e.UserAgent == "" {
			t.Errorf("motor de %q sem User-Agent", profile)
		}
		if len(e.Order) == 0 {
			t.Errorf("motor de %q sem ordem de cabeçalhos", profile)
		}
		if e.ClientHints && (e.SecCHUA == "" || e.SecCHUAPlatform == "") {
			t.Errorf("motor de %q anuncia client hints mas não os define", profile)
		}
	}

	if _, err := EngineFor("perfil_inexistente"); err == nil {
		t.Error("EngineFor aceitou perfil desconhecido")
	}
}

// TestGeckoHasNoClientHints fixa a característica que faz o Gecko passar: ele
// não anuncia client hint nenhum, então não há como divergir do navegador real.
func TestGeckoHasNoClientHints(t *testing.T) {
	if Gecko.ClientHints {
		t.Error("Gecko não deve anunciar client hints")
	}
	if te := Gecko.Extra["te"]; te != "trailers" {
		t.Errorf("Gecko.Extra[te] = %q, esperado \"trailers\"", te)
	}
	for _, name := range Gecko.Order {
		if name == "sec-ch-ua" || name == "priority" {
			t.Errorf("ordem do Gecko contém cabeçalho de Chromium: %q", name)
		}
	}

	if WebKit.SecFetch {
		t.Error("WebKit não deve enviar sec-fetch-*")
	}
	if !Chromium.ClientHints {
		t.Error("Chromium deve anunciar client hints")
	}
}

// TestProjectOrder confirma que só os cabeçalhos presentes são anunciados, com
// content-length e cookie sempre mantidos por serem definidos pelo transporte.
func TestProjectOrder(t *testing.T) {
	present := map[string]bool{"user-agent": true, "accept": true, "origin": true}

	order := Gecko.ProjectOrder(func(name string) bool { return present[name] })

	for _, name := range order {
		switch name {
		case "content-length", "cookie":
		default:
			if !present[name] {
				t.Errorf("ordem anuncia %q, que não foi enviado", name)
			}
		}
	}

	var hasCL, hasCookie bool
	for _, name := range order {
		hasCL = hasCL || name == "content-length"
		hasCookie = hasCookie || name == "cookie"
	}
	if !hasCL || !hasCookie {
		t.Error("content-length e cookie devem constar da ordem mesmo sem serem definidos à mão")
	}
}
