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
