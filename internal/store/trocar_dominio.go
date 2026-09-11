package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"vodmanager/internal/ingest"
)

// Trocar o domínio de uma fonte em todo o catálogo, de uma vez.
//
// # Por que existe
//
// Fonte de IPTV troca de domínio sem aviso — bloqueio, migração de servidor, troca de
// revenda. Numa fonte Xtream isso é uma edição: o link do vídeo é montado na hora a partir
// do endereço da fonte. Numa M3U não: cada link guarda o domínio dentro dele, e são milhares.
// Sem isto, a saída era apagar a fonte e cadastrar de novo — perdendo o que o administrador
// decidiu sobre cada título — ou esperar a próxima sincronização criar tudo em duplicata.
//
// Enquanto isso, a fonte fica morta sem nada dizer, e o failover cai na próxima da lista:
// quem olha vê a SEGUNDA fonte falhando e procura o defeito no lugar errado. Foi assim que
// este caso apareceu.
//
// # A regra de casamento
//
// Troca só o DOMÍNIO — o trecho entre "://" e o que vem depois (porta, caminho) —, e só
// quando ele é exatamente o informado. "antigo.com" não pode casar com "antigo.com.br", nem
// com "sub.antigo.com": seria trocar a fonte errada, e em silêncio.

// ErrDominioInvalido é o domínio que não dá para usar com segurança.
var ErrDominioInvalido = errors.New("domínio inválido")

// formatoDeDominio é um host (nome ou IP), com porta opcional.
var formatoDeDominio = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)

// NormalizarDominio aceita o que a pessoa cola — com ou sem "http://", com caminho, com
// barra no fim — e devolve só o host, com a porta se ela foi escrita.
func NormalizarDominio(entrada string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(entrada))
	if s == "" {
		return "", fmt.Errorf("%w: vazio", ErrDominioInvalido)
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%w: %q", ErrDominioInvalido, entrada)
	}
	if !formatoDeDominio.MatchString(u.Host) {
		return "", fmt.Errorf("%w: %q", ErrDominioInvalido, entrada)
	}
	return u.Host, nil
}

// TrocaDeDominio é o resultado — ou, simulando, a prévia — da troca.
type TrocaDeDominio struct {
	De     string `json:"de"`
	Para   string `json:"para"`
	Fontes int64  `json:"fontes"`
	Links  int64  `json:"links"`
	// NomesDasFontes são as fontes com o endereço principal afetado, para a pessoa conferir
	// que é a fonte que ela pensa antes de confirmar.
	NomesDasFontes []string `json:"nomes_das_fontes"`
}

// TrocarDominio troca `de` por `para` no endereço das fontes e em todos os links.
//
// Com `simular`, só conta — é o que a tela mostra antes de a pessoa confirmar. Uma troca em
// massa errada é difícil de desfazer à mão, então a conferência vem antes, e não depois.
func (s *Store) TrocarDominio(ctx context.Context, deBruto, paraBruto string, simular bool) (*TrocaDeDominio, error) {
	de, err := NormalizarDominio(deBruto)
	if err != nil {
		return nil, err
	}
	para, err := NormalizarDominio(paraBruto)
	if err != nil {
		return nil, err
	}
	if de == para {
		return nil, fmt.Errorf("%w: o domínio novo é igual ao antigo", ErrDominioInvalido)
	}
	// Porta só de um lado quebraria o link. "antigo.com" casa com "http://antigo.com:8080/x"
	// — a porta fica, porque ela vem depois do domínio —, e trocar por "novo.com:9090"
	// daria "novo.com:9090:8080".
	if !strings.Contains(de, ":") && strings.Contains(para, ":") {
		return nil, fmt.Errorf("%w: o domínio novo tem porta e o antigo não. "+
			"Escreva a porta nos dois, ou em nenhum (aí a porta atual é mantida)", ErrDominioInvalido)
	}

	// O domínio exato, entre "://" e um separador (porta, caminho, consulta) ou o fim.
	padrao := `^([a-z][a-z0-9+.-]*://)` + regexp.QuoteMeta(de) + `(?=[:/?#]|$)`
	troca := `\1` + para

	res := &TrocaDeDominio{De: de, Para: para, NomesDasFontes: []string{}}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrapErr("trocando domínio", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx,
		`SELECT name FROM sources WHERE base_url ~* $1 ORDER BY priority, id`, padrao)
	if err != nil {
		return nil, wrapErr("trocando domínio", err)
	}
	for rows.Next() {
		var nome string
		if err := rows.Scan(&nome); err != nil {
			rows.Close()
			return nil, wrapErr("trocando domínio", err)
		}
		res.NomesDasFontes = append(res.NomesDasFontes, nome)
	}
	rows.Close()
	res.Fontes = int64(len(res.NomesDasFontes))

	if simular {
		err := tx.QueryRow(ctx,
			`SELECT count(*) FROM source_variants WHERE origin_url ~* $1`, padrao).Scan(&res.Links)
		return res, wrapErr("contando links do domínio", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE sources SET base_url = regexp_replace(base_url, $1, $2, 'i'), updated_at = now()
		WHERE base_url ~* $1`, padrao, troca); err != nil {
		return nil, wrapErr("trocando o domínio das fontes", err)
	}
	rows, err = tx.Query(ctx, `
		UPDATE source_variants SET origin_url = regexp_replace(origin_url, $1, $2, 'i'),
		       updated_at = now()
		WHERE origin_url ~* $1
		RETURNING id, origin_url, (external_id = '' AND url_hash <> '')`, padrao, troca)
	if err != nil {
		return nil, wrapErr("trocando o domínio dos links", err)
	}
	var ids []int64
	var hashes []string
	for rows.Next() {
		var id int64
		var novaURL string
		var identificadaPelaURL bool
		if err := rows.Scan(&id, &novaURL, &identificadaPelaURL); err != nil {
			rows.Close()
			return nil, wrapErr("trocando o domínio dos links", err)
		}
		res.Links++
		if !identificadaPelaURL {
			continue
		}
		if h, ok := ingest.HashURL(novaURL); ok {
			ids, hashes = append(ids, id), append(hashes, h)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, wrapErr("trocando o domínio dos links", err)
	}

	// A identidade dos links de M3U sem tvg-id É a URL — domínio incluído.
	//
	// Trocar a URL sem recalcular a identidade faria a próxima sincronização não reconhecer
	// nenhum deles: ela veria milhares de links "novos" e criaria o catálogo da fonte em
	// dobro, com os antigos órfãos — e as cópias do acervo presas a eles.
	//
	// A exceção é o link cuja identidade nova JÁ existe: uma sincronização rodou depois que a
	// fonte mudou de domínio e criou a versão nova antes da troca. Recalcular bateria na
	// restrição de unicidade e desfaria a troca inteira; deixar esse como está é inofensivo —
	// ele já aponta para o domínio novo, e a sincronização o trata como sempre.
	if len(ids) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE source_variants v SET url_hash = x.h
			FROM unnest($1::bigint[], $2::text[]) AS x(id, h)
			WHERE v.id = x.id
			  AND NOT EXISTS (
			      SELECT 1 FROM source_variants o
			      WHERE o.source_id = v.source_id AND o.external_id = ''
			        AND o.url_hash = x.h AND o.id <> v.id)`, ids, hashes); err != nil {
			return nil, wrapErr("recalculando a identidade dos links", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, wrapErr("confirmando a troca de domínio", err)
	}
	return res, nil
}
