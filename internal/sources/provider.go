// Package sources define o contrato que todo tipo de fonte precisa cumprir.
//
// É a camada de TRANSPORTE: é aqui — e somente aqui — que credenciais são usadas e que
// URLs de mídia são materializadas. Os pacotes de parsing (m3u, xtream) continuam puros.
package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"vodmanager/internal/ingest"
)

// Config é tudo que uma fonte precisa para ser consultada.
//
// Username/Password vêm decifrados do cofre e vivem apenas em memória, durante a run.
type Config struct {
	SourceID      int64
	Kind          string
	BaseURL       string
	Username      string
	Password      string
	RequestBudget int
	Timeout       time.Duration
	UserAgent     string
}

// Redacted devolve uma cópia sem credenciais, segura para log.
func (c Config) Redacted() map[string]any {
	return map[string]any{
		"source_id":       c.SourceID,
		"kind":            c.Kind,
		"base_url":        ingest.RedactURL(c.BaseURL),
		"has_credentials": c.Username != "" || c.Password != "",
		"request_budget":  c.RequestBudget,
	}
}

// Capabilities descreve o que a fonte oferece. Permite ao orquestrador decidir o que
// nem tentar buscar.
type Capabilities struct {
	HasCategories       bool
	HasSeries           bool
	HasStableIDs        bool
	SupportsIncremental bool
	ProvidesTMDBID      bool
	ProvidesIMDBID      bool
}

// State é o estado de sincronização persistido entre runs. É o que torna o
// incremental possível (docs/07 §6.2).
type State struct {
	ProviderKind  string            `json:"provider_kind"`
	Version       int               `json:"version"`
	CatalogDigest string            `json:"catalog_digest,omitempty"`
	ItemDigests   map[string]string `json:"item_digests,omitempty"`
	FetchedAt     time.Time         `json:"fetched_at,omitempty"`
}

// DecodeState lê o estado persistido. Estado corrompido ou de outro provider é
// descartado silenciosamente: o pior efeito é uma sincronização completa a mais.
func DecodeState(raw []byte, providerKind string) State {
	var s State
	if len(raw) == 0 {
		return State{ProviderKind: providerKind}
	}
	if err := json.Unmarshal(raw, &s); err != nil || s.ProviderKind != providerKind {
		return State{ProviderKind: providerKind}
	}
	if s.ItemDigests == nil {
		s.ItemDigests = map[string]string{}
	}
	return s
}

// Encode serializa o estado para persistir.
func (s State) Encode() ([]byte, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("sources: serializando estado: %w", err)
	}
	return raw, nil
}

// Result resume uma coleta.
type Result struct {
	State State
	// Requests é quantas requisições HTTP foram feitas — a métrica que protege a fonte.
	Requests int
	// Partial indica que o teto de requisições foi atingido e o catálogo veio incompleto.
	// A run é marcada como parcial, nunca como falha: melhor catálogo incompleto e
	// honesto que fonte banida (docs/07 §6.1).
	Partial bool
	// Categories são as categorias declaradas pela fonte.
	Categories []Category
	// SkippedDetails é quantos itens tiveram detalhe pulado por não terem mudado.
	SkippedDetails int
}

// Category é uma categoria declarada pela fonte.
type Category struct {
	ExternalID  string
	Name        string
	ContentType string // "movie" | "series"
}

// Provider fala o protocolo de um tipo de fonte.
type Provider interface {
	Kind() string
	Capabilities() Capabilities

	// FetchCatalog percorre o catálogo e chama emit para cada item bruto.
	//
	// Contrato inviolável: NENHUMA URL de mídia é aberta. Só endpoints de catálogo.
	// Um erro devolvido por emit interrompe a coleta e é propagado.
	FetchCatalog(ctx context.Context, cfg Config, prev State, emit func(ingest.RawItem) error) (Result, error)

	// ResolveStreamURL materializa a URL de mídia. É a ÚNICA função de todo o sistema
	// autorizada a compor credencial com URL, e ela nunca é chamada durante o sync.
	ResolveStreamURL(cfg Config, item StreamTarget) (string, error)

	// Probe verifica se a fonte responde, sem abrir vídeo.
	Probe(ctx context.Context, cfg Config) error
}

// StreamTarget é o que se sabe de uma variante na hora de montar a URL.
type StreamTarget struct {
	OriginURL    string
	StreamRef    *ingest.StreamRef
	ContainerExt string
}

// ErrBudgetExceeded sinaliza que o teto de requisições foi atingido.
var ErrBudgetExceeded = fmt.Errorf("teto de requisições da fonte atingido")
