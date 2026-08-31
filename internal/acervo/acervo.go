// Package acervo decide o que guardar, de onde ler e o que apagar.
//
// # A divisão de trabalho
//
// O pacote `armazenamento` sabe GRAVAR e LER em algum lugar. Este sabe QUANDO fazer isso, e
// em qual lugar. A separação existe porque a segunda parte muda muito mais que a primeira:
// gravar num disco é a mesma coisa hoje e daqui a dois anos, mas "guardar o quê" já mudou
// três vezes antes de existir.
//
// # O que este pacote garante ao resto do sistema
//
// Que servir do acervo NUNCA é pior que servir da fonte. Toda função aqui devolve um erro
// que o chamador pode ignorar caindo no caminho antigo — não há estado intermediário em que
// o vídeo pare de sair porque o acervo teve um problema.
package acervo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"vodmanager/internal/armazenamento"
	"vodmanager/internal/cryptobox"
	"vodmanager/internal/store"
)

// capturasSimultaneas e quantas gravacoes cabem ao mesmo tempo.
//
// Tres. O numero vem da memoria, e nao de gosto: uma gravacao para a nuvem segura dezesseis
// megabytes de buffer de envio mais quatro de fila, entao tres sao sessenta megabytes no pior
// caso — um custo fixo que cabe em qualquer maquina.
//
// Passar disso nao melhora nada que se note: quem nao pegou vaga continua assistindo
// normalmente, e o filme entra na fila para o baixador pegar depois.
const capturasSimultaneas = 3

// ErrSemBackend: o arquivo aponta para um armazenamento que não está montado.
//
// Acontece de verdade: uma conta de nuvem desativada, um disco que não pôde ser criado na
// partida. Não é falha de programação, é estado do sistema — e o chamador trata voltando
// para a fonte.
var ErrSemBackend = errors.New("armazenamento indisponível para este arquivo")

// Servico é o acervo desta instalação.
type Servico struct {
	store    *store.Store
	registro *armazenamento.Registro
	crypto   *cryptobox.Box
	log      *slog.Logger
	// montarNuvem constrói o backend de uma conta. Injetada para que este pacote não
	// dependa de nenhum provedor específico — e para que o Drive possa existir sem que
	// nada aqui mude.
	montarNuvem MontadorDeNuvem
	// vagasDeCaptura limita quantas gravacoes acontecem ao mesmo tempo.
	//
	// Sem teto, cada reproducao nova abria uma gravacao — e cada gravacao para a nuvem
	// custa dezesseis megabytes de buffer de envio mais quatro de fila. Vinte estreias
	// simultaneas eram quatrocentos megabytes de uma vez, numa maquina que tambem roda o
	// banco. O processo ficava sem memoria e parava de responder: o painel nao abria e os
	// videos tambem nao.
	//
	// O cache e um ganho para amanha. Ele nunca pode custar o serviço de hoje.
	vagasDeCaptura chan struct{}
	// minimoDeVideo e o tamanho abaixo do qual uma copia nao e considerada filme.
	//
	// Precisa ser o MESMO limiar do proxy, e essa e a licao: o proxy tratava qualquer
	// resposta abaixo de cem megabytes como possivel aviso de manutencao e trocava de
	// fonte, enquanto o cache so recusava abaixo de um megabyte. Um aviso de dez segundos
	// com cinco megabytes passava — e era gravado como se fosse o filme.
	//
	// Dai em diante o acervo o servia NO LUGAR da fonte, para sempre: o player abria, tinha
	// duracao, e mostrava tela preta.
	minimoDeVideo int64

	// politica guarda a leitura mais recente das configuracoes.
	//
	// Ler a politica sao SEIS consultas ao banco, e ela e lida no caminho do primeiro byte
	// de toda reproducao — mais uma vez no adiantamento. Eram doze idas ao banco por filme
	// aberto, para responder perguntas cuja resposta muda quando alguem mexe numa tela.
	politicaMu    sync.RWMutex
	politicaCache Politica
	politicaEm    time.Time
}

// MontadorDeNuvem constrói o backend de uma conta a partir das credenciais dela.
//
// Recebe as credenciais já decifradas: quem lida com a chave mestra é este pacote, e não
// cada provedor. Um provedor a mais não pode significar mais um lugar por onde a chave
// passa.
type MontadorDeNuvem func(ctx context.Context, nuvem *store.Nuvem, credenciais []byte) (armazenamento.Backend, error)

// Opcoes monta o serviço.
type Opcoes struct {
	Store       *store.Store
	Registro    *armazenamento.Registro
	Crypto      *cryptobox.Box
	Log         *slog.Logger
	MontarNuvem MontadorDeNuvem
	// TamanhoMinimoDeVideo e o mesmo limiar que o proxy usa para reconhecer aviso de
	// manutencao. Zero cai no piso absoluto de plausibilidade.
	TamanhoMinimoDeVideo int64
}

// Novo cria o serviço do acervo.
func Novo(o Opcoes) *Servico {
	return &Servico{
		store: o.Store, registro: o.Registro, crypto: o.Crypto,
		log: o.Log, montarNuvem: o.MontarNuvem,
		vagasDeCaptura: make(chan struct{}, capturasSimultaneas),
		minimoDeVideo:  o.TamanhoMinimoDeVideo,
	}
}

// Backend devolve o armazenamento onde um arquivo está.
//
// Contas de nuvem são montadas SOB DEMANDA e ficam no registro: elas são cadastradas e
// removidas pelo painel em execução, e montar uma custa uma troca de token com o provedor.
// Montar a cada byte servido seria pagar essa ida e volta em todo pedido de reprodução.
func (s *Servico) Backend(ctx context.Context, arquivo *store.ArquivoGuardado) (armazenamento.Backend, error) {
	if arquivo.Backend == store.BackendLocal {
		b, ok := s.registro.Obter(armazenamento.ChaveLocal)
		if !ok {
			return nil, fmt.Errorf("%w: disco local", ErrSemBackend)
		}
		return b, nil
	}
	if arquivo.NuvemID == nil {
		return nil, fmt.Errorf("%w: arquivo na nuvem sem conta", ErrSemBackend)
	}
	return s.BackendDaNuvem(ctx, *arquivo.NuvemID)
}

// BackendDaNuvem monta (ou reaproveita) o backend de uma conta.
func (s *Servico) BackendDaNuvem(ctx context.Context, id int64) (armazenamento.Backend, error) {
	chave := armazenamento.ChaveDaNuvem(id)
	if b, ok := s.registro.Obter(chave); ok {
		return b, nil
	}
	if s.montarNuvem == nil {
		return nil, fmt.Errorf("%w: nenhum provedor de nuvem compilado neste binário", ErrSemBackend)
	}

	nuvem, err := s.store.NuvemPorID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("%w: conta %d: %v", ErrSemBackend, id, err)
	}
	if !nuvem.Ativa {
		// Desativada é decisão do administrador, não falha. O chamador cai na fonte.
		return nil, fmt.Errorf("%w: a conta %q está desativada", ErrSemBackend, nuvem.Nome)
	}

	blob, err := s.store.CredenciaisDaNuvem(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("%w: credenciais da conta %q: %v", ErrSemBackend, nuvem.Nome, err)
	}
	credenciais, err := s.crypto.Open(blob, cryptobox.NuvemAAD(nuvem.Nome))
	if err != nil {
		// Chave mestra trocada, ou linha movida entre contas. Registrar na própria conta
		// é o que faz a tela mostrar o motivo em vez de "não funciona".
		_ = s.store.AnotarErroDaNuvem(ctx, id, "as credenciais não puderam ser decifradas com a chave atual")
		return nil, fmt.Errorf("%w: credenciais ilegíveis da conta %q", ErrSemBackend, nuvem.Nome)
	}

	b, err := s.montarNuvem(ctx, nuvem, credenciais)
	// A cópia decifrada some da memória assim que o backend a consumiu.
	for i := range credenciais {
		credenciais[i] = 0
	}
	if err != nil {
		_ = s.store.AnotarErroDaNuvem(ctx, id, err.Error())
		return nil, fmt.Errorf("%w: conta %q: %v", ErrSemBackend, nuvem.Nome, err)
	}

	s.registro.Guardar(chave, b)
	return b, nil
}

// Abrir devolve o conteúdo de um arquivo guardado, a partir de um deslocamento.
func (s *Servico) Abrir(ctx context.Context, arquivo *store.ArquivoGuardado, deslocamento int64) (io.ReadCloser, error) {
	b, err := s.Backend(ctx, arquivo)
	if err != nil {
		return nil, err
	}
	return b.Abrir(ctx, arquivo.Localizador, deslocamento)
}

// CopiaPronta procura uma cópia utilizável de uma variante.
//
// Devolve nulo, sem erro, quando não há. Ausência é o caso normal — enquanto o acervo
// estiver desligado, é o único caso — e tratá-la como erro encheria o registro de linhas
// que não significam nada.
func (s *Servico) CopiaPronta(ctx context.Context, variantID int64) *store.ArquivoGuardado {
	a, err := s.store.ArquivoProntoDaVariante(ctx, variantID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("falha ao consultar o acervo", "variant_id", variantID, "erro", err)
		}
		return nil
	}
	return a
}

// RegistrarAcesso conta mais um uso, para a limpeza saber o que é pouco procurado.
//
// Erro só é registrado, nunca propagado: perder uma contagem piora uma decisão futura de
// limpeza; interromper a reprodução por causa dela seria muito pior.
func (s *Servico) RegistrarAcesso(ctx context.Context, id int64) {
	if err := s.store.RegistrarAcessoAoArquivo(ctx, id); err != nil {
		s.log.Warn("falha ao contar acesso ao acervo", "arquivo_id", id, "erro", err)
	}
}

// Politica é o estado das chaves que decidem se algo pode ser guardado.
type Politica struct {
	Ligado      bool
	Destino     string
	LimiteBytes int64
	IdadeMinima time.Duration
	// EspacoMinimoPct é a folga, em porcentagem, abaixo da qual não se guarda mais nada.
	EspacoMinimoPct int
	// ArquivarSempre desce o frio para a nuvem assim que passa a carencia, sem esperar o
	// disco apertar.
	ArquivarSempre bool
	// AdiantarNaNuvem manda o proximo episodio direto para a nuvem, poupando o disco de
	// uma copia que ninguem pediu ainda.
	AdiantarNaNuvem bool
}

// validadeDaPolitica e por quanto tempo a leitura das configuracoes vale sem reler.
//
// Cinco segundos. E configuracao: muda quando alguem clica em Salvar, e nao a cada segundo.
// Cinco segundos de atraso para uma mudanca de chave e imperceptivel; seis consultas ao banco
// por filme aberto, com o espectador esperando, nao e.
const validadeDaPolitica = 5 * time.Second

// PoliticaAtual le as configuracoes do acervo, reaproveitando a leitura recente.
func (s *Servico) PoliticaAtual(ctx context.Context) Politica {
	s.politicaMu.RLock()
	cache, em := s.politicaCache, s.politicaEm
	s.politicaMu.RUnlock()
	if time.Since(em) < validadeDaPolitica {
		return cache
	}

	p := s.lerPolitica(ctx)
	s.politicaMu.Lock()
	s.politicaCache, s.politicaEm = p, time.Now()
	s.politicaMu.Unlock()
	return p
}

// lerPolitica vai ao banco de verdade.
//
// Sem trava durante a leitura, de proposito: duas reproducoes simultaneas podem ler as
// configuracoes ao mesmo tempo e chegar ao mesmo resultado. O desperdicio e uma leitura
// repetida; a alternativa seria segurar todas as outras reproducoes atras de um mutex
// enquanto o banco responde, que e o oposto do que se quer aqui.
func (s *Servico) lerPolitica(ctx context.Context) Politica {
	p := Politica{
		Destino:         store.BackendLocal,
		IdadeMinima:     24 * time.Hour,
		EspacoMinimoPct: espacoMinimoPadrao,
	}

	if v, err := s.store.GetSetting(ctx, store.SettingCacheLigado, "false"); err == nil {
		p.Ligado = v == "true"
	}
	if v, err := s.store.GetSetting(ctx, store.SettingCacheBackend, store.BackendLocal); err == nil && v != "" {
		p.Destino = v
	}
	if v, err := s.store.GetSetting(ctx, store.SettingCacheLimiteBytes, "0"); err == nil {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			p.LimiteBytes = n
		}
	}
	if v, err := s.store.GetSetting(ctx, store.SettingCacheIdadeMinimaHoras, "24"); err == nil {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			p.IdadeMinima = time.Duration(n) * time.Hour
		}
	}
	if v, err := s.store.GetSetting(ctx, store.SettingCacheArquivarSempre, "false"); err == nil {
		p.ArquivarSempre = v == "true"
	}
	if v, err := s.store.GetSetting(ctx, store.SettingCacheAdiantarNaNuvem, "false"); err == nil {
		p.AdiantarNaNuvem = v == "true"
	}
	if v, err := s.store.GetSetting(ctx, store.SettingCacheEspacoMinimoPct, ""); err == nil && v != "" {
		// Aceita de 0 a 90. Acima disso o cache nunca guardaria nada, e a configuração
		// viraria um jeito silencioso de desligá-lo — para isso já existe a chave geral.
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 90 {
			p.EspacoMinimoPct = n
		}
	}
	return p
}

// TalvezGuardar enfileira uma cópia, se as duas chaves permitirem.
//
// "Talvez" está no nome de propósito: esta função é chamada do caminho de reprodução, e
// nada do que ela decide pode impedir o vídeo de sair. Ela não devolve erro — o pior que
// acontece é a cópia não ser feita, e o sistema seguir intermediando como sempre fez.
//
// # Por que na reprodução, e não em massa
//
// Baixar o catálogo inteiro seriam dezenas de terabytes, quase todos de filmes que ninguém
// vai pedir. Guardando o que É assistido, o acervo se enche sozinho com exatamente a parte
// que importa — e o primeiro espectador de cada filme paga a banda que os seguintes
// economizam.
func (s *Servico) TalvezGuardar(ctx context.Context, v *store.PlayableVariant, alvo *store.StreamTarget) {
	pol := s.PoliticaAtual(ctx)
	if !pol.Ligado {
		return
	}

	fonte, err := s.store.GetSource(ctx, v.SourceID)
	if err != nil || !fonte.CacheHabilitado {
		return
	}

	novo := store.NovoArquivo{
		VariantID:    &v.ID,
		TargetKind:   alvo.Kind,
		TargetID:     alvo.ID,
		Backend:      pol.Destino,
		Origem:       store.OrigemFonte,
		ContainerExt: v.ContainerExt,
	}

	if pol.Destino == store.BackendNuvem {
		nuvem, err := s.store.NuvemParaGravar(ctx, 0)
		if err != nil {
			// Sem conta gravável não há para onde ir. Cair para o disco seria surpreender
			// quem configurou a nuvem justamente por não ter disco.
			s.log.Warn("acervo na nuvem pedido, mas nenhuma conta pode receber agora",
				"variant_id", v.ID)
			return
		}
		novo.NuvemID = &nuvem.ID
	}

	// A conferência de espaço vem ANTES de enfileirar, e não na hora de baixar. Enfileirar o
	// que não cabe encheria a fila de pedidos condenados a falhar, e o painel mostraria uma
	// lista de erros onde a verdade é simplesmente "o disco encheu".
	if destino, err := s.backendDaPolitica(ctx, pol, novo.NuvemID); err == nil {
		if !s.HaOndeGuardar(ctx, pol, pol.Destino, destino) {
			return
		}
	}

	arquivo, err := s.store.EnfileirarArquivo(ctx, novo)
	if err != nil {
		s.log.Warn("falha ao enfileirar cópia", "variant_id", v.ID, "erro", err)
		return
	}
	if arquivo.Estado == store.ArquivoPendente {
		s.log.Info("cópia enfileirada", "variant_id", v.ID, "arquivo_id", arquivo.ID,
			"destino", novo.Backend)
	}
}

// credenciaisJSON é o formato guardado no banco. Exposto para os provedores lerem.
type credenciaisJSON map[string]string

// LerCredenciais decodifica o blob já decifrado.
func LerCredenciais(bruto []byte) (map[string]string, error) {
	var c credenciaisJSON
	if err := json.Unmarshal(bruto, &c); err != nil {
		return nil, fmt.Errorf("credenciais em formato inesperado: %w", err)
	}
	return c, nil
}

// espacoMinimoPadrao é a folga exigida quando ninguém configurou nada: 10%.
//
// Não zero. Um armazenamento levado a 100% não é só um cache que parou de crescer — é um
// disco onde o banco de dados não consegue mais escrever e o serviço inteiro cai. A folga
// existe para proteger a máquina, não o cache.
const espacoMinimoPadrao = 10

// HaOndeGuardar decide se ainda cabe mais uma cópia.
//
// # Por que isto é a resposta certa para "quando encher, usar a fonte"
//
// A alternativa seria apagar acervo para caber a cópia nova. Mas a cópia nova é sempre a
// menos merecedora: é a única do conjunto que ninguém ainda pediu duas vezes. Sacrificar um
// filme com dez acessos por um com um seria piorar o cache por definição.
//
// Então, cheio, o sistema volta a intermediar da fonte — que é exatamente o que ele fazia
// antes de o cache existir. Nada quebra; só deixa de melhorar.
//
// Na dúvida devolve `true`. Um armazenamento que não sabe informar o próprio tamanho (é o
// caso das nuvens sem limite anunciado) não é motivo para desligar o cache.
// `backend` diz QUAL armazenamento esta sendo medido, e nao pode ser deduzido do destino
// configurado: a limpeza pergunta pelo disco local mesmo quando o destino padrao e a nuvem, e
// medir o teto na nuvem nesse caso responderia sobre o armazenamento errado.
func (s *Servico) HaOndeGuardar(ctx context.Context, pol Politica, backend string, destino armazenamento.Backend) bool {
	if pol.LimiteBytes > 0 {
		usado, err := s.store.BytesEmCache(ctx, backend)
		if err != nil {
			s.log.Warn("falha ao medir o cache; seguindo sem o limite", "erro", err)
		} else if usado >= pol.LimiteBytes {
			s.log.Info("limite do cache atingido; a fonte volta a ser usada",
				"usado", usado, "limite", pol.LimiteBytes)
			return false
		}
	}

	// A nuvem NÃO é consultada aqui.
	//
	// Esta função é chamada no caminho do primeiro byte, antes de o vídeo começar a sair. Um
	// `Espaco()` de conta de nuvem é uma requisição HTTP ao provedor — centenas de
	// milissegundos, às vezes segundos, com a pessoa olhando a tela parada. Multiplicado por
	// toda reprodução, foi isso que fez os vídeos pararem de abrir quando o destino padrão
	// virou a nuvem.
	//
	// E a pergunta já tem resposta sem sair daqui: `NuvemParaGravar` só devolve uma conta que
	// cabe, comparando com a medição gravada no banco — atualizada a cada quinze minutos por
	// quem pode esperar. Chegar até aqui com uma conta de nuvem já significa que ela cabe.
	//
	// O disco local continua sendo medido: `statfs` é uma chamada de sistema local, na casa
	// dos microssegundos, e não sai da máquina.
	if backend != store.BackendLocal {
		return true
	}

	esp, err := destino.Espaco(ctx)
	if err != nil || esp.Ilimitado || esp.Total <= 0 {
		return true
	}
	// Inteiro e não ponto flutuante: `livre*100/total` é exato e não depende de
	// arredondamento para decidir se o disco encheu.
	livrePct := esp.Livre * 100 / esp.Total
	if livrePct < int64(pol.EspacoMinimoPct) {
		s.log.Info("armazenamento perto do limite; a fonte volta a ser usada",
			"livre_pct", livrePct, "minimo_pct", pol.EspacoMinimoPct)
		return false
	}
	return true
}

// backendDaPolitica resolve o destino padrão sem precisar de um arquivo já registrado.
//
// Existe porque a conferência de espaço acontece antes de haver linha no banco — e `Backend`
// pede um `ArquivoGuardado`, que nesse instante ainda não existe.
func (s *Servico) backendDaPolitica(ctx context.Context, pol Politica, nuvemID *int64) (armazenamento.Backend, error) {
	if pol.Destino == store.BackendLocal {
		b, ok := s.registro.Obter(armazenamento.ChaveLocal)
		if !ok {
			return nil, fmt.Errorf("%w: disco local", ErrSemBackend)
		}
		return b, nil
	}
	if nuvemID == nil {
		return nil, fmt.Errorf("%w: destino na nuvem sem conta", ErrSemBackend)
	}
	return s.BackendDaNuvem(ctx, *nuvemID)
}

// CopiaProntaDeAlguma procura a cópia utilizável entre várias variantes, numa consulta só.
//
// A ordem das variantes é respeitada: elas chegam por prioridade de reprodução, e a cópia da
// fonte preferida deve ganhar da cópia de uma fonte de reserva.
func (s *Servico) CopiaProntaDeAlguma(ctx context.Context, variantIDs []int64) *store.ArquivoGuardado {
	a, err := s.store.ArquivoProntoDeAlguma(ctx, variantIDs)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("falha ao consultar o acervo", "erro", err)
		}
		return nil
	}
	return a
}

// MinimoDeVideo é o tamanho abaixo do qual uma cópia não é aceita como filme.
//
// Cai no piso absoluto de plausibilidade quando não há limiar configurado — e o piso nunca é
// ultrapassado para baixo: mesmo com a detecção de manutenção desligada, um megabyte continua
// não sendo um vídeo.
func (s *Servico) MinimoDeVideo() int64 {
	if s.minimoDeVideo > store.TamanhoMinimoDeVideo {
		return s.minimoDeVideo
	}
	return store.TamanhoMinimoDeVideo
}
