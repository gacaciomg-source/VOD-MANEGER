// Package panel serve o painel web, embutido no próprio binário.
//
// Deliberadamente sem etapa de build: o painel é HTML, CSS e JavaScript puros, sem npm,
// sem bundler, sem node_modules. Isso significa que `go run` sobe o sistema inteiro —
// servidor e interface — sem nenhuma ferramenta além do Go.
//
// O custo dessa escolha é não ter framework; o benefício é que o projeto continua
// buildável com um único comando por anos, sem cadeia de dependências de frontend
// apodrecendo. Para o volume de telas deste painel, o custo é baixo.
package panel

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed assets
var arquivos embed.FS

// Handler devolve o handler do painel.
//
// Ele serve os arquivos estáticos e faz fallback para index.html em qualquer rota não
// encontrada — o roteamento das telas acontece no navegador.
func Handler() (http.Handler, error) {
	sub, err := fs.Sub(arquivos, "assets")
	if err != nil {
		return nil, err
	}
	servidor := http.FileServer(http.FS(sub))

	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return nil, err
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caminho := strings.TrimPrefix(r.URL.Path, "/")

		// O índice é sempre servido do buffer, com no-store: assim, ao atualizar o
		// binário, o navegador não fica preso numa versão antiga do painel.
		if caminho == "" || caminho == "index.html" {
			servirIndex(w, index)
			return
		}
		if _, err := fs.Stat(sub, caminho); err != nil {
			servirIndex(w, index)
			return
		}
		// Os arquivos embutidos não têm data de modificação, então o navegador não tem
		// como saber que mudaram e passa a servir a versão em cache indefinidamente —
		// atualizar o binário não atualizava o painel. Como são poucos kilobytes,
		// revalidar sempre custa menos que a confusão de ver uma tela antiga.
		w.Header().Set("Cache-Control", "no-store")
		servidor.ServeHTTP(w, r)
	}), nil
}

func servirIndex(w http.ResponseWriter, index []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// O painel só consome a própria API e não carrega nada de fora.
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; img-src 'self' data: http: https:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'")
	http.ServeContent(w, &http.Request{Method: http.MethodGet}, "index.html", time.Time{}, strings.NewReader(string(index)))
}
