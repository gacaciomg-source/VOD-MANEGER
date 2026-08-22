// Package backup exporta e restaura o estado do sistema.
//
// Existe para uma situação concreta: trocar de máquina sem perder nada e sem depender de
// nenhuma ferramenta externa estar instalada — nem `pg_dump`, nem `psql`.
//
// Por que não usar pg_dump. Ele é excelente e seria menos código, mas cria um acoplamento
// que morde exatamente na hora errada: o arquivo gerado pelo pg_dump de uma versão do
// Postgres pode ser recusado pelo servidor de outra versão. Trocar de VPS costuma
// significar trocar de versão do Postgres junto — e descobrir isso durante a migração,
// com o sistema fora do ar, é o pior momento possível.
//
// O formato aqui é CSV dentro de um tar comprimido: legível por qualquer ferramenta,
// independente de versão, e gerado pela mesma conexão que a aplicação já tem aberta.
//
// # O que o backup NÃO contém
//
// A chave de criptografia. Ela fica fora, de propósito: guardar a chave junto do arquivo
// cifrado com ela anula a cifra. O manifesto guarda uma IMPRESSÃO da chave, para a
// restauração conseguir avisar quando a chave da máquina nova é outra — sem isso, o
// sistema restauraria em silêncio e só falharia depois, ao tentar usar as credenciais.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FormatoAtual é a versão do formato do arquivo de backup.
//
// Muda quando o conteúdo do arquivo deixa de ser interpretável pela versão anterior. A
// restauração recusa um formato que não conhece, em vez de tentar adivinhar.
const FormatoAtual = 1

// tabelas lista o que é salvo, NA ORDEM EM QUE PRECISA SER RESTAURADO.
//
// A ordem não é estética: é a ordem das chaves estrangeiras. Restaurar `episodes` antes de
// `seasons` falha, porque o episódio aponta para uma temporada que ainda não existe.
var tabelas = []string{
	"users",
	"settings",
	"sources",
	"source_credentials",
	"categories",
	"category_aliases",
	"source_categories",
	"contents",
	"seasons",
	"episodes",
	"source_variants",
	// O REGISTRO das cópias viaja; os arquivos, não.
	//
	// Parece contraditório salvar a ficha de um arquivo que ficou para trás, e não é: para
	// o acervo próprio guardado na nuvem, o localizador continua válido na máquina nova —
	// o Drive é o mesmo. E mesmo para o que estava em disco local, perder a ficha seria
	// perder a informação de que aquele acervo existiu, que é o que permite reenviá-lo.
	// O cache que não achar o arquivo se refaz sozinho.
	"arquivos_guardados",
	"unresolved_items",
	"match_decisions",
	"duplicatas_ignoradas",
	"sync_runs",
	"stream_credentials",
	"events",
}

// efemeras são tabelas deliberadamente ausentes do backup.
//
// `sessions` e `api_tokens` são logins abertos: restaurá-los numa máquina nova reabriria
// sessões que deveriam ter morrido junto com a máquina antiga. `streams` são reproduções
// em andamento, que por definição não estão em andamento depois de uma migração.
var efemeras = []string{"sessions", "api_tokens", "streams"}

// TabelasSalvas e TabelasEfemeras expõem as duas listas para a guarda que as compara com
// o banco de verdade.
//
// Existem por causa de um esquecimento concreto: duas migrações criaram tabelas
// (duplicatas_ignoradas e category_aliases) e ninguém as acrescentou aqui. O backup
// continuou sendo gerado com sucesso, a restauração continuou passando nos testes, e o
// estrago só apareceria numa migração de servidor — com as decisões do administrador
// perdidas em silêncio, que é a única forma de falha que um backup não pode ter.
//
// Uma tabela nova agora obriga a uma escolha explícita: ela é salva, ou é efêmera.
func TabelasSalvas() []string   { return append([]string(nil), tabelas...) }
func TabelasEfemeras() []string { return append([]string(nil), efemeras...) }

// Manifesto descreve o backup. É o primeiro arquivo do tar, para poder ser lido sem
// descompactar o resto.
type Manifesto struct {
	Formato        int            `json:"formato"`
	Versao         string         `json:"versao_do_sistema"`
	CriadoEm       time.Time      `json:"criado_em"`
	SchemaMigracao int64          `json:"schema_migracao"`
	Linhas         map[string]int `json:"linhas"`
	// ImpressaoChave identifica a chave de criptografia SEM revelá-la. Permite à
	// restauração avisar que a chave da máquina de destino é outra — o que tornaria as
	// credenciais das fontes e as senhas de saída ilegíveis.
	ImpressaoChave string   `json:"impressao_da_chave"`
	Tabelas        []string `json:"tabelas"`
	Observacao     string   `json:"observacao"`
}

// ImpressaoDaChave deriva um identificador público da chave mestra.
//
// É um HMAC de rótulo fixo com a chave: quem tem a chave reproduz o valor, quem tem só o
// valor não obtém a chave.
func ImpressaoDaChave(chave []byte) string {
	if len(chave) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, chave)
	mac.Write([]byte("vodmanager:impressao-da-chave:v1"))
	return hex.EncodeToString(mac.Sum(nil))[:16]
}

// Opcoes controla a geração do backup.
type Opcoes struct {
	Pool    *pgxpool.Pool
	Chave   []byte
	Versao  string
	Destino io.Writer
	// Log recebe o andamento. Nulo silencia.
	Log func(string, ...any)
}

// Gerar escreve um backup completo no destino.
func Gerar(ctx context.Context, o Opcoes) (*Manifesto, error) {
	registrar := o.Log
	if registrar == nil {
		registrar = func(string, ...any) {}
	}

	conn, err := o.Pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("backup: obtendo conexão: %w", err)
	}
	defer conn.Release()

	// Uma transação só, com instantâneo consistente: sem isso, uma sincronização rodando
	// durante o backup produziria um arquivo com metade do catálogo antigo e metade do
	// novo — inconsistente de um jeito que só apareceria na restauração.
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("backup: iniciando transação: %w", err)
	}
	defer tx.Rollback(ctx)

	man := &Manifesto{
		Formato:  FormatoAtual,
		Versao:   o.Versao,
		CriadoEm: time.Now().UTC(),
		Linhas:   map[string]int{},
		Tabelas:  tabelas,
		Observacao: "A CHAVE DE CRIPTOGRAFIA NÃO ESTÁ NESTE ARQUIVO. " +
			"Sem ela, as credenciais das fontes e as senhas de saída são irrecuperáveis. " +
			"Guarde a chave em outro lugar, separada deste arquivo.",
		ImpressaoChave: ImpressaoDaChave(o.Chave),
	}
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&man.SchemaMigracao); err != nil {
		return nil, fmt.Errorf("backup: lendo versão do schema: %w", err)
	}

	gz := gzip.NewWriter(o.Destino)
	tw := tar.NewWriter(gz)

	// Os dados das tabelas são gerados antes do manifesto porque o manifesto carrega a
	// contagem de linhas — e ela só é conhecida depois de copiar.
	dados := map[string][]byte{}
	for _, t := range tabelas {
		buf, linhas, err := copiarTabela(ctx, tx, t)
		if err != nil {
			return nil, err
		}
		dados[t] = buf
		man.Linhas[t] = linhas
		registrar("tabela exportada", "tabela", t, "linhas", linhas)
	}

	if err := escreverArquivo(tw, "manifesto.json", indentar(man)); err != nil {
		return nil, err
	}
	for _, t := range tabelas {
		if err := escreverArquivo(tw, "dados/"+t+".csv", dados[t]); err != nil {
			return nil, err
		}
	}
	if err := escreverArquivo(tw, "LEIA-ME.txt", []byte(leiaMe(man))); err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("backup: fechando arquivo: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("backup: fechando compressão: %w", err)
	}
	return man, nil
}

// copiarTabela exporta uma tabela em CSV usando COPY, que é o caminho mais rápido do
// Postgres para tirar dados — e o mesmo que o pg_dump usa por baixo.
func copiarTabela(ctx context.Context, tx pgx.Tx, tabela string) ([]byte, int, error) {
	var buf escritorContador
	_, err := tx.Conn().PgConn().CopyTo(ctx, &buf,
		fmt.Sprintf(`COPY (SELECT * FROM %s) TO STDOUT WITH (FORMAT csv, HEADER true)`, tabela))
	if err != nil {
		return nil, 0, fmt.Errorf("backup: exportando %s: %w", tabela, err)
	}
	linhas := buf.linhas - 1 // desconta o cabeçalho
	if linhas < 0 {
		linhas = 0
	}
	return buf.dados, linhas, nil
}

// escritorContador acumula os bytes e conta as linhas de uma vez só.
type escritorContador struct {
	dados  []byte
	linhas int
}

func (e *escritorContador) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			e.linhas++
		}
	}
	e.dados = append(e.dados, p...)
	return len(p), nil
}

func escreverArquivo(tw *tar.Writer, nome string, conteudo []byte) error {
	cab := &tar.Header{
		Name:    nome,
		Mode:    0o600,
		Size:    int64(len(conteudo)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(cab); err != nil {
		return fmt.Errorf("backup: escrevendo cabeçalho de %s: %w", nome, err)
	}
	if _, err := tw.Write(conteudo); err != nil {
		return fmt.Errorf("backup: escrevendo %s: %w", nome, err)
	}
	return nil
}

func indentar(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return append(b, '\n')
}

func leiaMe(m *Manifesto) string {
	return fmt.Sprintf(`BACKUP DO VOD MANAGER
=====================

Criado em: %s
Versão do sistema: %s
Versão do schema: %d
Impressão da chave de criptografia: %s

O QUE ESTE ARQUIVO CONTÉM
-------------------------
O catálogo inteiro, as fontes, as categorias, as credenciais (cifradas), os usuários do
painel e as configurações. Um arquivo CSV por tabela, dentro de dados/.

O QUE ELE NÃO CONTÉM — LEIA ISTO
--------------------------------
A CHAVE DE CRIPTOGRAFIA (VODM_ENCRYPTION_KEY) não está aqui, e isso é intencional:
guardá-la junto anularia a cifra.

Sem a chave, este backup restaura o catálogo mas NÃO as credenciais das suas fontes nem as
senhas de saída dos seus clientes — elas ficam ilegíveis para sempre.

Guarde a chave em outro lugar. Confira que ela é a certa comparando a impressão acima com
a que a restauração exibir.

Sessões abertas, tokens de API e reproduções em andamento não são salvos de propósito:
não fazem sentido em outra máquina.

COMO RESTAURAR
--------------
Na máquina nova, com o VOD Manager instalado e o VODM_ENCRYPTION_KEY já configurado:

    vodmanager restaurar --arquivo este-arquivo.tar.gz

A restauração recusa começar se a versão do schema for incompatível, e avisa em voz alta
se a chave de criptografia for diferente da usada no backup.
`, m.CriadoEm.Format(time.RFC3339), m.Versao, m.SchemaMigracao, m.ImpressaoChave)
}
