package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Chaves de configuração guardadas no banco.
//
// Diferente das variáveis de ambiente, estas podem ser alteradas pelo painel sem
// reiniciar o processo — são decisões do administrador, não do operador da máquina.
const (
	// SettingPublicBaseURL é o endereço pelo qual o mundo alcança este servidor.
	//
	// Sem ele, os links de reprodução saem com o endereço da requisição — que dentro da
	// própria máquina é "localhost", e não funciona em lugar nenhum além dela.
	SettingPublicBaseURL = "public_base_url"

	// SettingContentBaseURL é o endereço usado NOS LINKS DE CONTEÚDO: vídeo, lista M3U e
	// API Xtream.
	//
	// Separado do endereço do painel de propósito. O link do vídeo vai para o cliente, e
	// com ele o cliente descobre onde fica o sistema — inclusive a tela de administração.
	// Com dois domínios, o que você entrega não revela por onde você administra.
	//
	// Vazio faz o conteúdo usar o mesmo endereço do painel, que é o comportamento de
	// sempre.
	SettingContentBaseURL = "content_base_url"

	// SettingCacheLigado é a chave geral do armazenamento de mídia.
	//
	// Existe além da marcação por fonte, e as duas precisam estar ligadas para uma cópia
	// acontecer. Parece redundante e não é: a marcação por fonte é uma decisão tomada uma
	// vez, e meses depois ninguém lembra quais fontes foram marcadas. A chave geral é o
	// jeito de parar tudo — quando o disco encheu, quando a conta da nuvem estourou,
	// quando algo está errado e não se sabe o quê — sem ter de reabrir cada fonte.
	//
	// Padrão desligado. Ligar o cache é uma decisão sobre custo de disco, e ninguém deve
	// descobrir que ela foi tomada por ele ao ver a partição cheia.
	SettingCacheLigado = "cache_ligado"

	// SettingCacheBackend é o destino padrão das cópias: "local" ou "gdrive".
	SettingCacheBackend = "cache_backend"

	// SettingCacheLimiteBytes limita o quanto o CACHE pode ocupar. Zero = sem limite
	// próprio, valendo só o espaço do armazenamento.
	//
	// O acervo próprio não conta neste limite, e não poderia: ele não é descartável, então
	// um limite que o incluísse ficaria estourado para sempre sem que a limpeza tivesse o
	// que apagar.
	SettingCacheLimiteBytes = "cache_limite_bytes"

	// SettingCacheIdadeMinimaHoras é quanto tempo uma cópia fica imune à limpeza.
	//
	// Sem isso o cache entra em vaivém: guarda um filme, apaga dez minutos depois para
	// caber outro, e na hora seguinte apaga o outro para rebaixar o primeiro. Gasta banda
	// dos dois lados e não melhora nada.
	SettingCacheIdadeMinimaHoras = "cache_idade_minima_horas"

	// SettingCacheEspacoMinimoPct é a folga que o armazenamento nunca deixa de ter.
	//
	// Abaixo dela, o sistema PARA DE GUARDAR e volta a intermediar da fonte. Não é uma
	// falha: é o comportamento correto quando não há mais para onde crescer, e é preferível
	// a apagar acervo para caber mais um filme que ninguém pediu ainda.
	//
	// Em porcentagem, e não em bytes, porque a pergunta que se faz é sempre relativa —
	// "quanto do disco ainda me sobra" —, e a resposta em bytes muda de sentido quando o
	// disco muda de tamanho.
	SettingCacheEspacoMinimoPct = "cache_espaco_minimo_pct"
)

// GetSetting lê uma configuração. Ausência não é erro: devolve o padrão.
func (s *Store) GetSetting(ctx context.Context, chave, padrao string) (string, error) {
	var raw json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, chave).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return padrao, nil
	}
	if err != nil {
		return padrao, wrapErr("lendo configuração", err)
	}

	var texto string
	if err := json.Unmarshal(raw, &texto); err != nil || texto == "" {
		return padrao, nil
	}
	return texto, nil
}

// SetSetting grava uma configuração.
func (s *Store) SetSetting(ctx context.Context, chave, valor string) error {
	raw, err := json.Marshal(valor)
	if err != nil {
		return wrapErr("gravando configuração", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO settings (key, value) VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = now()`,
		chave, raw)
	return wrapErr("gravando configuração", err)
}

// AllSettings devolve todas as configurações guardadas.
func (s *Store) AllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, wrapErr("listando configurações", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var chave string
		var raw json.RawMessage
		if err := rows.Scan(&chave, &raw); err != nil {
			return nil, wrapErr("listando configurações", err)
		}
		var texto string
		if err := json.Unmarshal(raw, &texto); err == nil {
			out[chave] = texto
		}
	}
	return out, wrapErr("listando configurações", rows.Err())
}
