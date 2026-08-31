package armazenamento

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Local guarda os arquivos numa pasta do disco desta máquina.
//
// # A pasta é um espaço fechado
//
// O localizador é sempre um nome de arquivo relativo à raiz, e nunca sai dela. Isso não é
// zelo abstrato: o localizador vem do banco, e o banco recebe dados que passaram por
// upload, por nome de arquivo de fonte e por URL. Um localizador com `../` transformaria a
// limpeza de cache numa forma de apagar arquivos do sistema. A conferência é feita em toda
// operação, e não só na gravação — quem grava e quem lê podem ser versões diferentes do
// código, e o banco sobrevive às duas.
type Local struct {
	raiz string
	// reservaBytes é o espaço que nunca será ocupado por cache.
	//
	// Um disco 100% cheio não deixa o Postgres escrever, e o sintoma disso não é "o cache
	// encheu" — é o sistema inteiro parando. A reserva é o que separa "o cache atingiu o
	// limite", que é rotina, de "a máquina travou", que é madrugada.
	reservaBytes int64
}

// NovoLocal cria o backend de disco, garantindo que a pasta exista.
func NovoLocal(raiz string, reservaBytes int64) (*Local, error) {
	if strings.TrimSpace(raiz) == "" {
		return nil, errors.New("armazenamento local: a pasta não foi informada")
	}
	abs, err := filepath.Abs(raiz)
	if err != nil {
		return nil, fmt.Errorf("armazenamento local: %w", err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return nil, fmt.Errorf("armazenamento local: criando %s: %w", abs, err)
	}
	if reservaBytes < 0 {
		reservaBytes = 0
	}
	return &Local{raiz: abs, reservaBytes: reservaBytes}, nil
}

// Nome identifica este backend.
func (l *Local) Nome() string { return "local" }

// Raiz devolve a pasta usada. Serve à tela de sistema, que informa onde o acervo está.
func (l *Local) Raiz() string { return l.raiz }

// Guardar grava o conteúdo num arquivo novo dentro da pasta.
func (l *Local) Guardar(ctx context.Context, sugestao string, conteudo io.Reader, bytesEsperados int64) (Localizacao, error) {
	if bytesEsperados > 0 {
		esp, err := l.Espaco(ctx)
		if err == nil && !esp.Ilimitado && esp.Livre < bytesEsperados {
			return Localizacao{}, fmt.Errorf("%w: precisa de %d bytes, há %d livres",
				ErrSemEspaco, bytesEsperados, esp.Livre)
		}
	}

	nome := nomeSeguro(sugestao)
	caminho := filepath.Join(l.raiz, nome)

	// Grava num arquivo temporário e só renomeia no fim.
	//
	// Um download interrompido pela metade não pode ficar no lugar do bom, com nome
	// válido: quem o lesse depois entregaria meio filme e um erro de rede — e o registro
	// no banco diria "pronto". O rename é atômico no mesmo sistema de arquivos, então ou
	// o arquivo aparece inteiro ou não aparece.
	parcial := caminho + ".parcial"
	f, err := os.OpenFile(parcial, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return Localizacao{}, fmt.Errorf("armazenamento local: criando %s: %w", parcial, err)
	}

	escritos, err := copiarComContexto(ctx, f, conteudo)
	fecharErr := f.Close()
	if err == nil {
		err = fecharErr
	}
	if err != nil {
		os.Remove(parcial)
		if errors.Is(err, syscall.ENOSPC) {
			return Localizacao{}, fmt.Errorf("%w: %v", ErrSemEspaco, err)
		}
		return Localizacao{}, fmt.Errorf("armazenamento local: gravando %s: %w", nome, err)
	}

	if err := os.Rename(parcial, caminho); err != nil {
		os.Remove(parcial)
		return Localizacao{}, fmt.Errorf("armazenamento local: finalizando %s: %w", nome, err)
	}
	return Localizacao{Localizador: nome, Bytes: escritos}, nil
}

// Abrir devolve o arquivo a partir do deslocamento pedido.
func (l *Local) Abrir(_ context.Context, localizador string, deslocamento int64) (io.ReadCloser, error) {
	caminho, err := l.caminhoDe(localizador)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(caminho)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNaoEncontrado, localizador)
		}
		return nil, fmt.Errorf("armazenamento local: abrindo %s: %w", localizador, err)
	}
	if deslocamento > 0 {
		if _, err := f.Seek(deslocamento, io.SeekStart); err != nil {
			f.Close()
			return nil, fmt.Errorf("armazenamento local: posicionando em %d: %w", deslocamento, err)
		}
	}
	return f, nil
}

// Apagar remove o arquivo. Apagar o que não existe não é erro.
func (l *Local) Apagar(_ context.Context, localizador string) error {
	caminho, err := l.caminhoDe(localizador)
	if err != nil {
		return err
	}
	if err := os.Remove(caminho); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("armazenamento local: apagando %s: %w", localizador, err)
	}
	return nil
}

// caminhoDe resolve o localizador dentro da raiz, recusando qualquer coisa que escape.
//
// A conferência é feita depois de resolver os links simbólicos e o `..`, e não sobre o
// texto: `a/../../etc/passwd` parece inocente caractere a caractere.
func (l *Local) caminhoDe(localizador string) (string, error) {
	if localizador == "" {
		return "", fmt.Errorf("%w: localizador vazio", ErrNaoEncontrado)
	}
	limpo := filepath.Clean(filepath.Join(l.raiz, localizador))
	if limpo != l.raiz && !strings.HasPrefix(limpo, l.raiz+string(os.PathSeparator)) {
		return "", fmt.Errorf("armazenamento local: localizador fora da pasta: %q", localizador)
	}
	return limpo, nil
}

// nomeSeguro transforma uma sugestão qualquer num nome de arquivo utilizável.
//
// O sufixo aleatório não é enfeite: as sugestões vêm de títulos de catálogo, e dois filmes
// com o mesmo nome existem o tempo todo. Sem ele, o segundo sobrescreveria o primeiro — e
// o banco continuaria apontando os dois para o mesmo arquivo.
func nomeSeguro(sugestao string) string {
	base := filepath.Base(strings.TrimSpace(sugestao))
	ext := filepath.Ext(base)
	base = strings.TrimSuffix(base, ext)

	limpo := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, base)
	limpo = strings.Trim(limpo, "-.")
	if limpo == "" {
		limpo = "arquivo"
	}
	if len(limpo) > 80 {
		limpo = limpo[:80]
	}

	extLimpa := strings.ToLower(strings.TrimPrefix(ext, "."))
	if len(extLimpa) > 8 || strings.ContainsAny(extLimpa, `/\.`) {
		extLimpa = ""
	}

	var aleatorio [6]byte
	_, _ = rand.Read(aleatorio[:])
	nome := limpo + "-" + hex.EncodeToString(aleatorio[:])
	if extLimpa != "" {
		nome += "." + extLimpa
	}
	return nome
}

// copiarComContexto copia respeitando o cancelamento.
//
// io.Copy sozinho não olha o contexto: um espectador que desiste, ou um desligamento do
// serviço, deixaria a cópia correndo até o fim do arquivo. Num vídeo de 2 GB puxado de uma
// fonte lenta, isso é muito tempo gastando banda que já não serve a ninguém.
func copiarComContexto(ctx context.Context, destino io.Writer, origem io.Reader) (int64, error) {
	const pedaco = 256 << 10
	buf := make([]byte, pedaco)
	var total int64
	for {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
		n, err := origem.Read(buf)
		if n > 0 {
			escritos, errEscrita := destino.Write(buf[:n])
			total += int64(escritos)
			if errEscrita != nil {
				return total, errEscrita
			}
		}
		if errors.Is(err, io.EOF) {
			return total, nil
		}
		if err != nil {
			return total, err
		}
	}
}

// ParcialDe informa quantos bytes de uma cópia interrompida já estão no disco.
//
// Zero quando não há nada — que é o caso normal, e não é erro.
//
// Existe para a retomada: as fontes deste sistema cortam a entrega no meio o tempo todo, e
// recomeçar um filme de dois gigabytes do zero a cada corte gasta a banda que o cache existe
// para economizar.
func (l *Local) ParcialDe(sugestao string) int64 {
	info, err := os.Stat(filepath.Join(l.raiz, nomeEstavel(sugestao)+".parcial"))
	if err != nil {
		return 0
	}
	return info.Size()
}

// DescartarParcial apaga uma cópia interrompida.
//
// Chamada quando a retomada não é possível — a fonte ignorou a faixa, ou o arquivo mudou de
// tamanho. Continuar sobre bytes que não correspondem produziria um vídeo corrompido que
// PARECE completo, e essa é a única falha aqui que ninguém descobriria.
func (l *Local) DescartarParcial(sugestao string) {
	_ = os.Remove(filepath.Join(l.raiz, nomeEstavel(sugestao)+".parcial"))
}

// Continuar acrescenta ao parcial existente e finaliza quando o total for atingido.
//
// # Por que uma função separada de Guardar
//
// Guardar sempre começa do zero — é o que se quer na maioria das vezes, e misturar os dois
// numa função só faria "começar de novo" e "continuar" dependerem de um parâmetro que alguém
// vai errar. Aqui a intenção está no nome.
//
// O arquivo temporário é o MESMO da gravação normal, e é isso que permite alternar entre os
// dois caminhos sem conversão: uma cópia começada por Guardar é continuada por Continuar.
func (l *Local) Continuar(ctx context.Context, sugestao string, conteudo io.Reader,
	bytesEsperados, jaGravados int64) (Localizacao, error) {

	if bytesEsperados > 0 {
		esp, err := l.Espaco(ctx)
		if err == nil && !esp.Ilimitado && esp.Livre < bytesEsperados-jaGravados {
			return Localizacao{}, fmt.Errorf("%w: faltam %d bytes, há %d livres",
				ErrSemEspaco, bytesEsperados-jaGravados, esp.Livre)
		}
	}

	// Nome ESTAVEL, e nao aleatorio: a retomada precisa reencontrar o parcial que a
	// tentativa anterior deixou, e um sufixo aleatorio o esconderia dela mesma.
	nome := nomeEstavel(sugestao)
	caminho := filepath.Join(l.raiz, nome)
	parcial := caminho + ".parcial"

	f, err := os.OpenFile(parcial, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return Localizacao{}, fmt.Errorf("armazenamento local: continuando %s: %w", parcial, err)
	}

	escritos, err := copiarComContexto(ctx, f, conteudo)
	fecharErr := f.Close()
	if err == nil {
		err = fecharErr
	}
	total := jaGravados + escritos

	if err != nil {
		// O parcial FICA, ao contrário do Guardar. É exatamente o que torna a retomada
		// possível: o próximo trabalhador continua daqui em vez de recomeçar.
		if errors.Is(err, syscall.ENOSPC) {
			return Localizacao{}, fmt.Errorf("%w: %v", ErrSemEspaco, err)
		}
		return Localizacao{}, fmt.Errorf("armazenamento local: gravando %s: %w", nome, err)
	}

	if err := os.Rename(parcial, caminho); err != nil {
		return Localizacao{}, fmt.Errorf("armazenamento local: finalizando %s: %w", nome, err)
	}
	return Localizacao{Localizador: nome, Bytes: total}, nil
}

// nomeEstavel produz o mesmo nome toda vez, para a mesma sugestão.
//
// `nomeSeguro` acrescenta um sufixo aleatório, e por um bom motivo: dois filmes com o mesmo
// título não podem virar o mesmo arquivo. Mas isso torna o nome IMPREVISÍVEL — e a retomada
// precisa reencontrar o parcial que a tentativa anterior deixou.
//
// Aqui a unicidade vem de fora: quem chama põe o id do registro na sugestão, e ele já é único
// por construção. O que sobra para esta função é limpar o que não pode ir para um nome de
// arquivo, de forma determinística.
func nomeEstavel(sugestao string) string {
	base := filepath.Base(strings.TrimSpace(sugestao))
	ext := filepath.Ext(base)
	base = strings.TrimSuffix(base, ext)

	limpo := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, base)
	limpo = strings.Trim(limpo, "-.")
	if limpo == "" {
		limpo = "arquivo"
	}
	if len(limpo) > 90 {
		limpo = limpo[:90]
	}

	extLimpa := strings.ToLower(strings.TrimPrefix(ext, "."))
	if len(extLimpa) > 8 || strings.ContainsAny(extLimpa, `/\.`) {
		return limpo
	}
	if extLimpa == "" {
		return limpo
	}
	return limpo + "." + extLimpa
}
