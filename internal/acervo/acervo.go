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
	"time"

	"vodmanager/internal/armazenamento"
	"vodmanager/internal/cryptobox"
	"vodmanager/internal/store"
)

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
}

// Novo cria o serviço do acervo.
func Novo(o Opcoes) *Servico {
	return &Servico{
		store: o.Store, registro: o.Registro, crypto: o.Crypto,
		log: o.Log, montarNuvem: o.MontarNuvem,
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
}

// PoliticaAtual lê as configurações do acervo.
func (s *Servico) PoliticaAtual(ctx context.Context) Politica {
	p := Politica{Destino: store.BackendLocal, IdadeMinima: 24 * time.Hour}

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
