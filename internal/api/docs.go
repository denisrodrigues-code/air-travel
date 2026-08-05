package api

import (
	_ "embed"
	"net/http"
)

// openAPI é a especificação, embutida no binário: a documentação nunca fica
// dessincronizada do executável nem depende de arquivo no disco.
//
//go:embed openapi.yaml
var openAPI []byte

// openAPISpec serve a especificação OpenAPI 3.1.
//
// Vale por si só: qualquer cliente OpenAPI — Insomnia, Postman, geradores de
// SDK — consome isto sem precisar do Swagger UI.
func (s *Server) openAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "application/yaml; charset=utf-8")
	w.Header().Set("cache-control", "no-cache")

	if _, err := w.Write(openAPI); err != nil {
		s.log.ErrorContext(r.Context(), "falha ao servir a especificação", "err", err)
	}
}

// swaggerUIPage carrega o Swagger UI de CDN e o aponta para /openapi.yaml.
//
// O JavaScript vem de fora, então esta página exige internet. A especificação
// em si é servida localmente e continua acessível sem ela — é o que garante que
// a documentação não dependa de um CDN para ser útil.
const swaggerUIPage = `<!DOCTYPE html>
<html lang="pt">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>airtravel · API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  <style>
    body { margin: 0; background: #fafafa; }
    .topbar { display: none; }
    .fallback {
      display: none; font-family: system-ui, sans-serif;
      max-width: 42rem; margin: 4rem auto; padding: 0 1.5rem; color: #333;
    }
    .fallback code {
      background: #eee; padding: .15rem .4rem; border-radius: .2rem;
    }
  </style>
</head>
<body>
  <div id="swagger"></div>

  <div class="fallback" id="fallback">
    <h1>Swagger UI indisponível</h1>
    <p>
      O Swagger UI é carregado de um CDN e não foi possível alcançá-lo.
      A especificação continua servida localmente:
    </p>
    <p><a href="/openapi.yaml"><code>GET /openapi.yaml</code></a></p>
    <p>
      Abra esse arquivo em qualquer cliente OpenAPI (Insomnia, Postman,
      editor.swagger.io) ou use os endpoints diretamente.
    </p>
  </div>

  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"
          onerror="document.getElementById('fallback').style.display='block'"></script>
  <script>
    window.addEventListener('load', function () {
      if (typeof SwaggerUIBundle === 'undefined') {
        document.getElementById('fallback').style.display = 'block';
        return;
      }
      SwaggerUIBundle({
        url: '/openapi.yaml',
        dom_id: '#swagger',
        deepLinking: true,
        displayRequestDuration: true,
        tryItOutEnabled: true,
        defaultModelsExpandDepth: 0,
      });
    });
  </script>
</body>
</html>
`

// swaggerUI serve a página de documentação interativa.
func (s *Server) swaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("content-type", "text/html; charset=utf-8")

	if _, err := w.Write([]byte(swaggerUIPage)); err != nil {
		s.log.ErrorContext(r.Context(), "falha ao servir a documentação", "err", err)
	}
}
