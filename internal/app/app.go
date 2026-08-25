// Package app monta e executa a aplicação a partir de uma configuração já validada.
//
// Fica separado de cmd/ para que tanto o binário de produção quanto o de
// desenvolvimento subam exatamente a MESMA aplicação — sem código duplicado e sem o
// risco de o modo de desenvolvimento divergir do real.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"vodmanager/internal/acervo"
	"vodmanager/internal/api"
	"vodmanager/internal/armazenamento"
	"vodmanager/internal/auth"
	"vodmanager/internal/bootstrap"
	"vodmanager/internal/config"
	"vodmanager/internal/cryptobox"
	"vodmanager/internal/db"
	"vodmanager/internal/edge"
	"vodmanager/internal/ingest"
	"vodmanager/internal/metrics"
	"vodmanager/internal/roles"
	"vodmanager/internal/sources"
	"vodmanager/internal/store"
	vsync "vodmanager/internal/sync"
	"vodmanager/internal/sysinfo"
	"vodmanager/internal/transport"
)

// Run sobe a aplicação e bloqueia até o contexto ser cancelado ou o servidor cair.
func Run(ctx context.Context, cfg *config.Config, version string) error {
	log := NewLogger(cfg)
	log.Info("iniciando VOD Manager", "version", version, "config", cfg.Redacted())

	ehManager := cfg.Role == roles.RoleManager || cfg.Role == roles.RoleAll

	pool, err := db.Open(ctx, db.Options{
		DatabaseURL:     cfg.DatabaseURL,
		MaxConns:        cfg.DBMaxConns,
		ConnectTimeout:  cfg.DBConnectTimeout,
		ApplicationName: "vodmanager/" + cfg.NodeID,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	// Só o Manager aplica migrações. Um Node nunca altera o schema.
	if ehManager {
		aplicadas, err := db.Migrate(ctx, pool)
		if err != nil {
			return err
		}
		if len(aplicadas) > 0 {
			log.Info("migrações aplicadas", "versoes", aplicadas)
		} else {
			log.Info("schema já está atualizado")
		}
	}

	st := store.New(pool)
	box, err := cryptobox.New(cfg.EncryptionKey)
	if err != nil {
		return err
	}

	if ehManager {
		resultado, err := bootstrap.EnsureAdmin(ctx, st, log, cfg.BootstrapAdminUsername, cfg.BootstrapAdminPassword)
		if err != nil {
			return err
		}
		if resultado.GeneratedPassword != "" {
			log.Warn("SENHA INICIAL GERADA — anote agora, ela não será exibida novamente",
				"username", resultado.Username, "password", resultado.GeneratedPassword)
		}
	}

	var reg *metrics.Registry
	if cfg.MetricsEnabled {
		reg = metrics.New(cfg.NodeID, string(cfg.Role), version)
	}

	authSvc := auth.NewService(st, log, auth.Options{
		SessionTTL:       cfg.SessionTTL,
		LoginMaxAttempts: cfg.LoginMaxAttempts,
		LoginWindow:      cfg.LoginWindow,
	})

	// Sincronização é plano de controle: existe no Manager, não no Node.
	var scheduler *vsync.Scheduler
	if ehManager {
		normalizer, err := ingest.NewNormalizer()
		if err != nil {
			return err
		}
		orch := vsync.New(vsync.Options{
			Store:      st,
			Crypto:     box,
			Normalizer: normalizer,
			Providers: map[string]sources.Provider{
				"m3u":    transport.NewM3UProvider(),
				"xtream": transport.NewXtreamProvider(),
			},
			Log:    log,
			NodeID: cfg.NodeID,
		})
		scheduler = vsync.NewScheduler(orch, log)
	}

	// Medição de recursos da máquina: alimenta a tela de sistema do painel.
	sistema := sysinfo.NovoColetor()
	defer sistema.Fechar()

	// Armazenamento do acervo.
	//
	// O disco local é montado sempre, mesmo com o armazenamento desligado nas configurações:
	// desligado impede cópias NOVAS, não impede servir as que já existem. Um registro vazio
	// faria uma instalação que já tem acervo parar de entregá-lo no instante em que alguém
	// desmarcasse a caixa — e o sintoma seria "os filmes sumiram".
	//
	// As contas de nuvem não entram aqui: elas são cadastradas e removidas em execução, e
	// são montadas sob demanda a partir do banco.
	discoEContas := armazenamento.NovoRegistro()
	if disco, err := armazenamento.NovoLocal(cfg.ArmazenamentoLocal,
		int64(cfg.ArmazenamentoReservaGB)<<30); err != nil {
		// Sem disco utilizável o sistema continua de pé, intermediando como sempre fez.
		// Derrubar o serviço porque uma pasta não pôde ser criada trocaria um recurso a
		// menos por uma operação inteira fora do ar.
		log.Warn("armazenamento local indisponível; o acervo em disco fica desligado",
			"pasta", cfg.ArmazenamentoLocal, "erro", err)
	} else {
		discoEContas.Guardar(armazenamento.ChaveLocal, disco)
		log.Info("acervo em disco pronto", "pasta", disco.Raiz())
	}

	// O serviço do acervo: decide o que guardar, de onde ler e onde.
	servicoAcervo := acervo.Novo(acervo.Opcoes{
		Store:       st,
		Registro:    discoEContas,
		Crypto:      box,
		Log:         log,
		MontarNuvem: montarNuvem,
	})

	// Plano de dados: entrega os bytes de vídeo. Existe tanto no Manager quanto num Node.
	streamAuth := edge.NewAuthenticator(st, cfg.EncryptionKey)
	var streamProxy *edge.Proxy
	if scheduler != nil {
		// O resolvedor de URL de origem vive no orquestrador, que é quem conhece as
		// credenciais das fontes. Num Node puro ele virá por API interna (fase multi-node).
		streamProxy = edge.New(edge.Options{
			Store:    st,
			Auth:     streamAuth,
			Resolver: scheduler.Orchestrator(),
			Log:      log,
			NodeID:   cfg.NodeID,
			Acervo:   acervoParaEdge{servicoAcervo},
			// Em MB na configuracao, em bytes no codigo: ninguem escreve 20971520 num
			// arquivo de ambiente sem errar um zero.
			TamanhoMinimoDeVideo: int64(cfg.VideoMinimoMB) << 20,
		})
		// O consumo por credencial fica acumulado em memória entre as descargas; sem
		// isto, os últimos segundos de contagem se perderiam a cada desligamento.
		defer streamProxy.Fechar()
		// Sessões abertas por um processo que caiu ficariam "ativas" para sempre.
		if n, err := st.ReleaseActiveStreams(ctx, cfg.NodeID); err != nil {
			log.Warn("falha ao liberar sessões de stream", "erro", err)
		} else if n > 0 {
			log.Warn("sessões de stream liberadas na partida", "quantidade", n)
		}
	}

	server := api.NewServer(api.Deps{
		Store:         st,
		Auth:          authSvc,
		Crypto:        box,
		Sync:          scheduler,
		StreamAuth:    streamAuth,
		StreamProxy:   streamProxy,
		PublicBaseURL: cfg.PublicBaseURL,
		Log:           log,
		Metrics:       reg,
		NodeID:        cfg.NodeID,
		CookieName:    cfg.CookieName,
		CookieSecure:  cfg.CookieSecure,
		TrustProxy:    cfg.TrustProxy,
		Version:       version,
		Armazenamento: discoEContas,
		Sistema:       sistema,
	})
	apiModule := api.NewModule(server, cfg.HTTPAddr, cfg.ShutdownTimeout, log)

	// O baixador consome a fila de copias. Existe so no Manager, e so quando ha
	// sincronizacao — e o orquestrador que sabe montar a URL da fonte.
	var baixador *acervo.Baixador
	if scheduler != nil {
		baixador = acervo.NovoBaixador(servicoAcervo, st, scheduler.Orchestrator(), log)
	}

	registro := roles.NewRegistry()
	if err := registro.Register(apiModule); err != nil {
		return err
	}
	if scheduler != nil {
		if err := registro.Register(scheduler); err != nil {
			return err
		}
	}
	if baixador != nil {
		if err := registro.Register(baixador); err != nil {
			return err
		}
	}

	log.Info("módulos habilitados", "role", string(cfg.Role), "modulos", registro.Names(cfg.Role))
	if len(registro.Enabled(cfg.Role)) == 0 {
		return fmt.Errorf("nenhum módulo habilitado para o papel %q", cfg.Role)
	}
	if err := registro.StartAll(ctx, cfg.Role); err != nil {
		return err
	}

	go housekeeping(ctx, st, authSvc, log, cfg.NodeID)

	select {
	case <-ctx.Done():
		log.Info("sinal de encerramento recebido")
	case err := <-apiModule.Err():
		if err != nil {
			pararErr := registro.StopAll(context.Background())
			return errors.Join(err, pararErr)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := registro.StopAll(shutdownCtx); err != nil {
		return err
	}
	log.Info("encerrado com sucesso")
	return nil
}

// housekeeping roda as limpezas periódicas baratas.
func housekeeping(ctx context.Context, st *store.Store, authSvc *auth.Service, log *slog.Logger, nodeID string) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			authSvc.SweepLimiter()
			// Sessões que ficaram marcadas como ativas por tempo demais são fantasmas:
			// ocupam vaga no limite da credencial e poluem a tela de Reproduções.
			if n, err := st.ReleaseStaleStreams(ctx, nodeID, 12*time.Hour); err != nil {
				log.Warn("falha ao liberar sessões abandonadas", "erro", err)
			} else if n > 0 {
				log.Warn("sessões abandonadas encerradas", "quantidade", n)
			}
			if n, err := st.PurgeExpiredSessions(ctx, 24*time.Hour); err != nil {
				log.Warn("falha ao limpar sessões expiradas", "erro", err)
			} else if n > 0 {
				log.Info("sessões expiradas removidas", "quantidade", n)
			}
		}
	}
}

// NewLogger monta o logger conforme a configuração.
func NewLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var handler slog.Handler
	if cfg.LogFormat == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(handler).With("node_id", cfg.NodeID, "role", string(cfg.Role))
}

// montarNuvem constrói o backend de uma conta a partir das credenciais dela.
//
// É o único lugar do sistema que sabe quais provedores existem. O pacote `acervo` decide
// QUANDO montar; este decide COMO — e é por isso que acrescentar um provedor novo é um
// `case` aqui e um arquivo em `armazenamento`, sem tocar em mais nada.
//
// As credenciais chegam já decifradas: quem lida com a chave mestra é o serviço do acervo,
// e um provedor a mais não pode significar mais um lugar por onde a chave passa.
func montarNuvem(_ context.Context, nuvem *store.Nuvem, credenciais []byte) (armazenamento.Backend, error) {
	switch nuvem.Provedor {
	case store.ProvedorGDrive:
		var cred armazenamento.CredenciaisGDrive
		if err := json.Unmarshal(credenciais, &cred); err != nil {
			return nil, fmt.Errorf("credenciais da conta %q em formato inesperado", nuvem.Nome)
		}
		return armazenamento.NovoGDrive(nuvem.Nome, cred, nuvem.PastaRaiz)
	default:
		return nil, fmt.Errorf("provedor %q não é reconhecido por esta versão", nuvem.Provedor)
	}
}

// acervoParaEdge adapta o serviço do acervo à interface que o plano de dados espera.
//
// # Por que um adaptador, e não a interface direta
//
// `TalvezCapturar` devolve `*acervo.Captura`, e o plano de dados espera uma interface. Em Go
// esses dois tipos não casam numa assinatura de método, então a conversão acontece aqui — no
// único lugar que já conhece os dois lados.
//
// # A armadilha que ele desarma
//
// Um ponteiro nulo guardado dentro de uma interface NÃO é uma interface nula. Se este método
// devolvesse o `*Captura` nulo direto, o `if captura != nil` do proxy daria verdadeiro, e a
// primeira gravação chamaria um método em ponteiro nulo — derrubando a reprodução em
// exatamente o caso mais comum, que é o de não haver captura nenhuma.
//
// O `return nil` explícito abaixo é o que impede isso.
type acervoParaEdge struct{ *acervo.Servico }

func (a acervoParaEdge) TalvezCapturar(ctx context.Context, v *store.PlayableVariant,
	alvo *store.StreamTarget, inicio, tamanho int64, ext string) edge.CapturaDoAcervo {

	c := a.Servico.TalvezCapturar(ctx, v, alvo, inicio, tamanho, ext)
	if c == nil {
		return nil
	}
	return c
}
