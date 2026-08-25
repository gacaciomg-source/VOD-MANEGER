package acervo

import (
	"context"
	"errors"
	"fmt"
	"io"

	"vodmanager/internal/armazenamento"
	"vodmanager/internal/store"
)

// A captura: guardar enquanto entrega.
//
// # O problema que ela resolve
//
// Sem ela, um filme desce da fonte DUAS vezes: uma para o espectador que o assistiu, outra
// depois, para o baixador. Duas conexões, duas vezes a banda, para os mesmos bytes.
//
// Com ela, a passagem do espectador É o download. Os bytes que já estão sendo entregues são
// escritos no acervo no caminho, e a segunda descida deixa de existir. Quem assiste um filme
// inédito paga a mesma espera de sempre — e ao terminar, o filme está guardado.
//
// # A regra que não pode ser quebrada
//
// O espectador NUNCA espera pelo armazenamento.
//
// É por isso que a captura não escreve no disco na mesma linha de execução que entrega ao
// player. Ela empurra os pedaços para uma fila curta, e uma rotina separada os grava. Se a
// gravação não acompanhar — disco ocupado, nuvem lenta —, a fila enche e a captura é
// ABANDONADA na hora, sem nenhum efeito sobre a transmissão.
//
// Abandonar é barato: o arquivo volta para a fila, e o baixador o pega depois no ritmo
// dele. O contrário — deixar a transmissão travar porque o disco engasgou — trocaria um
// download adiado por um filme travado, que é exatamente o defeito que o cache existe para
// remover.
//
// # Por que só do começo
//
// A captura só acontece quando o pedido começa no byte zero. Um pedido com `Range` no meio
// produziria um arquivo que começa no minuto quarenta — inútil como cópia e perigoso se
// alguém o servisse achando que está inteiro.

// pedacosNaFila é o tamanho da fila entre quem entrega e quem grava.
//
// Dezesseis pedaços de 256 KB são 4 MB de folga: o bastante para absorver uma pausa de
// escrita, pouco o bastante para não virar consumo de memória por espectador. Cheia, a
// captura desiste — o limite existe justamente para ter um ponto onde desistir.
const pedacosNaFila = 16

// Captura é uma cópia sendo gravada a partir de uma transmissão em curso.
type Captura struct {
	servico  *Servico
	arquivo  *store.ArquivoGuardado
	pedacos  chan []byte
	fim      chan resultadoDaCaptura
	desistiu bool
	escritos int64
}

type resultadoDaCaptura struct {
	localizador string
	bytes       int64
	err         error
}

// TalvezCapturar decide se esta transmissão pode alimentar o acervo, e começa a gravação.
//
// Devolve nulo quando não — que é o caso comum e não é falha. O chamador segue entregando
// da fonte exatamente como fazia antes.
//
// O `inicio` é o deslocamento pedido pelo cliente: só zero serve.
func (s *Servico) TalvezCapturar(ctx context.Context, v *store.PlayableVariant,
	alvo *store.StreamTarget, inicio, tamanho int64, ext string) *Captura {

	if inicio != 0 || tamanho <= 0 {
		return nil
	}
	pol := s.PoliticaAtual(ctx)
	if !pol.Ligado {
		return nil
	}
	fonte, err := s.store.GetSource(ctx, v.SourceID)
	if err != nil || !fonte.CacheHabilitado {
		return nil
	}

	novo := store.NovoArquivo{
		VariantID:    &v.ID,
		TargetKind:   alvo.Kind,
		TargetID:     alvo.ID,
		Backend:      pol.Destino,
		Origem:       store.OrigemFonte,
		ContainerExt: ext,
	}
	if pol.Destino == store.BackendNuvem {
		nuvem, err := s.store.NuvemParaGravar(ctx, tamanho)
		if err != nil {
			return nil
		}
		novo.NuvemID = &nuvem.ID
	}

	destino, err := s.backendDaPolitica(ctx, pol, novo.NuvemID)
	if err != nil || !s.HaOndeGuardar(ctx, pol, destino) {
		return nil
	}

	arquivo, err := s.store.EnfileirarArquivo(ctx, novo)
	if err != nil {
		return nil
	}
	// Já pronto, já baixando, ou tomado por outro espectador do mesmo filme neste instante.
	// A reivindicação é uma troca de estado condicional no banco: só um ganha, e os outros
	// seguem entregando da fonte sem gravar nada.
	if arquivo.Estado != store.ArquivoPendente {
		return nil
	}
	if ok, err := s.store.ReivindicarParaCaptura(ctx, arquivo.ID); err != nil || !ok {
		return nil
	}
	if err := s.store.AnotarTamanhoTotal(ctx, arquivo.ID, tamanho); err != nil {
		s.log.Warn("falha ao anotar o tamanho da captura", "arquivo_id", arquivo.ID, "erro", err)
	}

	nome := alvo.Title
	if nome == "" {
		nome = fmt.Sprintf("acervo-%d", arquivo.ID)
	}
	if ext != "" {
		nome += "." + ext
	}

	c := &Captura{
		servico: s,
		arquivo: arquivo,
		pedacos: make(chan []byte, pedacosNaFila),
		fim:     make(chan resultadoDaCaptura, 1),
	}

	// O contexto é o de fundo, e não o da requisição: a gravação precisa poder terminar de
	// escrever o que já recebeu mesmo depois de o player fechar.
	go c.gravar(context.WithoutCancel(ctx), destino, nome, tamanho)

	s.log.Info("captura iniciada", "arquivo_id", arquivo.ID, "variant_id", v.ID,
		"tamanho", tamanho, "destino", pol.Destino)
	return c
}

// gravar consome a fila e escreve no armazenamento. Roda numa rotina própria.
func (c *Captura) gravar(ctx context.Context, destino armazenamentoDeCaptura, nome string, tamanho int64) {
	// O andamento é gravado desta rotina, e nunca da que entrega ao player: uma escrita no
	// banco no caminho do vídeo seria a mesma pausa que a captura inteira existe para evitar.
	fonte := &leitorComProgresso{
		origem:  &leitorDaFila{pedacos: c.pedacos},
		anotar:  func(n int64) { _ = c.servico.store.AnotarProgresso(ctx, c.arquivo.ID, n) },
		periodo: intervaloDoProgresso,
	}

	local, err := destino.Guardar(ctx, nome, fonte, tamanho)
	if err != nil {
		c.fim <- resultadoDaCaptura{err: err}
		return
	}
	c.fim <- resultadoDaCaptura{localizador: local.Localizador, bytes: local.Bytes}
}

// Write recebe os bytes que estão indo para o espectador.
//
// Nunca bloqueia e nunca devolve erro: ela é chamada de dentro da cópia que alimenta o
// player, e qualquer uma das duas coisas viraria uma pausa no vídeo. Quando a fila está
// cheia, a captura desiste e as escritas seguintes não fazem nada.
func (c *Captura) Write(p []byte) (int, error) {
	if c.desistiu {
		return len(p), nil
	}
	// A cópia é obrigatória: `p` é o buffer reaproveitado pela transferência, e o conteúdo
	// dele muda antes de a gravação chegar a lê-lo.
	pedaco := make([]byte, len(p))
	copy(pedaco, p)

	select {
	case c.pedacos <- pedaco:
		c.escritos += int64(len(p))
	default:
		// A gravação não acompanha. Desistir aqui é a decisão certa: o filme volta para a
		// fila e o baixador o pega depois, sem que ninguém veja nada travar.
		c.desistir()
	}
	return len(p), nil
}

func (c *Captura) desistir() {
	if c.desistiu {
		return
	}
	c.desistiu = true
	close(c.pedacos)
}

// Fechar encerra a captura e decide o destino da cópia.
//
// `completo` diz se a transmissão entregou tudo o que a fonte anunciou. Só nesse caso a
// cópia vira acervo: metade de um filme guardado como pronto seria servido no lugar da
// fonte, e o espectador veria o corte toda vez.
func (c *Captura) Fechar(completo bool) {
	jaDesistiu := c.desistiu
	c.desistir()

	res := <-c.fim
	ctx := context.Background()
	s := c.servico

	falhou := jaDesistiu || !completo || res.err != nil
	if !falhou && res.bytes != c.escritos {
		// A gravação recebeu menos do que passou pelo cliente. Não deveria acontecer, e
		// justamente por isso não pode ser ignorado.
		falhou = true
	}

	if falhou {
		if res.localizador != "" {
			if d, err := s.Backend(ctx, c.arquivo); err == nil {
				_ = d.Apagar(ctx, res.localizador)
			}
		}
		// Volta para a fila em vez de virar erro: não houve falha da fonte, houve um
		// espectador que fechou o player ou um disco que não acompanhou. O baixador pega
		// depois, no ritmo dele.
		if err := s.store.DevolverAFila(ctx, c.arquivo.ID); err != nil {
			s.log.Warn("falha ao devolver a captura à fila", "arquivo_id", c.arquivo.ID, "erro", err)
		}
		if res.err != nil && !errors.Is(res.err, io.ErrClosedPipe) {
			s.log.Info("captura descartada", "arquivo_id", c.arquivo.ID, "erro", res.err)
		}
		return
	}

	if err := s.store.ConcluirArquivo(ctx, c.arquivo.ID, res.localizador, res.bytes); err != nil {
		s.log.Warn("falha ao concluir a captura", "arquivo_id", c.arquivo.ID, "erro", err)
		return
	}
	s.log.Info("captura concluída — o filme foi guardado enquanto era assistido",
		"arquivo_id", c.arquivo.ID, "bytes", res.bytes)
}

// armazenamentoDeCaptura é o pedaço do backend que a captura usa.
type armazenamentoDeCaptura = armazenamento.Backend

// leitorDaFila transforma o canal de pedaços num io.Reader, que é o que os backends pedem.
type leitorDaFila struct {
	pedacos chan []byte
	atual   []byte
}

func (l *leitorDaFila) Read(p []byte) (int, error) {
	for len(l.atual) == 0 {
		pedaco, ok := <-l.pedacos
		if !ok {
			return 0, io.EOF
		}
		l.atual = pedaco
	}
	n := copy(p, l.atual)
	l.atual = l.atual[n:]
	return n, nil
}

// TalvezAdiantarProximo enfileira o episódio seguinte ao que acabou de ser assistido.
//
// # Por que isto existe
//
// O cache guarda o que JÁ foi assistido — e em série isso é exatamente o episódio que o
// espectador não vai repetir. Terminado o 50, ele abre o 51, que é o único que ninguém tem.
// Sozinho, o cache chega sempre um episódio atrasado.
//
// Adiantando, a conta se inverte: quando o espectador chega ao 51, ele já está no disco.
//
// # Um só, e não a temporada inteira
//
// Adiantar dez episódios seria baixar dezenas de gigabytes por uma aposta sobre alguém que
// pode largar a série no próximo. Um episódio é a aposta que se paga sozinha na primeira
// troca — e a que custa pouco quando erra.
//
// Não devolve erro de propósito: nada aqui pode afetar quem está assistindo. Falhar em
// adiantar significa apenas que o espectador vai esperar o download do 51, que é exatamente
// o que acontecia antes.
func (s *Servico) TalvezAdiantarProximo(ctx context.Context, alvo *store.StreamTarget) {
	if alvo == nil || alvo.Kind != store.TargetEpisode || alvo.EpisodeID == nil {
		return
	}
	pol := s.PoliticaAtual(ctx)
	if !pol.Ligado {
		return
	}

	proximoID, err := s.store.ProximoEpisodio(ctx, *alvo.EpisodeID)
	if err != nil {
		// Último episódio da série é o caso normal aqui, e não tem o que registrar.
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("falha ao procurar o próximo episódio",
				"episode_id", *alvo.EpisodeID, "erro", err)
		}
		return
	}

	proximoAlvo, variantes, err := s.store.ResolveEpisodeForStream(ctx, proximoID)
	if err != nil || len(variantes) == 0 {
		return
	}

	// A primeira variante é a que a reprodução usaria: a lista já vem na ordem de
	// prioridade. Adiantar por outra guardaria um arquivo que não é o que seria servido.
	v := variantes[0]
	fonte, err := s.store.GetSource(ctx, v.SourceID)
	if err != nil || !fonte.CacheHabilitado {
		return
	}

	novo := store.NovoArquivo{
		VariantID:    &v.ID,
		TargetKind:   proximoAlvo.Kind,
		TargetID:     proximoAlvo.ID,
		Backend:      pol.Destino,
		Origem:       store.OrigemFonte,
		ContainerExt: v.ContainerExt,
	}
	if pol.Destino == store.BackendNuvem {
		nuvem, err := s.store.NuvemParaGravar(ctx, 0)
		if err != nil {
			return
		}
		novo.NuvemID = &nuvem.ID
	}

	destino, err := s.backendDaPolitica(ctx, pol, novo.NuvemID)
	if err != nil || !s.HaOndeGuardar(ctx, pol, destino) {
		return
	}

	arquivo, err := s.store.EnfileirarArquivo(ctx, novo)
	if err != nil {
		s.log.Warn("falha ao adiantar o próximo episódio", "episode_id", proximoID, "erro", err)
		return
	}
	if arquivo.Estado != store.ArquivoPendente {
		// Já está pronto ou já está descendo. Nada a fazer, e é o desfecho desejável.
		return
	}
	if err := s.store.MarcarAdiantado(ctx, arquivo.ID); err != nil {
		s.log.Warn("falha ao marcar a cópia como adiantada", "arquivo_id", arquivo.ID, "erro", err)
	}
	s.log.Info("próximo episódio adiantado",
		"episode_id", proximoID, "arquivo_id", arquivo.ID, "titulo", proximoAlvo.Title)
}
